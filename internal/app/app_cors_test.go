// 本文件验证只读端点的跨源读取策略。
package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Green-hats/devin-2api-lite/internal/config"
)

// TestReadOnlyEndpointsAllowCrossOrigin 验证只读 GET 端点开放跨源读取，
// 写入端点不开放。
func TestReadOnlyEndpointsAllowCrossOrigin(t *testing.T) {
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	router := application.Router()

	for _, path := range []string{"/healthz", "/v1/models", "/v1/quota"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Origin", "http://127.0.0.1:62045")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("%s: Access-Control-Allow-Origin = %q, want *", path, got)
		}
	}

	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("Origin", "http://127.0.0.1:62045")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s: Access-Control-Allow-Origin = %q, want empty", path, got)
		}
	}
}
