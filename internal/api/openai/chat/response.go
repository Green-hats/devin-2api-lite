// 本文件定义 OpenAI Chat Completions 最终 JSON 和 SSE chunk 编码。
package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/api/common"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// SSEEvent 是单个 SSE 数据行。
type SSEEvent struct {
	Name string
	Data []byte
}

// StreamEncoder 保存一次 Chat Completions 流的协议状态。
type StreamEncoder struct {
	model           string
	responseID      string
	createdAt       int64
	includeUsage    bool
	textIndex       int
	textStarted     bool
	thinkingIndex   int
	thinkingStarted bool
	toolCalls       []*toolCallState
	finished        bool
	finalUsage      llm.Usage
	finalReason     llm.StopReason
}

type toolCallState struct {
	// index 是工具调用在 OpenAI tool_calls 数组中的序号。
	index int
	// contentIndex 是该工具调用对应的上游内容块下标，用于与中间事件对齐。
	contentIndex int
	id           string
	name         string
	arguments    strings.Builder
	done         bool
}

// NewStreamEncoder 为一次 Chat Completions 流创建编码状态。
func NewStreamEncoder(model string, includeUsage bool) *StreamEncoder {
	return &StreamEncoder{
		model:         model,
		responseID:    newChatResponseID(),
		createdAt:     time.Now().Unix(),
		includeUsage:  includeUsage,
		thinkingIndex: -1,
	}
}

// EncodeResponse 把最终助手消息编码为非流式 Chat Completions JSON。
func EncodeResponse(message *llm.AssistantMessage) ([]byte, error) {
	if message == nil {
		return nil, errors.New("response message is nil")
	}
	model := message.ResponseModel
	if model == "" {
		model = message.Model
	}
	if model == "" {
		model = "devin"
	}
	messageObj, toolCalls := messageToChat(message)
	response := map[string]any{
		"id":      newChatResponseID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       messageObj,
			"finish_reason": finishReason(message.StopReason),
		}},
		"usage": chatUsage(message.Usage),
	}
	if len(toolCalls) > 0 {
		response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"] = toolCalls
	}
	return json.Marshal(response)
}

// Encode 把一个中间响应事件展开为零个或多个有序 Chat Completions SSE chunk。
func (encoder *StreamEncoder) Encode(event llm.ResponseEvent) ([]SSEEvent, error) {
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("validate response event: %w", err)
	}
	if encoder.finished {
		return nil, errors.New("chat completion stream is already done")
	}
	switch event.Type {
	case llm.ResponseEventStart:
		return encoder.start(), nil
	case llm.ResponseEventTextStart:
		return encoder.startText(event), nil
	case llm.ResponseEventTextDelta:
		return encoder.textDelta(event), nil
	case llm.ResponseEventTextEnd:
		return encoder.endText(event), nil
	case llm.ResponseEventThinkingStart:
		return encoder.startThinking(event), nil
	case llm.ResponseEventThinkingDelta:
		return encoder.thinkingDelta(event), nil
	case llm.ResponseEventThinkingEnd:
		return encoder.endThinking(event), nil
	case llm.ResponseEventToolCallStart:
		return encoder.startToolCall(event), nil
	case llm.ResponseEventToolCallDelta:
		return encoder.toolCallDelta(event), nil
	case llm.ResponseEventToolCallEnd:
		return encoder.endToolCall(event), nil
	case llm.ResponseEventDone:
		return encoder.finish(event), nil
	case llm.ResponseEventError:
		return encoder.failed(event), nil
	default:
		return nil, fmt.Errorf("unsupported response event type %q", event.Type)
	}
}

func (encoder *StreamEncoder) start() []SSEEvent {
	return []SSEEvent{encoder.chunk(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"role": "assistant"},
			"finish_reason": nil,
		}},
	})}
}

func (encoder *StreamEncoder) startText(event llm.ResponseEvent) []SSEEvent {
	encoder.textIndex = event.ContentIndex
	encoder.textStarted = true
	return nil
}

func (encoder *StreamEncoder) textDelta(event llm.ResponseEvent) []SSEEvent {
	if !encoder.textStarted {
		encoder.startText(event)
	}
	if event.Delta == "" {
		return nil
	}
	return []SSEEvent{encoder.chunk(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"content": event.Delta},
			"finish_reason": nil,
		}},
	})}
}

func (encoder *StreamEncoder) endText(event llm.ResponseEvent) []SSEEvent {
	encoder.textStarted = false
	return nil
}

func (encoder *StreamEncoder) startThinking(event llm.ResponseEvent) []SSEEvent {
	encoder.thinkingIndex = event.ContentIndex
	encoder.thinkingStarted = true
	// OpenAI Chat Completions 没有官方 reasoning 字段。
	// 这里参考 DeepSeek 等厂商的约定，用 choices[0].delta.reasoning_content 输出思考。
	return nil
}

func (encoder *StreamEncoder) thinkingDelta(event llm.ResponseEvent) []SSEEvent {
	if !encoder.thinkingStarted {
		encoder.startThinking(event)
	}
	if event.Delta == "" {
		return nil
	}
	return []SSEEvent{encoder.chunk(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"reasoning_content": event.Delta},
			"finish_reason": nil,
		}},
	})}
}

func (encoder *StreamEncoder) endThinking(event llm.ResponseEvent) []SSEEvent {
	encoder.thinkingStarted = false
	return nil
}

func (encoder *StreamEncoder) startToolCall(event llm.ResponseEvent) []SSEEvent {
	state := &toolCallState{
		index:        len(encoder.toolCalls),
		contentIndex: event.ContentIndex,
		id:           event.ToolCallID,
		name:         event.ToolName,
	}
	encoder.toolCalls = append(encoder.toolCalls, state)
	return []SSEEvent{encoder.chunk(map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    state.index,
					"id":       state.id,
					"type":     "function",
					"function": map[string]any{"name": state.name, "arguments": ""},
				}},
			},
			"finish_reason": nil,
		}},
	})}
}

func (encoder *StreamEncoder) toolCallDelta(event llm.ResponseEvent) []SSEEvent {
	state := encoder.findTool(event.ToolCallID, event.ContentIndex)
	if state == nil {
		return nil
	}
	state.arguments.WriteString(event.Delta)
	return []SSEEvent{encoder.argumentsDelta(state, event.Delta)}
}

// argumentsDelta 构造只携带工具参数增量的 chat.completion.chunk。
func (encoder *StreamEncoder) argumentsDelta(state *toolCallState, delta string) SSEEvent {
	return encoder.chunk(map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    state.index,
					"function": map[string]any{"arguments": delta},
				}},
			},
			"finish_reason": nil,
		}},
	})
}

func (encoder *StreamEncoder) endToolCall(event llm.ResponseEvent) []SSEEvent {
	state := encoder.findToolByContentIndex(event.ContentIndex)
	if state == nil && event.ToolCall != nil {
		state = encoder.findTool(event.ToolCall.ID, event.ContentIndex)
	}
	if state == nil {
		return nil
	}
	// 客户端把参数增量逐段拼成完整 JSON，因此只有在从未下发过增量时才补一段；
	// 无参数工具（或上游只在结束帧给出参数）若不补，客户端会拿到空字符串。
	hasDelta := state.arguments.Len() > 0
	state.done = true
	arguments := state.arguments.String()
	if event.ToolCall != nil {
		state.id = event.ToolCall.ID
		state.name = event.ToolCall.Name
		arguments = string(event.ToolCall.Arguments)
	}
	state.arguments.Reset()
	state.arguments.WriteString(arguments)
	if hasDelta {
		// 参数已通过增量下发，这里只校准内部状态，不再重复发送。
		return nil
	}
	if arguments == "" {
		arguments = "{}"
	}
	return []SSEEvent{encoder.argumentsDelta(state, arguments)}
}

func (encoder *StreamEncoder) finish(event llm.ResponseEvent) []SSEEvent {
	encoder.finished = true
	if event.Message != nil {
		encoder.finalUsage = event.Message.Usage
		encoder.finalReason = event.Message.StopReason
	}
	reason := finishReason(event.Reason)
	events := []SSEEvent{encoder.chunk(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": reason,
		}},
	})}
	if encoder.includeUsage {
		events = append(events, encoder.chunk(map[string]any{
			"choices": []any{},
			"usage":   chatUsage(encoder.finalUsage),
		}))
	}
	events = append(events, SSEEvent{Name: "[DONE]", Data: []byte("[DONE]")})
	return events
}

func (encoder *StreamEncoder) failed(event llm.ResponseEvent) []SSEEvent {
	encoder.finished = true
	message := "chat completion stream failed"
	if event.Error != nil && event.Error.ErrorMessage != "" {
		message = event.Error.ErrorMessage
	}
	// OpenAI Chat Completions 流式错误没有官方统一格式。
	// 这里生成一个带 error 字段的 chat.completion.chunk，
	// 让 openai-python 等客户端看到 data.error 后抛出异常。
	data, _ := json.Marshal(map[string]any{
		"id":      encoder.responseID,
		"object":  "chat.completion.chunk",
		"created": encoder.createdAt,
		"model":   encoder.model,
		"choices": []any{},
		"usage":   nil,
		"error": map[string]any{
			"message": message,
			"type":    common.OpenAIErrorType(message),
			"code":    nil,
			"param":   nil,
		},
	})
	return []SSEEvent{{Name: "", Data: data}}
}

// findTool 按工具调用 ID 查找；ID 缺失或不匹配时回退到上游内容块下标。
func (encoder *StreamEncoder) findTool(id string, contentIndex int) *toolCallState {
	if id != "" {
		for _, state := range encoder.toolCalls {
			if state.id == id {
				return state
			}
		}
	}
	return encoder.findToolByContentIndex(contentIndex)
}

// findToolByContentIndex 按上游内容块下标查找工具调用状态。
// 该下标与事件的 ContentIndex 同源，不能与 OpenAI 侧的 tool_calls 序号混用。
func (encoder *StreamEncoder) findToolByContentIndex(contentIndex int) *toolCallState {
	for _, state := range encoder.toolCalls {
		if state.contentIndex == contentIndex {
			return state
		}
	}
	return nil
}

func (encoder *StreamEncoder) chunk(payload map[string]any) SSEEvent {
	payload["id"] = encoder.responseID
	payload["object"] = "chat.completion.chunk"
	payload["created"] = encoder.createdAt
	payload["model"] = encoder.model
	if _, ok := payload["usage"]; !ok {
		payload["usage"] = nil
	}
	data, _ := json.Marshal(payload)
	return SSEEvent{Name: "", Data: data}
}

func messageToChat(message *llm.AssistantMessage) (map[string]any, []any) {
	var textParts []string
	var reasoningParts []string
	var toolCalls []any
	for _, block := range message.Content {
		switch content := block.(type) {
		case llm.TextContent:
			textParts = append(textParts, content.Text)
		case llm.ThinkingContent:
			// 非流式模式下把思考单独放到 reasoning_content，正文只放 text。
			reasoningParts = append(reasoningParts, content.Thinking)
		case llm.ToolCall:
			toolCalls = append(toolCalls, map[string]any{
				"id":       content.ID,
				"type":     "function",
				"function": map[string]any{"name": content.Name, "arguments": string(content.Arguments)},
			})
		}
	}
	messageObj := map[string]any{
		"role":    "assistant",
		"content": strings.Join(textParts, ""),
	}
	if len(reasoningParts) > 0 {
		messageObj["reasoning_content"] = strings.Join(reasoningParts, "")
	}
	if len(toolCalls) > 0 {
		messageObj["tool_calls"] = toolCalls
		messageObj["content"] = nil
	}
	return messageObj, toolCalls
}

func chatUsage(usage llm.Usage) map[string]any {
	reasoningTokens := int64(0)
	if usage.Reasoning != nil {
		reasoningTokens = *usage.Reasoning
	}
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	total := usage.TotalTokens
	if total == 0 {
		total = inputTokens + usage.Output
	}
	return map[string]any{
		"prompt_tokens":     inputTokens,
		"completion_tokens": usage.Output,
		"total_tokens":      total,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": usage.CacheRead,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
	}
}

func finishReason(reason llm.StopReason) any {
	switch reason {
	case llm.StopReasonToolUse:
		return "tool_calls"
	case llm.StopReasonLength:
		return "length"
	case llm.StopReasonStop:
		return "stop"
	case llm.StopReasonError, llm.StopReasonAborted:
		return "content_filter"
	default:
		return nil
	}
}

func newChatResponseID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(value)
}
