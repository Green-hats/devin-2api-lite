// 本文件把 Devin 请求/响应投影为调试日志。
package devin

import "github.com/Green-hats/devin-2api-lite/internal/debuglog"

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
