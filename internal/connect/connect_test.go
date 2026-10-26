// 本文件验证手写 Connect 客户端的帧解析、错误信封与一元调用行为。
package connect

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testFrame 构造任意 flags 的 Connect 流式帧。
func testFrame(flags byte, payload []byte) []byte {
	out := make([]byte, 5, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

// endStream 构造正常的流结束信封。
func endStream() []byte {
	return testFrame(flagEndStream, []byte(`{"metadata":{}}`))
}

// TestServerStreamReadsFramesAndEndStream 的测试动机是锁定请求头、帧封装与结束信封。
func TestServerStreamReadsFramesAndEndStream(t *testing.T) {
	payload := []byte("request-body")
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Content-Type"); got != "application/connect+proto" {
			t.Errorf("content type = %q", got)
		}
		if got := request.Header.Get("Connect-Protocol-Version"); got != "1" {
			t.Errorf("protocol version = %q", got)
		}
		received, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/connect+proto")
		_, _ = writer.Write(frame([]byte("one")))
		_, _ = writer.Write(frame([]byte("two")))
		_, _ = writer.Write(endStream())
	}))
	defer server.Close()

	stream, err := NewClient(server.URL, server.Client()).ServerStream(context.Background(), "/svc/Method", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, frame(payload)) {
		t.Fatalf("request body = %x, want %x", received, frame(payload))
	}
	for _, want := range []string{"one", "two"} {
		chunk, ok := stream.Receive()
		if !ok {
			t.Fatalf("receive %q failed: %v", want, stream.Err())
		}
		if string(chunk) != want {
			t.Fatalf("chunk = %q, want %q", chunk, want)
		}
	}
	if _, ok := stream.Receive(); ok {
		t.Fatal("stream should be finished")
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error = %v", err)
	}
}

// TestServerStreamSurfacesErrorEnvelope 的测试动机是保证上游错误码与文案原样透传。
func TestServerStreamSurfacesErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/connect+proto")
		_, _ = writer.Write(testFrame(flagEndStream, []byte(`{"error":{"code":"unauthenticated","message":"bad token"}}`)))
	}))
	defer server.Close()

	stream, err := NewClient(server.URL, server.Client()).ServerStream(context.Background(), "/svc/Method", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.Receive(); ok {
		t.Fatal("error envelope must not yield a message")
	}
	if err := stream.Err(); err == nil || err.Error() != "unauthenticated: bad token" {
		t.Fatalf("error = %v", err)
	}
}

// TestServerStreamRejectsTruncatedStream 的测试动机是避免把断流误判为正常结束。
func TestServerStreamRejectsTruncatedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/connect+proto")
		_, _ = writer.Write(frame([]byte("partial")))
	}))
	defer server.Close()

	stream, err := NewClient(server.URL, server.Client()).ServerStream(context.Background(), "/svc/Method", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.Receive(); !ok {
		t.Fatal("first frame should be readable")
	}
	if _, ok := stream.Receive(); ok {
		t.Fatal("truncated stream must not yield another frame")
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "without an end-stream frame") {
		t.Fatalf("error = %v", err)
	}
}

// TestServerStreamDecompressesFrames 的测试动机是兼容上游按帧压缩。
func TestServerStreamDecompressesFrames(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/connect+proto")
		_, _ = writer.Write(testFrame(flagCompressed, compressed.Bytes()))
		_, _ = writer.Write(endStream())
	}))
	defer server.Close()

	stream, err := NewClient(server.URL, server.Client()).ServerStream(context.Background(), "/svc/Method", nil)
	if err != nil {
		t.Fatal(err)
	}
	chunk, ok := stream.Receive()
	if !ok {
		t.Fatalf("receive failed: %v", stream.Err())
	}
	if string(chunk) != "hello" {
		t.Fatalf("chunk = %q", chunk)
	}
}

// TestServerStreamRejectsOversizedFrame 的测试动机是提前拒绝畸形长度，避免巨额分配。
func TestServerStreamRejectsOversizedFrame(t *testing.T) {
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:], maxMessageSize+1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/connect+proto")
		_, _ = writer.Write(header)
	}))
	defer server.Close()

	stream, err := NewClient(server.URL, server.Client()).ServerStream(context.Background(), "/svc/Method", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.Receive(); ok {
		t.Fatal("oversized frame must be rejected")
	}
	if err := stream.Err(); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v", err)
	}
}

// TestUnarySuccessAndError 的测试动机是覆盖一元调用的成功体与 Connect 错误体。
func TestUnarySuccessAndError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Content-Type"); got != "application/proto" {
			t.Errorf("content type = %q", got)
		}
		if request.URL.Path == "/svc/Fail" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"code":"unauthenticated","message":"bad token"}`))
			return
		}
		writer.Header().Set("Content-Type", "application/proto")
		_, _ = writer.Write([]byte{0x08, 0x01})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	payload, err := client.Unary(context.Background(), "/svc/Ok", []byte{0x0a, 0x01, 0x41})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{0x08, 0x01}) {
		t.Fatalf("payload = %x", payload)
	}

	if _, err := client.Unary(context.Background(), "/svc/Fail", nil); err == nil || err.Error() != "unauthenticated: bad token" {
		t.Fatalf("error = %v", err)
	}
}

// TestResponseErrorFallsBackToStatusText 的测试动机是非 JSON 错误体也能给出可读信息。
func TestResponseErrorFallsBackToStatusText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, server.Client()).Unary(context.Background(), "/svc/Method", nil)
	if err == nil || !strings.Contains(err.Error(), "http_502") {
		t.Fatalf("error = %v", err)
	}
}

// TestErrorCode 验证 Connect 错误实现了稳定错误码。
func TestErrorCode(t *testing.T) {
	err := &Error{Code: "unauthenticated", Message: "bad token"}
	if err.ErrorCode() != "unauthenticated" {
		t.Fatalf("code = %q", err.ErrorCode())
	}
	if err.Error() != "unauthenticated: bad token" {
		t.Fatalf("message = %q", err.Error())
	}
}

// FuzzReadFrame 保证畸形帧不会导致 panic。
func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 1, 'x'})
	f.Add([]byte{0x02, 0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = readFrame(bufio.NewReader(bytes.NewReader(data)))
	})
}
