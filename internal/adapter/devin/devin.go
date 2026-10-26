// 本文件定义 Devin adapter 的配置、组装、上游调用入口与鉴权。
//
// Package devin 负责一次 Devin GetChatMessage 调用及其响应事件转换，不执行工具或 agent loop。
package devin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/connect"
	"github.com/Green-hats/devin-2api-lite/internal/debuglog"
	"github.com/Green-hats/devin-2api-lite/internal/httpproxy"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

const (
	clientName    = "chisel"
	clientVersion = "3000.2.17"

	// procedureGetChatMessage 是 Devin 对话流式接口。
	procedureGetChatMessage = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	// procedureGetCascadeModelConfigs 是模型目录一元接口。
	procedureGetCascadeModelConfigs = "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs"
)

// Config 保存 Devin adapter 的固定上游配置。
type Config struct {
	// BaseURL 是 Devin Connect 服务的基础地址。
	BaseURL string
	// Token 是 Devin session token；不会写入日志。
	Token string
	// Model 是 Devin chat model UID。
	Model string
	// Proxy 是可选的 HTTP/HTTPS 代理地址；为空时直连或走系统环境变量。
	Proxy string
	// ForceHTTP1 为 true 时强制 HTTP/1.1，每请求独立连接，避免 HTTP/2 单连接多 stream 并发瓶颈。
	ForceHTTP1 bool
	// IdleTimeout 是上游流两次帧之间的最长等待时间；0 表示不限制。
	IdleTimeout time.Duration
	// ReasoningTiers 把基础模型 + 推理档位映射到实际 uid。
	ReasoningTiers map[string]map[string]string
	// SnapshotPath 是模型目录快照路径；为空表示不写快照。
	SnapshotPath string
	// CatalogRefresh 是模型目录缓存/刷新间隔；0 表示默认 5m。
	CatalogRefresh time.Duration
	// Logger 用于记录快照写入/回退等非请求错误；nil 表示丢弃。
	Logger *slog.Logger
	// ModelMode 控制 /v1/models 的展示方式：raw、openai 或 both。
	ModelMode string
	// AutoTiers 表示从 family metadata 自动生成档位映射。
	AutoTiers bool
}

// Adapter 调用 Devin 的 ApiServerService/GetChatMessage。
type Adapter struct {
	config         Config
	streamClient   *connect.Client
	unaryClient    *connect.Client
	modelsMu       sync.RWMutex
	models         []adapter.ModelInfo
	modelsExpiry   time.Time
	modelsCacheTTL time.Duration
	// snapshotPath 是模型目录快照路径；空表示禁用。
	snapshotPath string
	// logger 记录快照写入/回退错误。
	logger *slog.Logger
	// catalogGeneratedAt 是当前内存目录的生成时间。
	catalogGeneratedAt time.Time
	// catalogSource 是目录来源地址。
	catalogSource string
	// catalogHash 是当前目录内容的稳定摘要。
	catalogHash string
	// catalogFamilies 是根据 family metadata 生成的档位建议。
	catalogFamilies map[string]map[string]string
	// catalogStale 表示当前目录来自本地快照。
	catalogStale bool
	// imagesMu 保护从模型目录学到的图片能力，避免与硬编码名单互相矛盾。
	imagesMu     sync.RWMutex
	imageSupport map[string]bool
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.ManifestProvider = (*Adapter)(nil)

// New 创建 Devin adapter。
func New(config Config) (*Adapter, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, errors.New("devin base URL is required")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("devin token is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("devin model is required")
	}
	base, err := httpproxy.NewTransport(config.Proxy, config.ForceHTTP1)
	if err != nil {
		return nil, fmt.Errorf("create proxy transport: %w", err)
	}
	transport := &authTransport{base: base, token: config.Token}

	// SSE 流需要长期保持连接，不能设置 Client.Timeout；
	// 但 Transport 层的 ResponseHeaderTimeout 已限制首包等待时间。
	streamClient := connect.NewClient(config.BaseURL, &http.Client{Transport: transport})

	// 一元调用（模型目录）设置整体超时，避免慢请求长时间占用 goroutine。
	unaryClient := connect.NewClient(config.BaseURL, &http.Client{Transport: transport, Timeout: 610 * time.Second})

	refresh := config.CatalogRefresh
	if refresh <= 0 {
		refresh = 5 * time.Minute
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	provider := &Adapter{
		config:         config,
		streamClient:   streamClient,
		unaryClient:    unaryClient,
		modelsCacheTTL: refresh,
		snapshotPath:   config.SnapshotPath,
		logger:         logger,
		imageSupport:   make(map[string]bool),
	}
	// 自动档位可以在首请求前先从快照恢复 families，避免多一次上游调用。
	if config.AutoTiers && config.SnapshotPath != "" {
		if snapshot, snapshotErr := loadCatalogSnapshot(config.SnapshotPath); snapshotErr == nil {
			provider.catalogFamilies = snapshot.Families
			provider.catalogGeneratedAt = snapshot.GeneratedAt
			provider.catalogSource = snapshot.Source
			provider.catalogHash = snapshot.CatalogHash
		}
	}
	return provider, nil
}

// Stream 将一份中间请求转换为 Devin RPC，并返回一份中间响应事件流。
func (adapter *Adapter) Stream(ctx context.Context, request llm.RequestMessages) (llm.ResponseStream, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate Devin request: %w", err)
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = adapter.config.Model
	}
	model, err := adapter.resolveReasoning(ctx, model, request.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	if err := adapter.validateImagesForModel(request, model); err != nil {
		return nil, err
	}
	cfg := adapter.config
	cfg.Model = model
	protoRequest, err := buildRequest(request, cfg)
	if err != nil {
		return nil, err
	}
	recorder := debuglog.FromContext(ctx)
	recordRequest(recorder, protoRequest)
	upstream, err := adapter.streamClient.ServerStream(ctx, procedureGetChatMessage, protoRequest.encode())
	if err != nil {
		recorder.WriteError("devin_connect", err)
		// 透传上游 Connect 错误原文，不包一层模糊前缀。
		return nil, err
	}
	return &responseStream{
		upstream:    upstream,
		decoder:     newResponseDecoder(model),
		recorder:    recorder,
		idleTimeout: adapter.config.IdleTimeout,
	}, nil
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (transport *authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Basic "+transport.token+"-"+transport.token)
	return transport.base.RoundTrip(clone)
}
