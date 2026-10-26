// 本文件验证 app 能把 OpenAI HTTP 请求交给 adapter，并按顺序输出 Responses SSE。
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/config"
	"github.com/Green-hats/devin-2api-lite/internal/connect"
	"github.com/Green-hats/devin-2api-lite/internal/debuglog"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

type fakeAdapter struct {
	lastRequest llm.RequestMessages
	lastStream  *fakeStream
	events      []llm.ResponseEvent
	manifest    adapter.Manifest
}

func (fake *fakeAdapter) Stream(_ context.Context, request llm.RequestMessages) (llm.ResponseStream, error) {
	fake.lastRequest = request
	stream := &fakeStream{events: fake.events}
	fake.lastStream = stream
	return stream, nil
}

func (fake *fakeAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "gpt-test", Created: 1, OwnedBy: "test"}}, nil
}

// ModelManifest 返回测试用的目录契约。
func (fake *fakeAdapter) ModelManifest() adapter.Manifest { return fake.manifest }

type fakeStream struct {
	events []llm.ResponseEvent
	index  int
	closed bool
}

func (stream *fakeStream) Recv(_ context.Context) (llm.ResponseEvent, error) {
	if stream.index >= len(stream.events) {
		return llm.ResponseEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

// Close 释放测试流。
func (stream *fakeStream) Close() error {
	stream.closed = true
	return nil
}

// concurrentFakeAdapter 为每个 model 返回独立的事件流，用于并发隔离测试。
type concurrentFakeAdapter struct {
	mu     sync.Mutex
	events map[string][]llm.ResponseEvent
}

func (c *concurrentFakeAdapter) Stream(_ context.Context, request llm.RequestMessages) (llm.ResponseStream, error) {
	c.mu.Lock()
	events := c.events[request.Model]
	c.mu.Unlock()
	return &fakeStream{events: events}, nil
}

func (c *concurrentFakeAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "model-a", Created: 1, OwnedBy: "test"}, {ID: "model-b", Created: 1, OwnedBy: "test"}, {ID: "model-c", Created: 1, OwnedBy: "test"}}, nil
}

// TestResponsesHandlerStreamsOrderedEvents 验证请求路由和 SSE 事件顺序。
func TestResponsesHandlerStreamsOrderedEvents(t *testing.T) {
	final := &llm.AssistantMessage{
		ResponseID:    "resp-1",
		ResponseModel: "gpt-test",
		Content:       []llm.Content{llm.TextContent{Text: "hello"}},
		StopReason:    llm.StopReasonStop,
	}
	partial := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{ResponseID: "resp-1", StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: partial},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: partial},
		{Type: llm.ResponseEventTextEnd, ContentIndex: 0, Content: "hello", Partial: partial},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true,"input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if strings.Index(body, "response.created") > strings.Index(body, "response.output_item.added") || strings.Index(body, "response.output_text.delta") > strings.Index(body, "response.completed") {
		t.Fatalf("events are out of order: %s", body)
	}
	for _, expected := range []string{"response.in_progress", "response.content_part.added", "response.content_part.done", "response.output_item.done", `"output":[{"content"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body = %s, want %s", body, expected)
		}
	}
	if len(fake.lastRequest.Messages) != 1 {
		t.Fatalf("adapter message count = %d, want 1", len(fake.lastRequest.Messages))
	}
}

// TestResponsesHandlerReturnsJSONForNonStream 验证非流式请求返回最终 JSON。
func TestResponsesHandlerReturnsJSONForNonStream(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type:   llm.ResponseEventDone,
		Reason: llm.StopReasonStop,
		Message: &llm.AssistantMessage{
			ResponseID: "resp-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop,
		},
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"object":"response"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// TestResponsesHandlerConsumesAllAssistantRounds 的测试动机是保证非流式模式消费完整事件流并返回最后一轮助手内容。
func TestResponsesHandlerConsumesAllAssistantRounds(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonToolUse, Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}, ResponseModel: "gpt-test", StopReason: llm.StopReasonToolUse}},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "last"}}, ResponseModel: "gpt-test", StopReason: llm.StopReasonStop}},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"text":"last"`) || strings.Contains(response.Body.String(), `"text":"first"`) {
		t.Fatalf("body = %s, want only last assistant round", response.Body.String())
	}
}

// TestResponsesHandlerWritesStageLogs 的测试动机是保证 HTTP 边界和中间响应事件可以按一次请求完整回放。
func TestResponsesHandlerWritesStageLogs(t *testing.T) {
	final := &llm.AssistantMessage{
		Provider: "devin", ResponseID: "resp-1", ResponseModel: "model", StopReason: llm.StopReasonStop,
	}
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final}}}
	root := filepath.Join(t.TempDir(), "logs")
	application := New(fake, config.ServerConfig{Listen: ":0"}, debuglog.NewManager(root))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("request directory count = %d, want 1", len(entries))
	}
	directory := filepath.Join(root, entries[0].Name())
	for _, name := range []string{"meta.json", "01-http-request.json", "02-request-messages.json", "05-response-events.jsonl", "06-http-response.jsonl"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	meta, err := os.ReadFile(filepath.Join(directory, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"result": "completed"`) || !strings.Contains(string(meta), `"provider": "devin"`) {
		t.Fatalf("meta = %s", meta)
	}
}

// TestResponsesHandlerMarksStreamError 的测试动机是避免已输出失败 SSE 的请求被误记为成功。
func TestResponsesHandlerMarksStreamError(t *testing.T) {
	failed := &llm.AssistantMessage{Provider: "devin", StopReason: llm.StopReasonError, ErrorMessage: "upstream failed"}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart},
		{Type: llm.ResponseEventError, Reason: llm.StopReasonError, Error: failed},
	}}
	root := filepath.Join(t.TempDir(), "logs")
	application := New(fake, config.ServerConfig{Listen: ":0"}, debuglog.NewManager(root))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","stream":true,"input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, entries[0].Name())
	meta, err := os.ReadFile(filepath.Join(directory, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"result": "failed"`) {
		t.Fatalf("meta = %s", meta)
	}
	errorLog, err := os.ReadFile(filepath.Join(directory, "error.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(errorLog), `"stage": "http_stream"`) {
		t.Fatalf("error = %s", errorLog)
	}
}

// TestResponsesHandlerRejectsMissingAPIKey 验证未提供密钥时 /v1/* 返回 401。
func TestResponsesHandlerRejectsMissingAPIKey(t *testing.T) {
	fake := &fakeAdapter{}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"type":"unauthenticated"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// TestResponsesHandlerRejectsInvalidAPIKey 验证错误密钥无法通过鉴权。
func TestResponsesHandlerRejectsInvalidAPIKey(t *testing.T) {
	fake := &fakeAdapter{}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Authorization", "Bearer wrong-key")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerAcceptsBearerAPIKey 验证 Authorization: Bearer <key> 通用格式可用。
func TestResponsesHandlerAcceptsBearerAPIKey(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{ResponseID: "resp-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop}}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret-key")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerAcceptsXApiKeyHeader 验证兼容头 X-Api-Key 也可用。
func TestResponsesHandlerAcceptsXApiKeyHeader(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{ResponseID: "resp-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop}}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "secret-key")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerHealthIsUnprotected 验证 /healthz 不受 API Key 保护。
func TestResponsesHandlerHealthIsUnprotected(t *testing.T) {
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerIgnoresLogInitializationFailure 的测试动机是保证诊断写盘故障不会改变兼容 API 的业务结果。
func TestResponsesHandlerIgnoresLogInitializationFailure(t *testing.T) {
	final := &llm.AssistantMessage{ResponseID: "resp-1", ResponseModel: "model", StopReason: llm.StopReasonStop}
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final}}}
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	application := New(fake, config.ServerConfig{Listen: ":0"}, debuglog.NewManager(blockedRoot))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestConcurrentResponsesDoNotInterleave 验证高并发下每个请求的响应流互不串扰。
func TestConcurrentResponsesDoNotInterleave(t *testing.T) {
	models := []string{"model-a", "model-b", "model-c"}
	events := make(map[string][]llm.ResponseEvent)
	for _, m := range models {
		events[m] = []llm.ResponseEvent{{
			Type:   llm.ResponseEventDone,
			Reason: llm.StopReasonStop,
			Message: &llm.AssistantMessage{
				ResponseID:    "resp-" + m,
				ResponseModel: m,
				Content:       []llm.Content{llm.TextContent{Text: "unique-response-for-" + m}},
				StopReason:    llm.StopReasonStop,
			},
		}}
	}
	fake := &concurrentFakeAdapter{events: events}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, m := range models {
			wg.Add(1)
			go func(model string) {
				defer wg.Done()
				body := fmt.Sprintf(`{"model":"%s","input":"hi"}`, model)
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				application.Router().ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Errorf("status = %d for model %s: %s", response.Code, model, response.Body.String())
					return
				}
				want := "unique-response-for-" + model
				bodyStr := response.Body.String()
				if !strings.Contains(bodyStr, want) {
					t.Errorf("response for %s missing %q: %s", model, want, bodyStr)
				}
				// 同时确认没有其它 model 的标记串入。
				for _, other := range models {
					if other == model {
						continue
					}
					if strings.Contains(bodyStr, "unique-response-for-"+other) {
						t.Errorf("response for %s contains marker of %s: %s", model, other, bodyStr)
					}
				}
			}(m)
		}
	}
	wg.Wait()
}

// TestUnavailableReturns503 验证 token 未配置时 /v1/* 返回 503 而不是 502。
func TestUnavailableReturns503(t *testing.T) {
	application := New(adapter.Unavailable{Reason: "devin.token is not configured"}, config.ServerConfig{Listen: ":0"}, nil)
	for _, path := range []string{"/v1/chat/completions", "/v1/models"} {
		method := http.MethodPost
		body := `{"model":"glm-5-2","messages":[{"role":"user","content":"hi"}]}`
		if path == "/v1/models" {
			method = http.MethodGet
			body = ""
		}
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		response := httptest.NewRecorder()
		application.Router().ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503: %s", path, response.Code, response.Body.String())
		}
	}
}

// TestAuthAndRequestID 验证 API Key 校验和每个响应都有请求 ID。
func TestAuthAndRequestID(t *testing.T) {
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret")

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d", response.Code)
	}
	if response.Header().Get("X-Request-Id") == "" {
		t.Fatal("401 response lacks X-Request-Id")
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response = httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("X-Api-Key", "secret")
	response = httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid key status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-Id") == "" {
		t.Fatal("200 response lacks X-Request-Id")
	}
}

// TestRequestBodyTooLargeReturns413 验证超过 32 MiB 的请求体返回 413。
func TestRequestBodyTooLargeReturns413(t *testing.T) {
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	body := bytes.Repeat([]byte("a"), 32<<20+1)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

// TestProviderErrorStatusMapping 验证 typed code 优先于字符串兜底。
func TestProviderErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unavailable", adapter.ErrUnavailable, http.StatusServiceUnavailable},
		{"idle", adapter.ErrStreamIdle, http.StatusGatewayTimeout},
		{"quota", fmt.Errorf("wrapped: %w", &connect.Error{Code: "resource_exhausted", Message: "quota"}), http.StatusTooManyRequests},
		{"auth", &connect.Error{Code: "unauthenticated", Message: "bad token"}, http.StatusUnauthorized},
		{"legacy", errors.New("unauthenticated: bad token"), http.StatusUnauthorized},
		{"unknown", errors.New("boom"), http.StatusBadGateway},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := mapProviderErrorStatus(testCase.err); got != testCase.want {
				t.Fatalf("status = %d, want %d", got, testCase.want)
			}
		})
	}
}

// TestIdleErrorEventReturns504 验证流内错误保留错误码并映射 504。
func TestIdleErrorEventReturns504(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type:   llm.ResponseEventError,
		Reason: llm.StopReasonError,
		Error: &llm.AssistantMessage{
			StopReason:   llm.StopReasonError,
			ErrorMessage: "upstream stream idle timeout after 5m0s",
			ErrorCode:    "deadline_exceeded",
		},
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5-2","messages":[{"role":"user","content":"hi"}]}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", response.Code, response.Body.String())
	}
}

// TestStreamIsClosedAfterRequest 验证 handler 结束时会关闭上游流。
func TestStreamIsClosedAfterRequest(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type:   llm.ResponseEventDone,
		Reason: llm.StopReasonStop,
		Message: &llm.AssistantMessage{
			ResponseID: "resp-1", StopReason: llm.StopReasonStop,
		},
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5-2","messages":[{"role":"user","content":"hi"}]}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if fake.lastStream == nil || !fake.lastStream.closed {
		t.Fatal("upstream stream was not closed")
	}
}

// TestFailuresAreLoggedWithoutDebug 验证关闭 debug 日志时失败仍会进入应用日志。
func TestFailuresAreLoggedWithoutDebug(t *testing.T) {
	var logs bytes.Buffer
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	application.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
	output := logs.String()
	if !strings.Contains(output, "request_failed") || !strings.Contains(output, "http_decode") {
		t.Fatalf("logs = %s", output)
	}
	if !strings.Contains(output, "id=") {
		t.Fatalf("logs lack request id: %s", output)
	}
}

// TestModelManifestEndpoint 验证 manifest 路由和 stale 响应头。
func TestModelManifestEndpoint(t *testing.T) {
	fake := &fakeAdapter{manifest: adapter.Manifest{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC(),
		Source:        "https://example.com",
		CatalogHash:   "hash",
		Stale:         true,
		Models:        []adapter.ModelInfo{{ID: "swe-2-high", SupportsImages: true}},
		Families:      map[string]map[string]string{"swe-2": {"high": "swe-2-high"}},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/model-manifest", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Catalog-Stale") != "true" {
		t.Fatal("stale header missing")
	}
	body := response.Body.String()
	for _, want := range []string{`"schema_version":1`, `"swe-2-high"`, `"families"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q: %s", want, body)
		}
	}
}
