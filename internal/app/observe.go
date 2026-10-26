// 本文件提供请求级日志、请求 ID 和响应状态记录。
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// requestIDKey 是请求上下文中 request ID 的私有键类型。
type requestIDKey struct{}

// requestInfoKey 是请求上下文中日志附加信息的私有键类型。
type requestInfoKey struct{}

// requestInfo 保存请求处理过程中才能确定的日志字段。
type requestInfo struct {
	model  string
	stream bool
}

// newRequestID 生成短请求 ID；随机源不可用时退化为时间戳。
func newRequestID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes[:])
}

// requestIDFrom 取出请求 ID；不存在时返回空字符串。
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestInfoFrom 取出日志附加信息；不存在时返回 nil。
func requestInfoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*requestInfo)
	return info
}

// statusRecorder 记录实际写出的状态码，并保留 Flusher 能力供 SSE 使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// WriteHeader 只透传首个状态码，避免重复写头。
func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status != 0 {
		return
	}
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

// Write 在未显式写状态码时按 200 记录，并统计字节数。
func (recorder *statusRecorder) Write(payload []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	written, err := recorder.ResponseWriter.Write(payload)
	recorder.bytes += int64(written)
	return written, err
}

// Flush 透传 Flusher，保证 SSE 不被包装层破坏。
func (recorder *statusRecorder) Flush() {
	if flusher, ok := recorder.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap 暴露底层 ResponseWriter，兼容 http.ResponseController。
func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

// statusCode 返回最终状态码；未写响应时按 200 处理。
func (recorder *statusRecorder) statusCode() int {
	if recorder.status == 0 {
		return http.StatusOK
	}
	return recorder.status
}

// SetLogger 设置应用日志器；nil 表示丢弃日志。
func (application *App) SetLogger(logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	application.logger = logger
}

// observe 为请求分配 ID，并在请求结束时输出一行结构化日志。
func (application *App) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id := newRequestID()
		info := &requestInfo{}
		ctx := context.WithValue(request.Context(), requestIDKey{}, id)
		ctx = context.WithValue(ctx, requestInfoKey{}, info)
		writer.Header().Set("X-Request-Id", id)

		recorder := &statusRecorder{ResponseWriter: writer}
		start := time.Now()
		next.ServeHTTP(recorder, request.WithContext(ctx))
		application.logger.InfoContext(ctx, "request",
			"id", id,
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.statusCode(),
			"bytes", recorder.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"model", info.model,
			"stream", info.stream,
		)
	})
}

// annotateRequest 记录请求解析后才能确定的日志字段。
func (application *App) annotateRequest(ctx context.Context, model string, stream bool) {
	if info := requestInfoFrom(ctx); info != nil {
		info.model = model
		info.stream = stream
	}
}

// logFailure 输出失败日志；5xx 用 ERROR，其余用 WARN。
func (application *App) logFailure(ctx context.Context, stage string, status int, err error) {
	attributes := []any{
		"id", requestIDFrom(ctx),
		"stage", stage,
		"status", status,
		"error", err,
	}
	if status >= http.StatusInternalServerError {
		application.logger.ErrorContext(ctx, "request_failed", attributes...)
		return
	}
	application.logger.WarnContext(ctx, "request_failed", attributes...)
}
