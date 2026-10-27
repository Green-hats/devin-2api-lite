// 本文件验证 /v1/quota 路由的鉴权、编码与错误映射。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/config"
)

// fakeQuotaAdapter 在 fakeAdapter 之上提供额度能力。
type fakeQuotaAdapter struct {
	fakeAdapter
	quota adapter.Quota
	err   error
}

// Quota 返回测试预置的额度快照。
func (fake *fakeQuotaAdapter) Quota(context.Context) (adapter.Quota, error) {
	return fake.quota, fake.err
}

// TestQuotaEndpointReturnsSnapshot 验证额度快照按 JSON 返回并带 stale 头。
func TestQuotaEndpointReturnsSnapshot(t *testing.T) {
	daily, weekly := int64(100), int64(98)
	fake := &fakeQuotaAdapter{quota: adapter.Quota{
		Plan:                   "Pro",
		DailyRemainingPercent:  &daily,
		WeeklyRemainingPercent: &weekly,
		DailyResetAt:           1793088000,
		WeeklyResetAt:          1793520000,
		SampledAt:              1793070000,
		Stale:                  true,
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Quota-Stale"); got != "true" {
		t.Fatalf("X-Quota-Stale = %q", got)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	var parsed map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["plan"] != "Pro" {
		t.Fatalf("plan = %v", parsed["plan"])
	}
	if parsed["daily_remaining_percent"] != float64(100) || parsed["weekly_remaining_percent"] != float64(98) {
		t.Fatalf("percentages = %v/%v", parsed["daily_remaining_percent"], parsed["weekly_remaining_percent"])
	}
	if parsed["weekly_reset_at"] != float64(1793520000) || parsed["stale"] != true {
		t.Fatalf("payload = %#v", parsed)
	}
}

// TestQuotaEndpointRequiresAPIKey 验证额度接口受统一的 API Key 保护。
func TestQuotaEndpointRequiresAPIKey(t *testing.T) {
	fake := &fakeQuotaAdapter{}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret")

	request := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestQuotaEndpointWithoutCapability 验证适配器不支持额度时返回 503。
func TestQuotaEndpointWithoutCapability(t *testing.T) {
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
}

// TestQuotaEndpointMapsProviderError 验证上游错误沿用统一的状态码映射。
func TestQuotaEndpointMapsProviderError(t *testing.T) {
	fake := &fakeQuotaAdapter{err: fmt.Errorf("call GetUserStatus: %w", adapter.ErrUnavailable)}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
}
