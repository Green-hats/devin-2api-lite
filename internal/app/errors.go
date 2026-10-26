// 本文件实现 HTTP 错误响应与上游错误到状态码的映射。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/api/common"
	"github.com/Green-hats/devin-2api-lite/internal/debuglog"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

func writeJSONError(writer http.ResponseWriter, status int, message string) {
	errorType := "invalid_request_error"
	if status >= http.StatusInternalServerError {
		errorType = "server_error"
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": errorType, "code": nil, "param": nil},
	})
}

func writeAuthError(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": "unauthenticated", "code": nil, "param": nil},
	})
}

// concurrencyMiddleware 限制同时处理的 /v1/* 请求数，避免上游阻塞时资源耗尽。
func (application *App) writeLoggedError(writer http.ResponseWriter, ctx context.Context, recorder *debuglog.Recorder, stage string, status int, err error) {
	recorder.WriteError(stage, err)
	application.logFailure(ctx, stage, status, err)
	message := err.Error()
	errorType := common.OpenAIErrorType(message)
	// 客户端可修正的错误用 invalid_request_error，便于 IDE 直接展示。
	if status == http.StatusBadRequest ||
		strings.Contains(message, "does not support image") ||
		strings.Contains(message, "invalid_argument") ||
		strings.HasPrefix(message, "invalid_argument:") {
		errorType = "invalid_request_error"
		if status >= 500 {
			status = http.StatusBadRequest
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	// 透传完整 message，不改写上游文案。
	response := map[string]any{"error": map[string]any{
		"message": message,
		"type":    errorType,
		"code":    nil,
		"param":   nil,
	}}
	_ = json.NewEncoder(writer).Encode(response)
	recorder.AppendJSONL("06-http-response.jsonl", "error", response)
}

// mapProviderErrorStatus 将上游/适配器错误映射为合适的 HTTP 状态，message 仍原样透传。
// 优先使用 typed error code；字符串匹配只作为旧上游文案的兜底。
func mapProviderErrorStatus(err error) int {
	if err == nil {
		return http.StatusBadGateway
	}
	if errors.Is(err, adapter.ErrUnavailable) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, adapter.ErrStreamIdle) {
		return http.StatusGatewayTimeout
	}
	var coded adapter.CodedError
	if errors.As(err, &coded) {
		switch strings.ToLower(coded.ErrorCode()) {
		case "invalid_argument":
			return http.StatusBadRequest
		case "unauthenticated":
			return http.StatusUnauthorized
		case "permission_denied":
			return http.StatusForbidden
		case "not_found":
			return http.StatusNotFound
		case "resource_exhausted":
			return http.StatusTooManyRequests
		case "deadline_exceeded":
			return http.StatusGatewayTimeout
		case "unavailable":
			return http.StatusServiceUnavailable
		}
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "does not support image"),
		strings.Contains(msg, "invalid_argument"),
		strings.HasPrefix(msg, "invalid_argument:"),
		strings.Contains(msg, "file_id images"),
		strings.Contains(msg, "only data URL"),
		strings.Contains(msg, "validate Devin request"),
		strings.Contains(msg, "validate adapted request"):
		return http.StatusBadRequest
	case strings.Contains(msg, "unauthenticated"),
		strings.HasPrefix(msg, "unauthenticated:"):
		return http.StatusUnauthorized
	case strings.Contains(msg, "permission_denied"),
		strings.HasPrefix(msg, "permission_denied:"):
		return http.StatusForbidden
	case strings.Contains(msg, "not_found"),
		strings.HasPrefix(msg, "not_found:"):
		return http.StatusNotFound
	case strings.Contains(msg, "resource_exhausted"),
		strings.HasPrefix(msg, "resource_exhausted:"):
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

// providerMessageError 把 AssistantMessage 的稳定错误码带到 HTTP 层。
type providerMessageError struct {
	code    string
	message string
}

// Error 返回可读错误信息。
func (e *providerMessageError) Error() string { return e.message }

// ErrorCode 实现 adapter.CodedError。
func (e *providerMessageError) ErrorCode() string { return e.code }

// newProviderMessageError 从中间消息构造带错误码的 error。
func newProviderMessageError(message *llm.AssistantMessage, fallback string) error {
	if message == nil {
		return errors.New(fallback)
	}
	text := message.ErrorMessage
	if text == "" {
		text = fallback
	}
	return &providerMessageError{code: message.ErrorCode, message: text}
}
