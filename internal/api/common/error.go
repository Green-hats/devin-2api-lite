// 本文件提供把上游 Connect/gRPC 错误码映射到 OpenAI 错误类型的工具。
package common

import "strings"

// openAIErrorTypes 把 Connect code 映射为 OpenAI 兼容的错误对象 type。
// 参考：https://platform.openai.com/docs/guides/error-codes
var openAIErrorTypes = map[string]string{
	"invalid_argument":    "invalid_request_error",
	"failed_precondition": "invalid_request_error",
	"out_of_range":        "invalid_request_error",
	"unimplemented":       "invalid_request_error",
	"unauthenticated":     "authentication_error",
	"permission_denied":   "permission_error",
	"not_found":           "not_found_error",
	"resource_exhausted":  "rate_limit_error",
	"deadline_exceeded":   "timeout_error",
	"unavailable":         "server_error",
	"internal":            "server_error",
	"unknown":             "server_error",
}

// extractErrorCode 从 "<code>: <message>" 形式的消息中提取 code。
// 如果不是 Connect 错误格式，返回空字符串。
func extractErrorCode(message string) string {
	if i := strings.Index(message, ":"); i >= 0 {
		return strings.TrimSpace(message[:i])
	}
	return ""
}

// OpenAIErrorType 把上游错误消息中的 Connect code 映射为 OpenAI error.type。
// 无法识别时返回 "server_error"。
func OpenAIErrorType(message string) string {
	if t, ok := openAIErrorTypes[extractErrorCode(message)]; ok {
		return t
	}
	return "server_error"
}
