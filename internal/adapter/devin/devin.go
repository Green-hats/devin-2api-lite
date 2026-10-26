// 本文件实现 RequestMessages 与 Devin Connect RPC 的双向转换。
//
// Package devin 负责一次 Devin GetChatMessage 调用及其响应事件转换，不执行工具或 agent loop。
package devin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	// imagesMu 保护从模型目录学到的图片能力，避免与硬编码名单互相矛盾。
	imagesMu     sync.RWMutex
	imageSupport map[string]bool
}

var _ adapter.Adapter = (*Adapter)(nil)

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

	return &Adapter{
		config:         config,
		streamClient:   streamClient,
		unaryClient:    unaryClient,
		modelsCacheTTL: 5 * time.Minute,
		imageSupport:   make(map[string]bool),
	}, nil
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

// validateImagesForModel 在本地尽早拒绝「无视觉能力模型 + 图片」组合。
// 优先使用模型目录学到的 supports_images，未知时回退到内置名单。
func (adapter *Adapter) validateImagesForModel(request llm.RequestMessages, model string) error {
	if !requestHasImages(request) {
		return nil
	}
	if supported, known := adapter.cachedImageSupport(model); known {
		if supported {
			return nil
		}
		return imageUnsupportedError(model)
	}
	if !modelLikelySupportsImages(model) {
		return imageUnsupportedError(model)
	}
	return nil
}

func imageUnsupportedError(model string) error {
	return fmt.Errorf("model %q does not support image inputs (supports_images=false); use a vision-capable model or remove images", model)
}

// cachedImageSupport 返回模型目录中记录的图片能力。
func (adapter *Adapter) cachedImageSupport(model string) (bool, bool) {
	adapter.imagesMu.RLock()
	defer adapter.imagesMu.RUnlock()
	supported, ok := adapter.imageSupport[model]
	return supported, ok
}

func requestHasImages(request llm.RequestMessages) bool {
	for _, message := range request.Messages {
		var content []llm.Content
		switch m := message.(type) {
		case llm.UserMessage:
			content = m.Content
		case llm.ToolResultMessage:
			content = m.Content
		default:
			continue
		}
		for _, block := range content {
			if _, ok := block.(llm.ImageContent); ok {
				return true
			}
		}
	}
	return false
}

// modelLikelySupportsImages 用已知无视觉模型名单；不确定时放行让上游裁决。
func modelLikelySupportsImages(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	// 与 GetCascadeModelConfigs.supports_images=false 的常见 uid 对齐。
	noVisionPrefixes := []string{
		"glm-5-2", "glm-5", "glm-4.7", "glm-4-7", "glm-4",
		"deepseek", "kimi-k2", "qwen3-coder",
	}
	for _, p := range noVisionPrefixes {
		if m == p || strings.HasPrefix(m, p+"-") || strings.HasPrefix(m, p+"_") {
			return false
		}
	}
	if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3-mini") || strings.HasPrefix(m, "o4-mini") {
		return false
	}
	return true
}

// ListModels 通过 GetCascadeModelConfigs 拉取可用模型目录，结果带 TTL 缓存。
func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	a.modelsMu.RLock()
	if a.models != nil && time.Now().Before(a.modelsExpiry) {
		cached := cloneModelInfo(a.models)
		a.modelsMu.RUnlock()
		return cached, nil
	}
	a.modelsMu.RUnlock()

	payload, err := a.unaryClient.Unary(ctx, procedureGetCascadeModelConfigs, (&modelConfigsRequest{
		metadata: &metadata{
			ideName:          clientName,
			ideVersion:       clientVersion,
			extensionName:    clientName,
			extensionVersion: clientVersion,
			apiKey:           a.config.Token,
			locale:           "en",
			os:               "win",
		},
	}).encode())
	if err != nil {
		return nil, fmt.Errorf("call GetCascadeModelConfigs: %w", err)
	}
	response, err := decodeModelConfigs(payload)
	if err != nil {
		return nil, fmt.Errorf("decode Devin model configs: %w", err)
	}

	now := time.Now().Unix()
	models := make([]adapter.ModelInfo, 0, len(response.configs))
	seen := make(map[string]struct{}, len(response.configs))
	for _, c := range response.configs {
		uid := c.modelUID
		if uid == "" || c.disabled {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		models = append(models, adapter.ModelInfo{
			ID: uid, Created: now, OwnedBy: providerName(c.provider), SupportsImages: c.supportsImages,
		})
	}
	// 用户显式配置的 model（如 gpt5.6）即使不在 Devin 返回的列表中，也应可被发现和调用。
	if configured := strings.TrimSpace(a.config.Model); configured != "" {
		if _, ok := seen[configured]; !ok {
			models = append(models, adapter.ModelInfo{
				ID: configured, Created: now, OwnedBy: "devin",
				// 与 Stream 的本地校验使用同一判断，避免 /v1/models 与真实行为矛盾。
				SupportsImages: modelLikelySupportsImages(configured),
			})
		}
	}

	a.recordImageSupport(models)

	a.modelsMu.Lock()
	defer a.modelsMu.Unlock()
	// 请求期间可能有其他请求已写入缓存，避免覆盖更热的数据。
	if a.models != nil && time.Now().Before(a.modelsExpiry) {
		return cloneModelInfo(a.models), nil
	}
	a.models = models
	a.modelsExpiry = time.Now().Add(a.modelsCacheTTL)
	return cloneModelInfo(models), nil
}

// recordImageSupport 把模型目录中的图片能力写入本地缓存。
func (a *Adapter) recordImageSupport(models []adapter.ModelInfo) {
	a.imagesMu.Lock()
	defer a.imagesMu.Unlock()
	for _, model := range models {
		a.imageSupport[model.ID] = model.SupportsImages
	}
}

// cloneModelInfo 返回缓存切片副本，避免调用方修改内部状态。
func cloneModelInfo(models []adapter.ModelInfo) []adapter.ModelInfo {
	cloned := make([]adapter.ModelInfo, len(models))
	copy(cloned, models)
	return cloned
}

// providerName 把 ModelProvider 枚举值映射为展示用归属方。
func providerName(provider int) string {
	switch provider {
	case 1:
		return "windsurf"
	case 2:
		return "openai"
	case 3:
		return "anthropic"
	case 4:
		return "google"
	case 5:
		return "xai"
	case 6:
		return "deepseek"
	case 7:
		return "moonshot"
	case 8:
		return "qwen"
	case 9:
		return "zai"
	case 10:
		return "minimax"
	case 11:
		return "nvidia"
	case 12:
		return "thinking-machines"
	default:
		return "devin"
	}
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

func buildRequest(request llm.RequestMessages, config Config) (*chatRequest, error) {
	fingerprint, err := randomHex(366)
	if err != nil {
		return nil, fmt.Errorf("generate Devin device fingerprint: %w", err)
	}
	trajectoryID := randomUUID()
	cascadeID := randomUUID()
	executionID := randomUUID()
	result := &chatRequest{
		metadata: &metadata{
			ideName:          clientName,
			ideVersion:       clientVersion,
			extensionName:    clientName,
			extensionVersion: clientVersion,
			apiKey:           config.Token,
			locale:           "en",
			os:               "mac",
			fingerprint:      fingerprint,
		},
		prompt:       withToolDescriptions(request.SystemPrompt, request.Tools),
		chatModelUID: config.Model,
		requestType:  requestTypeCascade,
		configuration: &completionConfig{
			numCompletions: 1,
			maxTokens:      128000,
			maxNewlines:    400,
			temperature:    1,
			topK:           40,
			topP:           0.95,
		},
		trajectory: &trajectoryReference{
			trajectoryID:   trajectoryID,
			trajectoryType: trajectoryTypeCascade,
			stepType:       stepTypeUserInput,
		},
		cascadeID:   cascadeID,
		plannerMode: plannerModeDefault,
		executionID: executionID,
	}
	// Devin/Cascade 只可靠接受「当前轮」图片；历史图进 Images 会 invalid_argument。
	// 当前轮 = 最后一条 AssistantMessage 之后的所有 user/tool 消息。
	// Anthropic 客户端常把 image 和 tool_result 放在同一条 user 消息里，
	// 解码后拆成 UserMessage + ToolResultMessage 两条；仅挂最后一条会丢失图片。
	lastAssistantIndex := -1
	for index, message := range request.Messages {
		if _, ok := message.(llm.AssistantMessage); ok {
			lastAssistantIndex = index
		}
	}
	for index, message := range request.Messages {
		converted, err := convertMessage(message, index > lastAssistantIndex)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		result.prompts = append(result.prompts, converted...)
	}
	for _, tool := range request.Tools {
		converted, err := convertToolDefinition(tool)
		if err != nil {
			return nil, err
		}
		result.tools = append(result.tools, converted)
	}
	return result, nil
}

// convertMessage 将中间消息转为 Devin ChatMessagePrompt。
// attachImages 为 true 时才把 ImageContent 写入 Images（仅最新用户轮）；历史图改成文本占位。
func convertMessage(message llm.Message, attachImages bool) ([]*chatPrompt, error) {
	switch message := message.(type) {
	case llm.UserMessage:
		return []*chatPrompt{promptForContent(sourceUser, message.Content, attachImages)}, nil
	case llm.AssistantMessage:
		// 助手历史不回传图片；若有意外 ImageContent 同样占位。
		prompt := promptForContent(sourceSystem, message.Content, false)
		for _, block := range message.Content {
			if call, ok := block.(llm.ToolCall); ok {
				prompt.toolCalls = append(prompt.toolCalls, &toolCall{
					id:            call.ID,
					name:          call.Name,
					argumentsJSON: string(call.Arguments),
				})
			}
		}
		return []*chatPrompt{prompt}, nil
	case llm.ToolResultMessage:
		prompt := promptForContent(sourceTool, message.Content, attachImages)
		prompt.toolCallID = message.ToolCallID
		prompt.toolResultIsError = message.IsError
		return []*chatPrompt{prompt}, nil
	default:
		return nil, fmt.Errorf("unsupported message type %T", message)
	}
}

func promptForContent(source int, content []llm.Content, attachImages bool) *chatPrompt {
	prompt := &chatPrompt{
		messageID: randomID(),
		source:    source,
	}
	var text strings.Builder
	for _, block := range content {
		switch block := block.(type) {
		case llm.TextContent:
			text.WriteString(block.Text)
		case llm.ThinkingContent:
			prompt.thinking = block.Thinking
			prompt.signature = block.ThinkingSignature
			prompt.thinkingRedacted = block.Redacted
		case llm.ImageContent:
			if !attachImages {
				// 与 WindsurfAPI 一致：历史图不进 Images，避免上游 invalid_argument。
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString("[Image omitted from history]")
				continue
			}
			// Devin/Windsurf ImageData：纯 base64（无 data: 前缀）+ mime_type。
			data := block.Data
			if strings.HasPrefix(data, "data:") {
				if _, encoded, ok := strings.Cut(data, ","); ok {
					data = encoded
				}
			}
			mimeType := block.MIMEType
			if mimeType == "" {
				mimeType = "image/png"
			}
			prompt.images = append(prompt.images, &imageData{base64Data: data, mimeType: mimeType})
		}
	}
	prompt.prompt = text.String()
	return prompt
}

func randomID() string {
	return randomUUID()
}

func randomUUID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func randomHex(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// frameStream 描述 responseStream 消费上游流所需的最小能力。
type frameStream interface {
	// Receive 前进到下一帧，并报告是否成功取得消息。
	Receive() ([]byte, bool)
	// Err 返回流结束时的错误；正常结束返回 nil。
	Err() error
	// Close 释放底层连接；可重复调用。
	Close() error
}

// responseStream 从 Connect 上游按需读取帧并依次返回 decoder 生成的事件。
type responseStream struct {
	// upstream 是 Devin Connect 返回的服务端流。
	upstream frameStream
	// decoder 将一个 Devin protobuf 帧转换为零个或多个中间响应事件。
	decoder *responseDecoder
	// recorder 记录 Devin 原始响应帧；nil 表示禁用调试日志。
	recorder *debuglog.Recorder
	// idleTimeout 是两次帧之间的最长等待时间；0 表示不限制。
	idleTimeout time.Duration
	// readErr 记录空闲超时等本地读取错误，优先于上游错误。
	readErr error
	// closeOnce 保证底层流只关闭一次。
	closeOnce sync.Once
	// closeErr 是首次关闭上游时的错误。
	closeErr error
	// started 表示是否已经请求 decoder 产生 start 事件。
	started bool
	// finished 表示 decoder 已经生成最终事件，不再读取上游。
	finished bool
	// queue 保存已经转换、等待调用方读取的中间响应事件。
	queue []llm.ResponseEvent
}

func (stream *responseStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	for len(stream.queue) == 0 && !stream.finished {
		if err := ctx.Err(); err != nil {
			return llm.ResponseEvent{}, err
		}
		if !stream.started {
			stream.started = true
			stream.queue = append(stream.queue, stream.decoder.start()...)
			break
		}
		payload, ok := stream.receive(ctx)
		if !ok {
			if err := ctx.Err(); err != nil {
				return llm.ResponseEvent{}, err
			}
			readErr := stream.readErr
			if readErr == nil {
				readErr = stream.upstream.Err()
			}
			stream.queue = append(stream.queue, stream.decoder.finish(readErr)...)
			stream.finished = true
			break
		}
		response, err := decodeChatResponse(payload)
		if err != nil {
			stream.queue = append(stream.queue, stream.decoder.fail(fmt.Errorf("decode Devin frame: %w", err))...)
			stream.finished = true
			break
		}
		recordResponse(stream.recorder, response)
		stream.queue = append(stream.queue, stream.decoder.decode(response)...)
		stream.finished = stream.decoder.finished
	}
	if len(stream.queue) > 0 {
		event := stream.queue[0]
		stream.queue = stream.queue[1:]
		return event, nil
	}
	return llm.ResponseEvent{}, io.EOF
}

// receive 在 idleTimeout 内等待下一帧；超时会关闭上游并返回 false。
func (stream *responseStream) receive(ctx context.Context) ([]byte, bool) {
	if stream.idleTimeout <= 0 {
		return stream.upstream.Receive()
	}
	type frameResult struct {
		payload []byte
		ok      bool
	}
	results := make(chan frameResult, 1)
	go func() {
		payload, ok := stream.upstream.Receive()
		results <- frameResult{payload: payload, ok: ok}
	}()
	timer := time.NewTimer(stream.idleTimeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.payload, result.ok
	case <-ctx.Done():
		_ = stream.Close()
		<-results
		return nil, false
	case <-timer.C:
		stream.readErr = fmt.Errorf("%w after %s", adapter.ErrStreamIdle, stream.idleTimeout)
		_ = stream.Close()
		<-results
		return nil, false
	}
}

// Close 关闭上游流；可重复调用。
func (stream *responseStream) Close() error {
	if stream == nil {
		return nil
	}
	stream.closeOnce.Do(func() {
		if stream.upstream != nil {
			stream.closeErr = stream.upstream.Close()
		}
	})
	return stream.closeErr
}

// errorCode 把上游/本地错误归一为稳定错误码，供 HTTP 层映射。
func errorCode(err error) string {
	var coded adapter.CodedError
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	switch {
	case errors.Is(err, adapter.ErrStreamIdle):
		return "deadline_exceeded"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, adapter.ErrUnavailable):
		return "unavailable"
	default:
		return ""
	}
}

// recordRequest 把即将发往上游的请求投影为调试 JSON。
func recordRequest(recorder *debuglog.Recorder, request *chatRequest) {
	if recorder == nil || request == nil {
		return
	}
	recorder.WriteJSON("03-devin-request.json", requestProjection(request))
}

// requestProjection 只保留排查问题时关心的字段。
func requestProjection(request *chatRequest) map[string]any {
	prompts := make([]map[string]any, 0, len(request.prompts))
	for _, prompt := range request.prompts {
		prompts = append(prompts, map[string]any{
			"message_id": prompt.messageID,
			"source":     prompt.source,
			"prompt":     prompt.prompt,
			"tool_calls": len(prompt.toolCalls),
			"images":     len(prompt.images),
		})
	}
	tools := make([]string, 0, len(request.tools))
	for _, tool := range request.tools {
		tools = append(tools, tool.name)
	}
	return map[string]any{
		"chat_model_uid": request.chatModelUID,
		"request_type":   request.requestType,
		"prompt":         request.prompt,
		"prompts":        prompts,
		"tools":          tools,
	}
}

// recordResponse 把上游原始帧投影为调试 JSONL。
func recordResponse(recorder *debuglog.Recorder, response *chatResponse) {
	if recorder == nil || response == nil {
		return
	}
	recorder.AppendJSONL("04-devin-response.jsonl", "", responseProjection(response))
}

func responseProjection(response *chatResponse) map[string]any {
	projection := map[string]any{
		"message_id":     response.messageID,
		"delta_text":     response.deltaText,
		"delta_thinking": response.deltaThinking,
		"stop_reason":    response.stopReason,
		"tool_calls":     len(response.deltaToolCalls),
	}
	if response.actualModelUID != "" {
		projection["actual_model_uid"] = response.actualModelUID
	}
	if response.usage != nil {
		projection["usage"] = map[string]any{
			"model_uid":     response.usage.modelUID,
			"input_tokens":  response.usage.inputTokens,
			"output_tokens": response.usage.outputTokens,
		}
	}
	if response.timestamp != nil {
		projection["timestamp"] = map[string]any{
			"seconds": response.timestamp.seconds,
			"nanos":   response.timestamp.nanos,
		}
	}
	return projection
}
