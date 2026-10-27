// 本文件定义 HTTP 应用、路由和模型目录接口。
//
// Package app 负责组装 HTTP 路由并连接 API 编解码与供应商适配器。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/config"
	"github.com/Green-hats/devin-2api-lite/internal/debuglog"
)

const (
	// readHeaderTimeout 是防止慢速请求头连接长期占用资源的内部策略。
	readHeaderTimeout = 60 * time.Second
	// readTimeout 限制请求体读取总时长，防止慢速客户端长期占用连接。
	readTimeout = 120 * time.Second
	// writeTimeout 限制响应写入总时长；SSE 流可能较长，这里给足余量。
	writeTimeout = 30 * time.Minute
	// idleTimeout 是 keep-alive 连接两次请求之间的内部空闲策略。
	idleTimeout = 360 * time.Second
	// defaultMaxConcurrency 是默认同时处理的 /v1/* 请求数上限。
	defaultMaxConcurrency = 1024
)

// App 保存 HTTP 应用依赖和服务配置。
type App struct {
	// adapter 是供应商无关请求与上游协议之间的适配器。
	adapter adapter.Adapter
	// serverConfig 是 HTTP 服务运行配置。
	serverConfig config.ServerConfig
	// debugManager 为每次兼容 API 请求创建独立的写盘日志。
	debugManager *debuglog.Manager
	// apiKey 是可选的 OpenAI 兼容接口访问密钥；为空则不校验。
	apiKey string
	// logger 是请求级结构化日志；默认丢弃，由 main 注入。
	logger *slog.Logger
	// concurrency 限制同时处理的 /v1/* 请求数。
	concurrency chan struct{}
}

// New 创建一个使用指定供应商适配器的 HTTP 应用。
func New(providerAdapter adapter.Adapter, serverConfig config.ServerConfig, debugManager *debuglog.Manager) *App {
	limit := serverConfig.MaxConcurrency
	if limit <= 0 {
		limit = defaultMaxConcurrency
	}
	return &App{
		adapter:      providerAdapter,
		serverConfig: serverConfig,
		debugManager: debugManager,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		concurrency:  make(chan struct{}, limit),
	}
}

// SetAPIKey 设置 OpenAI 兼容接口的访问密钥；应在 Router/HTTPServer 之前调用。
func (application *App) SetAPIKey(apiKey string) {
	application.apiKey = apiKey
}

// Router 返回应用的 HTTP 路由。
func (application *App) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", application.health)
	// 客户端排障：插件把它在渲染进程里的异常写到这里（仅本地调试用）。
	mux.HandleFunc("POST /__clientlog", application.clientLog)
	protected := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, application.observe(application.concurrencyMiddleware(application.apiKeyMiddleware(handler))))
	}
	protected("GET /v1/models", application.listModels)
	protected("GET /v1/models/{model}", application.getModel)
	protected("GET /v1/model-manifest", application.modelManifest)
	protected("GET /v1/quota", application.quota)
	protected("POST /v1/responses", application.createResponses)
	protected("POST /v1/chat/completions", application.createChatCompletions)
	return application.readOnlyCORS(mux)
}

// readOnlyCORS 允许浏览器跨源读取只读 GET 端点：桌面端插件在渲染进程里直接读取
// 额度、健康状态和模型列表，不经由 Host 代理。写入类端点不开放跨源。
func (application *App) readOnlyCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet || request.Method == http.MethodHead {
			writer.Header().Set("Access-Control-Allow-Origin", "*")
		}
		next.ServeHTTP(writer, request)
	})
}

// HTTPServer 创建带有应用路由和超时配置的 HTTP 服务。
func (application *App) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              application.serverConfig.Listen,
		Handler:           application.Router(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

// clientLog 把浏览器端上报的日志追加到临时文件，便于排查客户端插件问题。
func (application *App) clientLog(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 16<<10))
	if err == nil && len(body) > 0 {
		path := filepath.Join(os.TempDir(), "dsh-devin2api-client.log")
		// macOS 的 TMPDIR 是每用户目录，日志位置随进程环境而定。
		if file, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); openErr == nil {
			_, _ = file.Write(body)
			if body[len(body)-1] != '\n' {
				_, _ = file.Write([]byte("\n"))
			}
			_ = file.Close()
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (application *App) health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"status":"ok"}` + "\n"))
}

// listModels 返回 OpenAI 兼容的 GET /v1/models 列表。
func (application *App) listModels(writer http.ResponseWriter, request *http.Request) {
	if application.adapter == nil {
		writeJSONError(writer, http.StatusServiceUnavailable, "provider adapter is not configured")
		return
	}
	models, err := application.adapter.ListModels(request.Context())
	if err != nil {
		status := mapProviderErrorStatus(err)
		application.logFailure(request.Context(), "provider_models", status, err)
		writeJSONError(writer, status, err.Error())
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		created := m.Created
		if created == 0 {
			created = time.Now().Unix()
		}
		ownedBy := m.OwnedBy
		if ownedBy == "" {
			ownedBy = "devin"
		}
		entry := map[string]any{
			"id": m.ID, "object": "model", "created": created, "owned_by": ownedBy,
		}
		// 非 OpenAI 标准字段，供客户端识别是否可传图/传档位。
		entry["supports_images"] = m.SupportsImages
		if len(m.ReasoningEfforts) > 0 {
			entry["reasoning_efforts"] = m.ReasoningEfforts
		}
		if m.RequiresReasoningEffort {
			entry["requires_reasoning_effort"] = true
		}
		data = append(data, entry)
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"object": "list", "data": data})
}

// getModel 返回 OpenAI 兼容的 GET /v1/models/{model}。
func (application *App) getModel(writer http.ResponseWriter, request *http.Request) {
	if application.adapter == nil {
		writeJSONError(writer, http.StatusServiceUnavailable, "provider adapter is not configured")
		return
	}
	id := request.PathValue("model")
	if id == "" {
		writeJSONError(writer, http.StatusBadRequest, "model id is required")
		return
	}
	models, err := application.adapter.ListModels(request.Context())
	if err != nil {
		status := mapProviderErrorStatus(err)
		application.logFailure(request.Context(), "provider_models", status, err)
		writeJSONError(writer, status, err.Error())
		return
	}
	for _, m := range models {
		if m.ID == id {
			created := m.Created
			if created == 0 {
				created = time.Now().Unix()
			}
			ownedBy := m.OwnedBy
			if ownedBy == "" {
				ownedBy = "devin"
			}
			writer.Header().Set("Content-Type", "application/json")
			entry := map[string]any{
				"id": m.ID, "object": "model", "created": created, "owned_by": ownedBy,
				"supports_images": m.SupportsImages,
			}
			if len(m.ReasoningEfforts) > 0 {
				entry["reasoning_efforts"] = m.ReasoningEfforts
			}
			if m.RequiresReasoningEffort {
				entry["requires_reasoning_effort"] = true
			}
			_ = json.NewEncoder(writer).Encode(entry)
			return
		}
	}
	err = fmt.Errorf("model %q not found", id)
	application.logFailure(request.Context(), "model_lookup", http.StatusNotFound, err)
	writeJSONError(writer, http.StatusNotFound, err.Error())
}

// modelManifest 返回版本化模型目录；目录尚未加载时返回 503。
func (application *App) modelManifest(writer http.ResponseWriter, _ *http.Request) {
	provider, ok := application.adapter.(adapter.ManifestProvider)
	if !ok {
		writeJSONError(writer, http.StatusServiceUnavailable, "model manifest is not available")
		return
	}
	manifest := provider.ModelManifest()
	if manifest.GeneratedAt.IsZero() {
		writeJSONError(writer, http.StatusServiceUnavailable, "model catalog is not loaded yet")
		return
	}
	if manifest.Stale {
		writer.Header().Set("X-Catalog-Stale", "true")
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(manifest)
}

// quota 返回账户额度快照；适配器不支持额度时返回 503。
func (application *App) quota(writer http.ResponseWriter, request *http.Request) {
	provider, ok := application.adapter.(adapter.QuotaProvider)
	if !ok {
		writeJSONError(writer, http.StatusServiceUnavailable, "quota is not available")
		return
	}
	quota, err := readQuota(request.Context(), provider, request.URL.Query().Get("fresh") == "1")
	if err != nil {
		status := mapProviderErrorStatus(err)
		application.logFailure(request.Context(), "provider_quota", status, err)
		writeJSONError(writer, status, err.Error())
		return
	}
	if quota.Stale {
		writer.Header().Set("X-Quota-Stale", "true")
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(quota)
}

// quotaRefresher 是可选能力：跳过缓存强制向上游取一次额度。
type quotaRefresher interface {
	RefreshQuota(context.Context) (adapter.Quota, error)
}

// readQuota 读取额度；fresh 为真时跳过缓存（适配器不支持则退回缓存读取）。
func readQuota(ctx context.Context, provider adapter.QuotaProvider, fresh bool) (adapter.Quota, error) {
	if fresh {
		if refresher, ok := provider.(quotaRefresher); ok {
			return refresher.RefreshQuota(ctx)
		}
	}
	return provider.Quota(ctx)
}
