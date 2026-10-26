// 本文件验证手写 wire 编解码、请求组装和响应事件解码的语义。
package devin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// parsedField 是测试用的一层 wire 字段。
type parsedField struct {
	wire   int
	value  uint64
	raw    []byte
	number float64
}

// parseFields 把一段 wire 数据解析为「字段号 → 字段列表」，用于断言编码结果。
func parseFields(t *testing.T, data []byte) map[int][]parsedField {
	t.Helper()
	fields := make(map[int][]parsedField)
	r := newReader(data)
	for {
		number, ok := r.next()
		if !ok {
			break
		}
		field := parsedField{wire: r.wire}
		switch r.wire {
		case wireVarint:
			field.value, _ = r.u64()
		case wireBytes:
			field.raw, _ = r.bytes()
		case wireFixed64:
			field.number, _ = r.f64()
		default:
			r.skip()
		}
		fields[number] = append(fields[number], field)
	}
	if err := r.errOrNil(); err != nil {
		t.Fatalf("parse wire fields: %v", err)
	}
	return fields
}

func rawField(t *testing.T, fields map[int][]parsedField, number int) []byte {
	t.Helper()
	values := fields[number]
	if len(values) == 0 {
		t.Fatalf("field %d is missing", number)
	}
	if values[0].raw == nil {
		t.Fatalf("field %d is not a bytes field (wire %d)", number, values[0].wire)
	}
	return values[0].raw
}

func varintField(t *testing.T, fields map[int][]parsedField, number int) uint64 {
	t.Helper()
	values := fields[number]
	if len(values) == 0 {
		t.Fatalf("field %d is missing", number)
	}
	return values[0].value
}

// TestChatRequestEncodingMatchesUpstreamFields 的测试动机是锁定手写字段号，
// 防止后续调整时静默破坏与上游的契约。
func TestChatRequestEncodingMatchesUpstreamFields(t *testing.T) {
	messages := llm.RequestMessages{
		Model:        "glm-5-2",
		SystemPrompt: "You are helpful.",
		Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "hi"}}},
			llm.AssistantMessage{Content: []llm.Content{
				llm.TextContent{Text: "ok"},
				llm.ToolCall{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)},
			}},
			llm.ToolResultMessage{ToolCallID: "call-1", ToolName: "read_file", Content: []llm.Content{llm.TextContent{Text: "done"}}},
		},
		Tools: []llm.ToolDefinition{{
			Name:        "read_file",
			Description: "Read a file. Supports text.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"path"}}}`),
		}},
	}
	request, err := buildRequest(messages, Config{Token: "devin-session-token$test", Model: "glm-5-2"})
	if err != nil {
		t.Fatal(err)
	}

	fields := parseFields(t, request.encode())

	if got := string(rawField(t, fields, 21)); got != "glm-5-2" {
		t.Fatalf("chat_model_uid = %q", got)
	}
	if got := varintField(t, fields, 7); got != requestTypeCascade {
		t.Fatalf("request_type = %d, want %d", got, requestTypeCascade)
	}
	if got := varintField(t, fields, 20); got != plannerModeDefault {
		t.Fatalf("planner_mode = %d, want %d", got, plannerModeDefault)
	}
	if len(fields[3]) != 3 {
		t.Fatalf("chat_message_prompts count = %d, want 3", len(fields[3]))
	}

	meta := parseFields(t, rawField(t, fields, 1))
	if got := string(rawField(t, meta, 12)); got != clientName {
		t.Fatalf("extension_name = %q", got)
	}
	if got := string(rawField(t, meta, 2)); got != clientVersion {
		t.Fatalf("extension_version = %q", got)
	}
	if got := string(rawField(t, meta, 3)); got != "devin-session-token$test" {
		t.Fatalf("api_key = %q", got)
	}
	if got := len(rawField(t, meta, 31)); got != 366*2 {
		t.Fatalf("device fingerprint length = %d, want %d", got, 366*2)
	}

	assistant := parseFields(t, fields[3][1].raw)
	call := parseFields(t, rawField(t, assistant, 6))
	if got := string(rawField(t, call, 1)); got != "call-1" {
		t.Fatalf("tool call id = %q", got)
	}
	if got := string(rawField(t, call, 2)); got != "read_file" {
		t.Fatalf("tool call name = %q", got)
	}
	if got := string(rawField(t, call, 3)); got != `{"path":"a"}` {
		t.Fatalf("tool call arguments = %q", got)
	}

	result := parseFields(t, fields[3][2].raw)
	if got := varintField(t, result, 2); got != sourceTool {
		t.Fatalf("tool result source = %d, want %d", got, sourceTool)
	}
	if got := string(rawField(t, result, 7)); got != "call-1" {
		t.Fatalf("tool result call id = %q", got)
	}

	// 工具说明只能出现在 system prompt，schema 中的自然语言注解必须被剥离。
	if !strings.Contains(request.prompt, `<tool name="read_file">`) {
		t.Fatalf("system prompt lacks tool description block: %q", request.prompt)
	}
	if !strings.Contains(request.prompt, "1. Read a file.") {
		t.Fatalf("system prompt lacks numbered description: %q", request.prompt)
	}
	tool := parseFields(t, rawField(t, fields, 10))
	if got := string(rawField(t, tool, 1)); got != "read_file" {
		t.Fatalf("tool name = %q", got)
	}
	if got := string(rawField(t, tool, 2)); got != "read_file" {
		t.Fatalf("tool description should fall back to the name, got %q", got)
	}
	if schema := string(rawField(t, tool, 3)); strings.Contains(schema, `"description"`) {
		t.Fatalf("tool schema still contains annotations: %s", schema)
	}
}

// TestImagesOnlyAttachToLatestTurn 的测试动机是复现上游对历史图片的 invalid_argument 限制。
func TestImagesOnlyAttachToLatestTurn(t *testing.T) {
	image := llm.ImageContent{Data: "data:image/png;base64,aGVsbG8=", MIMEType: "image/png"}
	messages := llm.RequestMessages{
		Model: "gpt-5",
		Messages: []llm.Message{
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "first"}, image}},
			llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "ok"}}},
			llm.UserMessage{Content: []llm.Content{llm.TextContent{Text: "second"}, image}},
		},
	}
	request, err := buildRequest(messages, Config{Token: "token", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(request.prompts))
	}
	if len(request.prompts[0].images) != 0 {
		t.Fatalf("historical images must not be attached, got %d", len(request.prompts[0].images))
	}
	if !strings.Contains(request.prompts[0].prompt, "[Image omitted from history]") {
		t.Fatalf("historical image placeholder missing: %q", request.prompts[0].prompt)
	}
	if len(request.prompts[2].images) != 1 {
		t.Fatalf("latest turn images = %d, want 1", len(request.prompts[2].images))
	}
	if got := request.prompts[2].images[0].base64Data; got != "aGVsbG8=" {
		t.Fatalf("data URL prefix was not stripped: %q", got)
	}
}

// TestDecodeChatResponseFrame 的测试动机是确保每个上游帧字段都能落到正确的结构体字段。
func TestDecodeChatResponseFrame(t *testing.T) {
	var encoded buffer
	encoded.str(1, "bot-1")
	encoded.message(2, func(ts *buffer) {
		ts.uint(1, 1785334506)
		ts.uint(2, 944872162)
	})
	encoded.str(3, "你好")
	encoded.uint(5, stopReasonFunctionCall)
	encoded.message(7, func(usage *buffer) {
		usage.uint(2, 10)
		usage.uint(3, 4)
		usage.uint(5, 3)
		usage.str(9, "glm-5-2")
	})
	encoded.str(23, "glm-5-2-actual")

	response, err := decodeChatResponse(encoded.data)
	if err != nil {
		t.Fatal(err)
	}
	if response.messageID != "bot-1" {
		t.Fatalf("message_id = %q", response.messageID)
	}
	if response.deltaText != "你好" {
		t.Fatalf("delta_text = %q", response.deltaText)
	}
	if response.stopReason != stopReasonFunctionCall {
		t.Fatalf("stop_reason = %d", response.stopReason)
	}
	if response.actualModelUID != "glm-5-2-actual" {
		t.Fatalf("actual_model_uid = %q", response.actualModelUID)
	}
	if response.timestamp == nil || response.timestamp.seconds != 1785334506 || response.timestamp.nanos != 944872162 {
		t.Fatalf("timestamp = %+v", response.timestamp)
	}
	if response.usage == nil {
		t.Fatal("usage is nil")
	}
	if *response.usage.inputTokens != 10 || *response.usage.outputTokens != 4 || *response.usage.cacheRead != 3 {
		t.Fatalf("usage = %+v", response.usage)
	}
	if response.usage.modelUID != "glm-5-2" {
		t.Fatalf("usage model uid = %q", response.usage.modelUID)
	}
}

// TestDecodeChatResponseSkipsUnknownFields 的测试动机是保证上游新增字段不会导致解析失败。
func TestDecodeChatResponseSkipsUnknownFields(t *testing.T) {
	var encoded buffer
	encoded.str(99, "future field")
	encoded.uint(98, 42)
	encoded.str(3, "hello")
	response, err := decodeChatResponse(encoded.data)
	if err != nil {
		t.Fatal(err)
	}
	if response.deltaText != "hello" {
		t.Fatalf("delta_text = %q", response.deltaText)
	}
}

// TestResponseDecoderEmitsTextAndDone 的测试动机是覆盖文本块生命周期与截断停止原因。
func TestResponseDecoderEmitsTextAndDone(t *testing.T) {
	decoder := newResponseDecoder("glm-5-2")
	var events []llm.ResponseEvent
	events = append(events, decoder.decode(&chatResponse{deltaText: "你"})...)
	events = append(events, decoder.decode(&chatResponse{deltaText: "好"})...)
	events = append(events, decoder.decode(&chatResponse{stopReason: stopReasonMaxTokens})...)
	events = append(events, decoder.finish(nil)...)

	kinds := make([]llm.ResponseEventType, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Type)
	}
	want := []llm.ResponseEventType{
		llm.ResponseEventTextStart, llm.ResponseEventTextDelta,
		llm.ResponseEventTextDelta,
		llm.ResponseEventTextEnd, llm.ResponseEventDone,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	done := events[len(events)-1]
	if done.Message.StopReason != llm.StopReasonLength {
		t.Fatalf("stop reason = %q", done.Message.StopReason)
	}
	text, ok := done.Message.Content[0].(llm.TextContent)
	if !ok || text.Text != "你好" {
		t.Fatalf("final text = %#v", done.Message.Content[0])
	}
}

// TestResponseDecoderStreamsToolCallArguments 的测试动机是验证增量参数只在结束时拼接为合法 JSON。
func TestResponseDecoderStreamsToolCallArguments(t *testing.T) {
	decoder := newResponseDecoder("glm-5-2")
	var events []llm.ResponseEvent
	events = append(events, decoder.decode(&chatResponse{deltaToolCalls: []*toolCall{{id: "call-1", name: "write_file"}}})...)
	events = append(events, decoder.decode(&chatResponse{deltaToolCalls: []*toolCall{{id: "call-1", argumentsJSON: `{"path":"`}}})...)
	events = append(events, decoder.decode(&chatResponse{deltaToolCalls: []*toolCall{{id: "call-1", argumentsJSON: `a.txt"}`}}})...)
	events = append(events, decoder.decode(&chatResponse{stopReason: stopReasonFunctionCall})...)
	events = append(events, decoder.finish(nil)...)

	counts := map[llm.ResponseEventType]int{}
	for _, event := range events {
		counts[event.Type]++
	}
	if counts[llm.ResponseEventToolCallStart] != 1 || counts[llm.ResponseEventToolCallDelta] != 2 || counts[llm.ResponseEventToolCallEnd] != 1 {
		t.Fatalf("tool call event counts = %#v", counts)
	}

	done := events[len(events)-1]
	if done.Type != llm.ResponseEventDone || done.Message.StopReason != llm.StopReasonToolUse {
		t.Fatalf("done = %#v", done)
	}
	var call llm.ToolCall
	for _, block := range done.Message.Content {
		if typed, ok := block.(llm.ToolCall); ok {
			call = typed
		}
	}
	if call.ID != "call-1" || call.Name != "write_file" {
		t.Fatalf("tool call = %#v", call)
	}
	if string(call.Arguments) != `{"path":"a.txt"}` {
		t.Fatalf("tool arguments = %s", call.Arguments)
	}
}

// TestDecodeModelConfigs 的测试动机是覆盖模型目录的三个可发现字段与禁用过滤。
func TestDecodeModelConfigs(t *testing.T) {
	var encoded buffer
	encoded.message(1, func(config *buffer) {
		config.str(22, "glm-5-2")
		config.boolean(5, true)
		config.uint(10, 9)
	})
	encoded.message(1, func(config *buffer) {
		config.message(2, func(alias *buffer) { alias.str(3, "claude-x") })
		config.boolean(4, true)
	})
	encoded.message(1, func(config *buffer) {
		config.str(22, "gpt-5")
		config.uint(10, 2)
	})

	response, err := decodeModelConfigs(encoded.data)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.configs) != 3 {
		t.Fatalf("configs = %d, want 3", len(response.configs))
	}
	if response.configs[0].modelUID != "glm-5-2" || !response.configs[0].supportsImages || response.configs[0].provider != 9 {
		t.Fatalf("first config = %#v", response.configs[0])
	}
	if response.configs[1].modelUID != "claude-x" || !response.configs[1].disabled {
		t.Fatalf("second config = %#v", response.configs[1])
	}
	if providerName(response.configs[2].provider) != "openai" {
		t.Fatalf("provider name = %q", providerName(response.configs[2].provider))
	}
}

// fakeFrameStream 是 responseStream 的帧来源替身。
type fakeFrameStream struct {
	frames   [][]byte
	position int
	err      error
}

func (f *fakeFrameStream) Receive() ([]byte, bool) {
	if f.position >= len(f.frames) {
		return nil, false
	}
	frame := f.frames[f.position]
	f.position++
	return frame, true
}

func (f *fakeFrameStream) Err() error {
	return f.err
}

// TestResponseStreamDecodesFrames 的测试动机是覆盖「帧 → 中间事件流」的完整消费路径。
func TestResponseStreamDecodesFrames(t *testing.T) {
	var encoded buffer
	encoded.str(3, "hi")
	stream := &responseStream{
		upstream: &fakeFrameStream{frames: [][]byte{encoded.data}},
		decoder:  newResponseDecoder("glm-5-2"),
	}

	var events []llm.ResponseEvent
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) == 0 || events[len(events)-1].Type != llm.ResponseEventDone {
		t.Fatalf("events = %v", events)
	}
}

// TestResponseStreamSurfacesUpstreamError 的测试动机是保证 Connect 错误原文能到达客户端。
func TestResponseStreamSurfacesUpstreamError(t *testing.T) {
	stream := &responseStream{
		upstream: &fakeFrameStream{err: errors.New("resource_exhausted: quota")},
		decoder:  newResponseDecoder("glm-5-2"),
	}
	var last llm.ResponseEvent
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = event
	}
	if last.Type != llm.ResponseEventError || last.Error == nil {
		t.Fatalf("last event = %#v", last)
	}
	if !strings.Contains(last.Error.ErrorMessage, "resource_exhausted") {
		t.Fatalf("error message = %q", last.Error.ErrorMessage)
	}
}
