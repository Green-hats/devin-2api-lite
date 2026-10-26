// 本文件验证代理 Transport 的协议校验与 HTTP/1.1 强制策略。
package httpproxy

import (
	"net/http"
	"testing"
)

// TestNewTransportWithoutProxyForcesHTTP1 的测试动机是锁定默认的并发规避策略。
func TestNewTransportWithoutProxyForcesHTTP1(t *testing.T) {
	transport, err := NewTransport("", true)
	if err != nil {
		t.Fatal(err)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("HTTP/2 should be disabled")
	}
	if transport.TLSClientConfig == nil || len(transport.TLSClientConfig.NextProtos) != 1 || transport.TLSClientConfig.NextProtos[0] != "http/1.1" {
		t.Fatalf("ALPN = %#v", transport.TLSClientConfig)
	}
	if transport.MaxIdleConnsPerHost != 200 {
		t.Fatalf("MaxIdleConnsPerHost = %d", transport.MaxIdleConnsPerHost)
	}
}

// TestNewTransportAcceptsHTTPProxy 的测试动机是覆盖 HTTP/HTTPS 代理配置。
func TestNewTransportAcceptsHTTPProxy(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:7890", "https://proxy.example.com:8443"} {
		transport, err := NewTransport(raw, false)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if transport.Proxy == nil {
			t.Fatalf("%s: proxy is nil", raw)
		}
		request, err := http.NewRequest(http.MethodGet, "https://server.codeium.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.Proxy(request); err != nil {
			t.Fatalf("%s: resolve proxy: %v", raw, err)
		}
	}
}

// TestNewTransportRejectsUnsupportedScheme 的测试动机是给出可操作的错误信息。
func TestNewTransportRejectsUnsupportedScheme(t *testing.T) {
	if _, err := NewTransport("socks5://127.0.0.1:1080", false); err == nil {
		t.Fatal("socks5 should be rejected")
	}
}
