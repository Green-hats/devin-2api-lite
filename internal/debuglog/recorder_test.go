// 本文件验证调试日志目录分配、JSON/JSONL 写入和首个错误保留。
package debuglog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestManagerWritesRequestFiles 的测试动机是保证每个请求的调试产物可独立排查。
func TestManagerWritesRequestFiles(t *testing.T) {
	manager := NewManager(t.TempDir())
	recorder := manager.Start(RequestMeta{Method: "POST", Path: "/v1/responses"})
	if recorder == nil {
		t.Fatal("recorder is nil")
	}
	recorder.WriteJSON("01-http-request.json", map[string]any{"model": "glm-5-2"})
	recorder.AppendJSONL("05-response-events.jsonl", "text_delta", map[string]any{"delta": "hi"})
	recorder.WriteError("http_decode", errors.New("bad request"))
	recorder.WriteError("http_encode", errors.New("later failure"))
	recorder.Complete(Completion{StatusCode: 200, Result: "completed", Model: "glm-5-2", Stream: true})

	for _, name := range []string{"01-http-request.json", "05-response-events.jsonl", "error.json", "meta.json"} {
		if _, err := os.Stat(filepath.Join(recorder.directory, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	errorBody, err := os.ReadFile(filepath.Join(recorder.directory, "error.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(errorBody), "http_decode") {
		t.Fatalf("first failing stage was overwritten: %s", errorBody)
	}
	metaBody, err := os.ReadFile(filepath.Join(recorder.directory, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metaBody), `"status_code": 200`) {
		t.Fatalf("meta = %s", metaBody)
	}
}

// TestManagerSeparatesConcurrentRequests 的测试动机是同一秒内的并发请求不能互相覆盖。
func TestManagerSeparatesConcurrentRequests(t *testing.T) {
	manager := NewManager(t.TempDir())
	fixed := time.Unix(1700000000, 0)
	manager.now = func() time.Time { return fixed }

	first := manager.Start(RequestMeta{Path: "/v1/responses"})
	second := manager.Start(RequestMeta{Path: "/v1/responses"})
	if first == nil || second == nil {
		t.Fatal("recorder is nil")
	}
	if first.directory == second.directory {
		t.Fatalf("both requests share %q", first.directory)
	}
	if !strings.HasSuffix(second.directory, "-02") {
		t.Fatalf("second directory = %q", second.directory)
	}
}

// TestNilRecorderIsNoop 的测试动机是关闭调试日志时调用方无需分支判断。
func TestNilRecorderIsNoop(t *testing.T) {
	var recorder *Recorder
	recorder.WriteJSON("x.json", map[string]any{})
	recorder.AppendJSONL("x.jsonl", "event", map[string]any{})
	recorder.WriteError("stage", errors.New("boom"))
	recorder.Complete(Completion{StatusCode: 500})
}
