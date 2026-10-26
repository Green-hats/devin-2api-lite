// 本文件实现请求解码、下游调用、流式写出与非流式聚合。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Green-hats/devin-2api-lite/internal/debuglog"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

func (application *App) createResponses(writer http.ResponseWriter, request *http.Request) {
	application.createCompletion(writer, request, responsesProtocol{})
}

func (application *App) createChatCompletions(writer http.ResponseWriter, request *http.Request) {
	application.createCompletion(writer, request, chatProtocol{})
}

func (application *App) createCompletion(
	writer http.ResponseWriter,
	request *http.Request,
	protocol protocol,
) {
	recorder := application.debugManager.Start(debuglog.RequestMeta{Method: request.Method, Path: request.URL.Path})
	completion := debuglog.Completion{StatusCode: http.StatusInternalServerError, Result: "failed"}
	defer func() { recorder.Complete(completion) }()

	if application.adapter == nil {
		completion.StatusCode = http.StatusServiceUnavailable
		application.writeLoggedError(writer, request.Context(), recorder, "provider_configuration", completion.StatusCode, errors.New("provider adapter is not configured"))
		return
	}
	// 图片 base64 会显著放大 JSON；与常见 IDE 多图请求对齐到 32MiB。
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 32<<20))
	if err != nil {
		completion.StatusCode = http.StatusRequestEntityTooLarge
		var maxBytesError *http.MaxBytesError
		if !errors.As(err, &maxBytesError) {
			completion.StatusCode = http.StatusBadRequest
		}
		application.writeLoggedError(writer, request.Context(), recorder, "http_read", completion.StatusCode, fmt.Errorf("read request: %w", err))
		return
	}
	recorder.WriteJSON("01-http-request.json", httpRequestProjection(request, body))
	messages, options, err := protocol.decode(body)
	if err != nil {
		completion.StatusCode = http.StatusBadRequest
		application.writeLoggedError(writer, request.Context(), recorder, "http_decode", completion.StatusCode, err)
		return
	}
	completion.Model = messages.Model
	completion.Stream = options.Stream
	application.annotateRequest(request.Context(), messages.Model, options.Stream)
	recorder.WriteJSON("02-request-messages.json", messages)
	ctx := debuglog.WithRecorder(request.Context(), recorder)
	stream, err := application.adapter.Stream(ctx, messages)
	if err != nil {
		completion.StatusCode = mapProviderErrorStatus(err)
		application.writeLoggedError(writer, request.Context(), recorder, "provider_stream", completion.StatusCode, err)
		return
	}
	defer func() { _ = stream.Close() }()
	if options.Stream {
		firstEvent, err := receiveEvent(ctx, stream, recorder)
		if err == nil && firstEvent.Type == llm.ResponseEventError {
			err = newProviderMessageError(firstEvent.Error, "response stream returned an error event immediately")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			completion.StatusCode = mapProviderErrorStatus(err)
			application.writeLoggedError(writer, request.Context(), recorder, "provider_stream", completion.StatusCode, err)
			return
		}

		completion.StatusCode = http.StatusOK
		message, streamErr := writeProtocolStream(ctx, writer, stream, recorder, protocol, messages.Model, options, firstEvent, err)
		updateCompletionIdentity(&completion, message)
		if streamErr != nil {
			if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
				completion.Result = "disconnected"
				recorder.WriteError("client_disconnected", streamErr)
			} else {
				recorder.WriteError("http_stream", streamErr)
			}
			return
		}
		completion.Result = "completed"
		return
	}
	message, err := collectFinalMessage(ctx, stream, recorder)
	if err != nil {
		completion.StatusCode = mapProviderErrorStatus(err)
		application.writeLoggedError(writer, request.Context(), recorder, "response_event", completion.StatusCode, err)
		return
	}
	updateCompletionIdentity(&completion, message)
	body, err = protocol.EncodeFinal(message)
	if err != nil {
		completion.StatusCode = http.StatusInternalServerError
		application.writeLoggedError(writer, request.Context(), recorder, "http_encode", completion.StatusCode, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write(body); err != nil {
		completion.Result = "disconnected"
		recorder.WriteError("client_disconnected", err)
		return
	}
	recorder.AppendJSONL("06-http-response.jsonl", "response", json.RawMessage(body))
	completion.StatusCode = http.StatusOK
	completion.Result = "completed"
}

func writeProtocolStream(
	ctx context.Context,
	writer http.ResponseWriter,
	stream llm.ResponseStream,
	recorder *debuglog.Recorder,
	protocol protocol,
	model string,
	options protocolOptions,
	firstEvent llm.ResponseEvent,
	firstErr error,
) (*llm.AssistantMessage, error) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming response writer does not support flushing")
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	// 避免 nginx 等反代缓冲 SSE；本地直连无影响。
	writer.Header().Set("X-Accel-Buffering", "no")
	encoder := protocol.NewStreamEncoder(model, options.IncludeUsage)
	var latest *llm.AssistantMessage
	event, err := firstEvent, firstErr
	for {
		if errors.Is(err, io.EOF) {
			return latest, nil
		}
		if err != nil {
			return latest, err
		}
		latest = eventMessage(event, latest)
		encodedEvents, encodeErr := encoder.Encode(event)
		if encodeErr != nil {
			return latest, encodeErr
		}
		for _, encoded := range encodedEvents {
			if _, wErr := writer.Write(protocol.SSEFormat(encoded.Name, encoded.Data)); wErr != nil {
				return latest, wErr
			}
			if encoded.Name == "[DONE]" {
				recorder.AppendJSONL("06-http-response.jsonl", encoded.Name, string(encoded.Data))
			} else {
				recorder.AppendJSONL("06-http-response.jsonl", encoded.Name, json.RawMessage(encoded.Data))
			}
			flusher.Flush()
		}
		if event.Type == llm.ResponseEventError {
			// 错误 SSE 已在上面循环写出，这里直接返回错误供外层记录失败日志。
			return latest, newProviderMessageError(event.Error, "response stream returned an error event")
		}
		event, err = receiveEvent(ctx, stream, recorder)
	}
}

func collectFinalMessage(ctx context.Context, stream llm.ResponseStream, recorder *debuglog.Recorder) (*llm.AssistantMessage, error) {
	var final *llm.AssistantMessage
	for {
		event, err := receiveEvent(ctx, stream, recorder)
		if errors.Is(err, io.EOF) {
			if final == nil {
				return nil, errors.New("response stream ended without a final message")
			}
			return final, nil
		}
		if err != nil {
			return nil, err
		}
		switch event.Type {
		case llm.ResponseEventDone:
			if event.Message == nil {
				return nil, errors.New("done event has no final message")
			}
			final = event.Message
		case llm.ResponseEventError:
			if event.Error == nil {
				return nil, errors.New("error event has no error message")
			}
			return nil, newProviderMessageError(event.Error, "response stream returned an error")
		}
	}
}

func receiveEvent(ctx context.Context, stream llm.ResponseStream, recorder *debuglog.Recorder) (llm.ResponseEvent, error) {
	event, err := stream.Recv(ctx)
	if err == nil {
		recorder.AppendJSONL("05-response-events.jsonl", string(event.Type), event)
	}
	return event, err
}

func eventMessage(event llm.ResponseEvent, fallback *llm.AssistantMessage) *llm.AssistantMessage {
	if event.Message != nil {
		return event.Message
	}
	if event.Error != nil {
		return event.Error
	}
	if event.Partial != nil {
		return event.Partial
	}
	return fallback
}

func updateCompletionIdentity(completion *debuglog.Completion, message *llm.AssistantMessage) {
	if message == nil {
		return
	}
	completion.Provider = message.Provider
	if message.ResponseModel != "" {
		completion.Model = message.ResponseModel
	} else if message.Model != "" {
		completion.Model = message.Model
	}
}

func httpRequestProjection(request *http.Request, body []byte) map[string]any {
	var parsedBody any
	if err := json.Unmarshal(body, &parsedBody); err != nil {
		parsedBody = string(body)
	}
	return map[string]any{
		"method": request.Method,
		"path":   request.URL.Path,
		"headers": map[string]string{
			"accept":       request.Header.Get("Accept"),
			"content_type": request.Header.Get("Content-Type"),
			"user_agent":   request.Header.Get("User-Agent"),
		},
		"body": parsedBody,
	}
}
