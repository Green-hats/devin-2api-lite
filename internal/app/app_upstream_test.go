// 本文件用假 Devin 上游验证完整链路：HTTP 编解码、适配器、Connect 帧与错误映射。
package app

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter/devin"
	"github.com/Green-hats/devin-2api-lite/internal/config"
)

const testProcedureChat = "/exa.api_server_pb.ApiServerService/GetChatMessage"
const testProcedureModels = "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs"

// connectFrame 构造 Connect 流式帧（1 字节 flags + 4 字节大端长度）。
func connectFrame(flags byte, payload []byte) []byte {
	framed := make([]byte, 5, 5+len(payload))
	framed[0] = flags
	binary.BigEndian.PutUint32(framed[1:], uint32(len(payload)))
	return append(framed, payload...)
}

// pbVarint 追加一个 base-128 变长整数。
func pbVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

// pbTag 追加字段号与 wire type。
func pbTag(field, wire int) []byte {
	return pbVarint(nil, uint64(field)<<3|uint64(wire))
}

// pbString 追加字符串字段。
func pbString(field int, value string) []byte {
	out := pbTag(field, 2)
	out = pbVarint(out, uint64(len(value)))
	return append(out, value...)
}

// pbUint 追加整数字段。
func pbUint(field int, value uint64) []byte {
	return pbVarint(pbTag(field, 0), value)
}

// pbMessage 追加嵌套消息字段。
func pbMessage(field int, payload []byte) []byte {
	out := pbTag(field, 2)
	out = pbVarint(out, uint64(len(payload)))
	return append(out, payload...)
}

// newTestGateway 启动假上游与网关，返回网关 URL。
func newTestGateway(t *testing.T, chatHandler http.HandlerFunc) (*httptest.Server, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(testProcedureChat, chatHandler)
	mux.HandleFunc(testProcedureModels, func(writer http.ResponseWriter, request *http.Request) {
		config := append(pbString(22, "glm-5-2"), pbUint(10, 9)...)
		config = append(config, pbUint(5, 0)...)
		payload := pbMessage(1, config)
		writer.Header().Set("Content-Type", "application/proto")
		_, _ = writer.Write(payload)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	provider, err := devin.New(devin.Config{
		BaseURL:    upstream.URL,
		Token:      "devin-session-token$test",
		Model:      "glm-5-2",
		ForceHTTP1: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	application := New(provider, config.ServerConfig{Listen: ":0"}, nil)
	gateway := httptest.NewServer(application.Router())
	t.Cleanup(gateway.Close)
	return upstream, gateway
}

// streamChat 返回一个按“两段文本 + 正常结束”响应 ChatMessage 的处理器。
func streamChat(t *testing.T, checkRequest func(request *http.Request, payload []byte)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		checkRequest(request, readConnectPayload(t, request))
		writer.Header().Set("Content-Type", "application/connect+proto")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("test upstream does not support flushing")
			return
		}
		write := func(frame []byte) {
			_, _ = writer.Write(frame)
			flusher.Flush()
		}
		write(connectFrame(0, pbString(3, "你好")))
		write(connectFrame(0, pbString(3, "！")))
		write(connectFrame(0x02, []byte(`{"metadata":{}}`)))
	}
}

// readConnectPayload 读取并校验流式请求的帧封装，返回 protobuf 负载。
func readConnectPayload(t *testing.T, request *http.Request) []byte {
	t.Helper()
	if got := request.Header.Get("Content-Type"); got != "application/connect+proto" {
		t.Errorf("upstream content type = %q", got)
	}
	if got := request.Header.Get("Connect-Protocol-Version"); got != "1" {
		t.Errorf("connect protocol version = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Basic devin-session-token$test-devin-session-token$test" {
		t.Errorf("authorization = %q", got)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read upstream request: %v", err)
		return nil
	}
	if len(body) < 5 {
		t.Errorf("upstream request is not framed: %d bytes", len(body))
		return nil
	}
	length := binary.BigEndian.Uint32(body[1:5])
	if int(length) != len(body)-5 {
		t.Errorf("frame length = %d, payload = %d", length, len(body)-5)
	}
	return body[5:]
}

// TestChatCompletionsStreamsFromFakeUpstream 的测试动机是验证完整 SSE 链路。
func TestChatCompletionsStreamsFromFakeUpstream(t *testing.T) {
	_, gateway := newTestGateway(t, streamChat(t, func(_ *http.Request, payload []byte) {
		if !strings.Contains(string(payload), "glm-5-2") {
			t.Errorf("upstream request lacks the model uid")
		}
	}))

	body := `{"model":"glm-5-2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	response, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	stream := string(raw)
	for _, want := range []string{`"content":"你好"`, `"content":"！"`, "data: [DONE]"} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream lacks %q:\n%s", want, stream)
		}
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
}

// TestResponsesNonStreamingFromFakeUpstream 的测试动机是验证非流式聚合与 Responses 编码。
func TestResponsesNonStreamingFromFakeUpstream(t *testing.T) {
	_, gateway := newTestGateway(t, streamChat(t, func(*http.Request, []byte) {}))

	body := `{"model":"glm-5-2","input":"hi"}`
	response, err := http.Post(gateway.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode responses body: %v\n%s", err, raw)
	}
	if decoded["object"] != "response" {
		t.Fatalf("object = %v", decoded["object"])
	}
	if !strings.Contains(string(raw), "你好！") {
		t.Fatalf("response lacks aggregated text:\n%s", raw)
	}
}

// TestUpstreamErrorMapsToHTTPStatus 的测试动机是验证 Connect 错误码到 HTTP 状态的映射。
func TestUpstreamErrorMapsToHTTPStatus(t *testing.T) {
	_, gateway := newTestGateway(t, func(writer http.ResponseWriter, request *http.Request) {
		readConnectPayload(t, request)
		writer.Header().Set("Content-Type", "application/connect+proto")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(connectFrame(0x02, []byte(`{"error":{"code":"resource_exhausted","message":"quota exceeded"}}`)))
	})

	body := `{"model":"glm-5-2","messages":[{"role":"user","content":"hi"}]}`
	response, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", response.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "resource_exhausted: quota exceeded") {
		t.Fatalf("body = %s", raw)
	}
}

// TestModelsUnaryFromFakeUpstream 的测试动机是验证一元 Connect 调用与模型目录解码。
func TestModelsUnaryFromFakeUpstream(t *testing.T) {
	_, gateway := newTestGateway(t, streamChat(t, func(*http.Request, []byte) {}))

	response, err := http.Get(gateway.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var decoded struct {
		Object string `json:"object"`
		Data   []struct {
			ID             string `json:"id"`
			OwnedBy        string `json:"owned_by"`
			SupportsImages bool   `json:"supports_images"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode models body: %v\n%s", err, raw)
	}
	if decoded.Object != "list" || len(decoded.Data) != 1 {
		t.Fatalf("models = %s", raw)
	}
	if decoded.Data[0].ID != "glm-5-2" || decoded.Data[0].OwnedBy != "zai" {
		t.Fatalf("model entry = %#v", decoded.Data[0])
	}
}

// TestHealthz 的测试动机是保证探活接口不经过鉴权与并发限制。
func TestHealthz(t *testing.T) {
	_, gateway := newTestGateway(t, streamChat(t, func(*http.Request, []byte) {}))
	response, err := http.Get(gateway.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

// TestUpstreamIdleTimeoutReturns504 验证上游只发响应头不再发帧时，网关按配置超时并返回 504。
func TestUpstreamIdleTimeoutReturns504(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/connect+proto")
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
	}))
	defer func() {
		close(release)
		upstream.Close()
	}()

	provider, err := devin.New(devin.Config{
		BaseURL:     upstream.URL,
		Token:       "test-token",
		Model:       "glm-5-2",
		ForceHTTP1:  true,
		IdleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	application := New(provider, config.ServerConfig{Listen: ":0"}, nil)
	gateway := httptest.NewServer(application.Router())
	defer gateway.Close()

	started := time.Now()
	response, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"glm-5-2","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 504: %s", response.StatusCode, raw)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("idle timeout took %s", elapsed)
	}
}
