// 本文件验证 /v1/quota 的 fresh 参数会绕过缓存。
package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/config"
)

// fakeRefreshingAdapter 记录缓存读取与强制刷新的调用次数。
type fakeRefreshingAdapter struct {
	fakeQuotaAdapter
	refreshed int
}

// RefreshQuota 记录一次强制刷新。
func (fake *fakeRefreshingAdapter) RefreshQuota(context.Context) (adapter.Quota, error) {
	fake.refreshed++
	return fake.quota, fake.err
}

// TestQuotaFreshBypassesCache 验证 fresh=1 走强制刷新，默认只读缓存。
func TestQuotaFreshBypassesCache(t *testing.T) {
	daily := int64(42)
	fake := &fakeRefreshingAdapter{fakeQuotaAdapter: fakeQuotaAdapter{quota: adapter.Quota{
		Plan:                  "Pro",
		DailyRemainingPercent: &daily,
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	router := application.Router()

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/quota", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if fake.refreshed != 0 {
		t.Fatalf("refreshed = %d without fresh=1, want 0", fake.refreshed)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/quota?fresh=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if fake.refreshed != 1 {
		t.Fatalf("refreshed = %d with fresh=1, want 1", fake.refreshed)
	}
}
