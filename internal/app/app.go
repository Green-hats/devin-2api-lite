// 本文件定义 HTTP 应用、路由和模型目录接口。
//
// Package app 负责组装 HTTP 路由并连接 API 编解码与供应商适配器。
package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	protected := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, application.observe(application.concurrencyMiddleware(application.apiKeyMiddleware(handler))))
	}
	protected("GET /v1/models", application.listModels)
	protected("GET /v1/models/{model}", application.getModel)
	protected("GET /v1/model-manifest", application.modelManifest)
	protected("POST /v1/responses", application.createResponses)
	protected("POST /v1/chat/completions", application.createChatCompletions)
	return mux
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
		// 非 OpenAI 标准字段，供客户端识别是否可传图。
		entry["supports_images"] = m.SupportsImages
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
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": m.ID, "object": "model", "created": created, "owned_by": ownedBy,
				"supports_images": m.SupportsImages,
			})
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
