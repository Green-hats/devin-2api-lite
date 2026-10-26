// 本文件验证 -check 自检输出与空 token 报错。
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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
	if err := runCheck(&serviceConfig, "config.yaml", &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"models      1", "vision 1", "found=true", "glm-5-2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lacks %q:\n%s", want, text)
		}
	}
}

// TestRunCheckRejectsEmptyToken 验证空 token 给出可操作的错误。
func TestRunCheckRejectsEmptyToken(t *testing.T) {
	var serviceConfig config.Config
	err := runCheck(&serviceConfig, "config.yaml", &bytes.Buffer{})
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
