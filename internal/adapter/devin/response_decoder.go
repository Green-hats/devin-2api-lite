// 本文件负责把 Devin protobuf 响应帧解释为有序的中间响应事件。
package devin

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// responseDecoder 保存一次 Devin 请求内的响应累计状态和内容映射。
type responseDecoder struct {
	// model 是请求使用的 Devin 模型标识。
	model string
	// partial 是当前累计形成的助手消息。
	partial llm.AssistantMessage
	// text 是当前累计的文字块。
	text *llm.TextContent
	// textBuilder 累计文字增量，避免 O(n²) 字符串拼接。
	textBuilder strings.Builder
	// textIdx 是文字块在 partial.Content 中的位置。
	textIdx int
	// textOpen 表示文字块已经开始但尚未结束。
	textOpen bool
	// thinking 是当前累计的思考块。
	thinking *llm.ThinkingContent
	// thinkingBuilder 累计思考正文。
	thinkingBuilder strings.Builder
	// thinkingSigBuilder 累计思考签名。
	thinkingSigBuilder strings.Builder
	// thinkIdx 是思考块在 partial.Content 中的位置。
	thinkIdx int
	// thinkingOpen 表示思考块已经开始但尚未结束。
	thinkingOpen bool
	// tools 保存正在累计参数的工具调用。
	tools []*toolState
	// started 表示已经生成 start 事件。
	started bool
	// finished 表示已经生成 done 或 error 事件。
	finished bool
	// hasStopReason 表示 Devin 已经显式返回停止原因。
	hasStopReason bool
	// stopReason 保存 Devin 声明的最终停止原因，等待上游 EOF 后用于完成响应。
	stopReason llm.StopReason
}

// toolState 保存一次 Devin 工具调用的累计状态。
type toolState struct {
	// call 是当前累计形成的工具调用。
	call llm.ToolCall
	// contentIdx 是调用在 partial.Content 中的位置。
	contentIdx int
	// arguments 累计 Devin 返回的工具参数 JSON 片段。
	arguments strings.Builder
	// emitted 表示原始工具调用已经加入 partial 并产生 start 事件。
	emitted bool
}

func newResponseDecoder(model string) *responseDecoder {
	return &responseDecoder{model: model}
}

func (decoder *responseDecoder) start() []llm.ResponseEvent {
	if decoder.started || decoder.finished {
		return nil
	}
	decoder.started = true
	decoder.partial = llm.AssistantMessage{
		API: "connect", Provider: "devin", Model: decoder.model,
		StopReason: llm.StopReasonPending, TimestampMS: time.Now().UnixMilli(),
	}
	return []llm.ResponseEvent{{Type: llm.ResponseEventStart, Partial: &decoder.partial}}
}

func (decoder *responseDecoder) decode(response *chatResponse) []llm.ResponseEvent {
	if response == nil || decoder.finished {
		return nil
	}
	decoder.updateMetadata(response)
	events := make([]llm.ResponseEvent, 0, 6)
	if response.deltaThinking != "" || response.deltaSignature != "" || response.thinkingRedacted {
		events = append(events, decoder.endText()...)
		events = append(events, decoder.decodeThinking(response)...)
	}
	if response.deltaText != "" {
		events = append(events, decoder.endThinking()...)
		events = append(events, decoder.decodeText(response.deltaText)...)
	}
	for _, delta := range response.deltaToolCalls {
		events = append(events, decoder.endThinking()...)
		events = append(events, decoder.endText()...)
		events = append(events, decoder.decodeTool(delta)...)
	}
	if response.stopReason != 0 {
		decoder.hasStopReason = true
		decoder.stopReason = mapStopReason(response.stopReason)
	}
	return events
}

func (decoder *responseDecoder) finish(upstreamErr error) []llm.ResponseEvent {
	if decoder.finished {
		return nil
	}
	if upstreamErr != nil {
		// 流中途/结束时的 Connect 错误同样透传原文。
		return decoder.fail(upstreamErr)
	}
	if !decoder.hasStopReason && len(decoder.partial.Content) == 0 && len(decoder.tools) == 0 {
		return decoder.fail(errors.New("stream ended without generated content"))
	}
	reason := decoder.stopReason
	if !decoder.hasStopReason {
		reason = llm.StopReasonStop
	}
	if reason == llm.StopReasonError {
		return decoder.fail(errors.New("stream stopped with an error"))
	}
	return decoder.complete(reason)
}

func (decoder *responseDecoder) updateMetadata(response *chatResponse) {
	if response.messageID != "" {
		decoder.partial.ResponseID = response.messageID
	}
	if response.actualModelUID != "" {
		decoder.partial.ResponseModel = response.actualModelUID
	}
	if timestamp := response.timestamp; timestamp != nil {
		decoder.partial.TimestampMS = time.Unix(timestamp.seconds, int64(timestamp.nanos)).UnixMilli()
	}
	if usage := response.usage; usage != nil {
		if decoder.partial.ResponseModel == "" && usage.modelUID != "" {
			decoder.partial.ResponseModel = usage.modelUID
		}
		if usage.inputTokens != nil {
			decoder.partial.Usage.Input = int64(*usage.inputTokens)
		}
		if usage.outputTokens != nil {
			decoder.partial.Usage.Output = int64(*usage.outputTokens)
		}
		if usage.cacheRead != nil {
			decoder.partial.Usage.CacheRead = int64(*usage.cacheRead)
		}
		if usage.cacheWrite != nil {
			decoder.partial.Usage.CacheWrite = int64(*usage.cacheWrite)
		}
		decoder.partial.Usage.TotalTokens = decoder.partial.Usage.Input + decoder.partial.Usage.Output + decoder.partial.Usage.CacheRead + decoder.partial.Usage.CacheWrite
	}
}

func (decoder *responseDecoder) decodeThinking(response *chatResponse) []llm.ResponseEvent {
	events := make([]llm.ResponseEvent, 0, 2)
	if !decoder.thinkingOpen {
		decoder.thinking = &llm.ThinkingContent{Redacted: response.thinkingRedacted}
		decoder.thinkingBuilder.Reset()
		decoder.thinkingSigBuilder.Reset()
		decoder.partial.Content = append(decoder.partial.Content, *decoder.thinking)
		decoder.thinkIdx = len(decoder.partial.Content) - 1
		decoder.thinkingOpen = true
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventThinkingStart, ContentIndex: decoder.thinkIdx, Partial: &decoder.partial})
	}
	if delta := response.deltaThinking; delta != "" {
		decoder.thinkingBuilder.WriteString(delta)
	}
	if sig := response.deltaSignature; sig != "" {
		decoder.thinkingSigBuilder.WriteString(sig)
	}
	decoder.thinking.Redacted = decoder.thinking.Redacted || response.thinkingRedacted
	// 思考正文在 endThinking 再 materialize；签名通常较短，每帧同步签名避免 startReasoning 拿不到。
	decoder.thinking.ThinkingSignature = decoder.thinkingSigBuilder.String()
	decoder.partial.Content[decoder.thinkIdx] = *decoder.thinking
	if delta := response.deltaThinking; delta != "" {
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventThinkingDelta, ContentIndex: decoder.thinkIdx, Delta: delta, Partial: &decoder.partial})
	}
	return events
}

func (decoder *responseDecoder) decodeText(delta string) []llm.ResponseEvent {
	events := make([]llm.ResponseEvent, 0, 2)
	if !decoder.textOpen {
		decoder.text = &llm.TextContent{}
		decoder.textBuilder.Reset()
		decoder.partial.Content = append(decoder.partial.Content, *decoder.text)
		decoder.textIdx = len(decoder.partial.Content) - 1
		decoder.textOpen = true
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventTextStart, ContentIndex: decoder.textIdx, Partial: &decoder.partial})
	}
	// 用 Builder 累加，避免每帧产生越来越大的新字符串。
	decoder.textBuilder.WriteString(delta)
	events = append(events, llm.ResponseEvent{Type: llm.ResponseEventTextDelta, ContentIndex: decoder.textIdx, Delta: delta, Partial: &decoder.partial})
	return events
}

func (decoder *responseDecoder) decodeTool(delta *toolCall) []llm.ResponseEvent {
	if delta == nil {
		return nil
	}
	state := decoder.findTool(delta.id)
	if state == nil {
		state = &toolState{
			call:       llm.ToolCall{ID: delta.id, Name: delta.name, Arguments: json.RawMessage(`{}`)},
			contentIdx: -1,
		}
		decoder.tools = append(decoder.tools, state)
	}
	if delta.id != "" {
		state.call.ID = delta.id
	}
	if delta.name != "" {
		state.call.Name = delta.name
	}
	fragment := delta.argumentsJSON
	hasFragment := delta.argumentsJSON != ""
	if hasFragment {
		state.arguments.WriteString(fragment)
	}
	return decoder.decodeNativeTool(state, fragment, hasFragment)
}

// decodeNativeTool 保留 Devin 原生工具名称和参数增量语义。
func (decoder *responseDecoder) decodeNativeTool(state *toolState, fragment string, hasFragment bool) []llm.ResponseEvent {
	events := make([]llm.ResponseEvent, 0, 2)
	if !state.emitted {
		state.contentIdx = len(decoder.partial.Content)
		state.emitted = true
		decoder.partial.Content = append(decoder.partial.Content, state.call)
		events = append(events, llm.ResponseEvent{
			Type: llm.ResponseEventToolCallStart, ContentIndex: state.contentIdx,
			ToolCallID: state.call.ID, ToolName: state.call.Name, Partial: &decoder.partial,
		})
	}
	// 工具参数在 complete 中一次性解析并写入，避免每帧 O(n) 拷贝/校验。
	if hasFragment {
		events = append(events, llm.ResponseEvent{
			Type: llm.ResponseEventToolCallDelta, ContentIndex: state.contentIdx,
			ToolCallID: state.call.ID, Delta: fragment, Partial: &decoder.partial,
		})
	}
	return events
}

func (decoder *responseDecoder) endThinking() []llm.ResponseEvent {
	if !decoder.thinkingOpen || decoder.thinking == nil {
		return nil
	}
	decoder.thinkingOpen = false
	// 只在思考块结束时一次性生成完整思考与签名。
	decoder.thinking.Thinking = decoder.thinkingBuilder.String()
	decoder.thinking.ThinkingSignature = decoder.thinkingSigBuilder.String()
	decoder.partial.Content[decoder.thinkIdx] = *decoder.thinking
	return []llm.ResponseEvent{{
		Type: llm.ResponseEventThinkingEnd, ContentIndex: decoder.thinkIdx,
		Content: decoder.thinking.Thinking, Partial: &decoder.partial,
	}}
}

func (decoder *responseDecoder) endText() []llm.ResponseEvent {
	if !decoder.textOpen || decoder.text == nil {
		return nil
	}
	decoder.textOpen = false
	// 只在内容块结束时一次性生成完整文字，避免 O(n²) 拷贝。
	decoder.text.Text = decoder.textBuilder.String()
	decoder.partial.Content[decoder.textIdx] = *decoder.text
	return []llm.ResponseEvent{{
		Type: llm.ResponseEventTextEnd, ContentIndex: decoder.textIdx,
		Content: decoder.text.Text, Partial: &decoder.partial,
	}}
}

func (decoder *responseDecoder) findTool(id string) *toolState {
	for _, state := range decoder.tools {
		if id != "" && state.call.ID == id {
			return state
		}
	}
	if id == "" && len(decoder.tools) > 0 {
		return decoder.tools[len(decoder.tools)-1]
	}
	return nil
}

func (decoder *responseDecoder) complete(reason llm.StopReason) []llm.ResponseEvent {
	if decoder.finished {
		return nil
	}
	events := make([]llm.ResponseEvent, 0, len(decoder.tools)*3+3)
	decoder.partial.StopReason = reason
	events = append(events, decoder.endThinking()...)
	events = append(events, decoder.endText()...)
	for _, state := range decoder.tools {
		if !state.emitted {
			continue
		}
		// 在结束时一次性把 Builder 中的完整参数转成 JSON，避免中间反复解析/拷贝。
		state.call.Arguments = json.RawMessage(state.arguments.String())
		if !isJSONObject(state.call.Arguments) {
			state.call.Arguments = json.RawMessage(`{}`)
		}
		decoder.partial.Content[state.contentIdx] = state.call
		events = append(events, llm.ResponseEvent{Type: llm.ResponseEventToolCallEnd, ContentIndex: state.contentIdx, ToolCall: &state.call, Partial: &decoder.partial})
	}
	events = append(events, llm.ResponseEvent{Type: llm.ResponseEventDone, Reason: reason, Message: &decoder.partial})
	decoder.finished = true
	return events
}

func (decoder *responseDecoder) fail(err error) []llm.ResponseEvent {
	if decoder.finished {
		return nil
	}
	decoder.partial.StopReason = llm.StopReasonError
	decoder.partial.ErrorMessage = err.Error()
	decoder.partial.ErrorCode = errorCode(err)
	decoder.finished = true
	return []llm.ResponseEvent{{Type: llm.ResponseEventError, Reason: llm.StopReasonError, Error: &decoder.partial}}
}

func mapStopReason(reason int) llm.StopReason {
	switch reason {
	case stopReasonMaxTokens, stopReasonIncomplete, stopReasonPartial:
		// INCOMPLETE/PARTIAL 都表示模型没有生成完整回复，按长度截断处理。
		return llm.StopReasonLength
	case stopReasonFunctionCall:
		return llm.StopReasonToolUse
	case stopReasonError:
		return llm.StopReasonError
	default:
		return llm.StopReasonStop
	}
}
