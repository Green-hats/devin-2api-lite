// 本文件验证 -check 自检输出与空 token 报错。
package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/config"
)

// pbVarint 追加一个 base-128 变长整数。
func pbVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

// pbString 追加字符串字段。
func pbString(field int, value string) []byte {
	out := pbVarint(nil, uint64(field)<<3|2)
	out = pbVarint(out, uint64(len(value)))
	return append(out, value...)
}

// pbUint 追加整数字段。
func pbUint(field int, value uint64) []byte {
	return pbVarint(pbVarint(nil, uint64(field)<<3), value)
}

// pbMessage 追加嵌套消息字段。
func pbMessage(field int, payload []byte) []byte {
	out := pbVarint(nil, uint64(field)<<3|2)
	out = pbVarint(out, uint64(len(payload)))
	return append(out, payload...)
}

// TestRunCheckWithFakeUpstream 验证自检能解析模型目录并输出关键字段。
func TestRunCheckWithFakeUpstream(t *testing.T) {
	model := pbString(22, "glm-5-2")
	model = append(model, pbUint(5, 1)...)
	model = append(model, pbUint(10, 9)...)
	payload := pbMessage(1, model)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs" {
			t.Errorf("path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/proto")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	forceHTTP1 := true
	serviceConfig := config.Config{
		Devin: config.DevinConfig{
			BaseURL:     server.URL,
			Token:       "test-token",
			Model:       "glm-5-2",
			ForceHTTP1:  &forceHTTP1,
			IdleTimeout: 5 * time.Minute,
		},
	}
	var out bytes.Buffer
	if err := runCheck(&serviceConfig, "config.yaml", &out, testLogger()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"models      1", "vision 1", "found=true", "glm-5-2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lacks %q:\n%s", want, text)
		}
	}
}

// familyModelPayload 构造一个带 family metadata 的模型目录响应。
func familyModelPayload() []byte {
	value := append(pbUint(1, 1), pbString(2, "High")...)
	entry := append(pbString(1, "Reasoning Effort"), pbMessage(2, value)...)
	family := append(pbString(1, "SWE-2"), pbMessage(2, entry)...)
	family = append(family, pbUint(3, 1)...)

	model := pbString(1, "SWE-2 High")
	model = append(model, pbString(22, "swe-2-high")...)
	model = append(model, pbUint(5, 1)...)
	model = append(model, pbUint(10, 1)...)
	model = append(model, pbUint(18, 262000)...)
	model = append(model, pbMessage(30, family)...)
	model = append(model, pbUint(31, 1)...)
	return pbMessage(1, model)
}

// TestRunSyncModelsWithFakeUpstream 验证 -sync-models 会拉取、写快照并报告实际/可见数量。
func TestRunSyncModelsWithFakeUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs" {
			t.Errorf("path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/proto")
		_, _ = writer.Write(familyModelPayload())
	}))
	defer server.Close()

	snapshotPath := filepath.Join(t.TempDir(), "state", "models.json")
	forceHTTP1 := true
	serviceConfig := config.Config{
		Devin: config.DevinConfig{
			BaseURL: server.URL, Token: "test-token", Model: "swe-2-high",
			ForceHTTP1: &forceHTTP1, IdleTimeout: 5 * time.Minute,
		},
		Catalog: config.CatalogConfig{Snapshot: snapshotPath},
	}
	var out bytes.Buffer
	if err := runSyncModels(&serviceConfig, "config.yaml", &out, testLogger()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"models      1 (visible 1)", "families    1", "hash        "} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lacks %q:\n%s", want, text)
		}
	}
	if info, err := os.Stat(snapshotPath); err != nil || info.Size() == 0 {
		t.Fatalf("snapshot missing: %v", err)
	}
}

// TestRunCheckStaleSnapshot 验证上游失败时 -check 使用快照并返回 stale 错误。
func TestRunCheckStaleSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	snapshotPath := filepath.Join(t.TempDir(), "models.json")
	snapshot := `{
  "schema_version": 1,
  "generated_at": "2026-10-26T00:00:00Z",
  "source": "https://example.com",
  "catalog_hash": "hash",
  "models": [{"id": "swe-2-high", "created": 1, "owned_by": "windsurf", "supports_images": false}],
  "families": {"swe-2": {"high": "swe-2-high"}}
}`
	if err := os.WriteFile(snapshotPath, []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	forceHTTP1 := true
	serviceConfig := config.Config{
		Devin: config.DevinConfig{
			BaseURL: server.URL, Token: "test-token", Model: "swe-2-high",
			ForceHTTP1: &forceHTTP1, IdleTimeout: 5 * time.Minute,
		},
		Catalog: config.CatalogConfig{Snapshot: snapshotPath},
	}
	var out bytes.Buffer
	err := runCheck(&serviceConfig, "config.yaml", &out, testLogger())
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v", err)
	}
	if text := out.String(); !strings.Contains(text, "stale-snapshot") || !strings.Contains(text, "models      1") {
		t.Fatalf("output = %s", text)
	}
}

// TestRunCheckRejectsEmptyToken 验证空 token 给出可操作的错误。
func TestRunCheckRejectsEmptyToken(t *testing.T) {
	var serviceConfig config.Config
	err := runCheck(&serviceConfig, "config.yaml", &bytes.Buffer{}, testLogger())
	if err == nil || !strings.Contains(err.Error(), "devin.token") {
		t.Fatalf("error = %v", err)
	}
}

// TestVersionString 验证版本输出非空。
func TestVersionString(t *testing.T) {
	if versionString() == "" {
		t.Fatal("version string is empty")
	}
}

// testLogger 返回丢弃输出的测试日志器。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
