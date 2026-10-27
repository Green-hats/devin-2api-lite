// 本文件验证账户额度解析、缓存与上游失败回退。
package devin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/connect"
)

// quotaFixture 构造一份与上游同形的 GetUserStatusResponse。
func quotaFixture() []byte {
	var plan buffer
	plan.message(planInfoField, func(info *buffer) {
		info.str(planLabelField, "Pro")
	})
	explicitUint(&plan, dailyPercentField, 100)
	explicitUint(&plan, weeklyPercentField, 98)
	explicitUint(&plan, dailyResetField, 1793088000)
	explicitUint(&plan, weeklyResetField, 1793520000)
	return wrapUserStatus(plan.data)
}

// wrapUserStatus 把 PlanStatus 负载包进 user_status 与响应外壳。
func wrapUserStatus(plan []byte) []byte {
	var user buffer
	user.message(planStatusField, func(nested *buffer) {
		nested.data = append(nested.data, plan...)
	})
	var response buffer
	response.message(userStatusField, func(nested *buffer) {
		nested.data = append(nested.data, user.data...)
	})
	return response.data
}

// explicitUint 写入显式整数字段；buffer.uint 会省略 0 值，测试需要保留。
func explicitUint(target *buffer, field int, value uint64) {
	target.tag(field, wireVarint)
	target.varint(value)
}

// TestDecodeUserStatus 验证套餐名与额度百分比、重置时间都被提取。
func TestDecodeUserStatus(t *testing.T) {
	quota, err := decodeUserStatus(quotaFixture())
	if err != nil {
		t.Fatal(err)
	}
	if quota.Plan != "Pro" {
		t.Fatalf("plan = %q, want Pro", quota.Plan)
	}
	if quota.DailyRemainingPercent == nil || *quota.DailyRemainingPercent != 100 {
		t.Fatalf("daily = %v", quota.DailyRemainingPercent)
	}
	if quota.WeeklyRemainingPercent == nil || *quota.WeeklyRemainingPercent != 98 {
		t.Fatalf("weekly = %v", quota.WeeklyRemainingPercent)
	}
	if quota.DailyResetAt != 1793088000 || quota.WeeklyResetAt != 1793520000 {
		t.Fatalf("resets = %d/%d", quota.DailyResetAt, quota.WeeklyResetAt)
	}
}

// TestDecodeUserStatusKeepsZeroAndDropsSentinel 验证 0% 是有效值，哨兵值视为未提供。
func TestDecodeUserStatusKeepsZeroAndDropsSentinel(t *testing.T) {
	var plan buffer
	explicitUint(&plan, dailyPercentField, 0)
	explicitUint(&plan, weeklyPercentField, 101)

	quota, err := decodeUserStatus(wrapUserStatus(plan.data))
	if err != nil {
		t.Fatal(err)
	}
	if quota.DailyRemainingPercent == nil || *quota.DailyRemainingPercent != 0 {
		t.Fatalf("daily = %v, want 0", quota.DailyRemainingPercent)
	}
	if quota.WeeklyRemainingPercent != nil {
		t.Fatalf("weekly = %v, want nil", quota.WeeklyRemainingPercent)
	}
}

// TestDecodeUserStatusRejectsMissingPlan 验证缺少 plan_status 时返回错误。
func TestDecodeUserStatusRejectsMissingPlan(t *testing.T) {
	var user buffer
	user.str(3, "someone")
	var response buffer
	response.message(userStatusField, func(nested *buffer) {
		nested.data = append(nested.data, user.data...)
	})
	if _, err := decodeUserStatus(response.data); err == nil {
		t.Fatal("expected an error for a response without plan_status")
	}
	if _, err := decodeUserStatus(nil); err == nil {
		t.Fatal("expected an error for an empty response")
	}
}

// TestQuotaCachesAndFallsBackToStale 验证缓存命中不重复请求，失败时回退上一次快照。
func TestQuotaCachesAndFallsBackToStale(t *testing.T) {
	fixture := quotaFixture()
	var calls int32
	var failing atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&calls, 1)
		if request.URL.Path != procedureGetUserStatus {
			t.Errorf("path = %q, want %q", request.URL.Path, procedureGetUserStatus)
		}
		if failing.Load() {
			http.Error(writer, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write(fixture)
	}))
	defer upstream.Close()

	provider := &Adapter{
		config:      Config{BaseURL: upstream.URL, Token: "test", Model: "swe-2"},
		unaryClient: connect.NewClient(upstream.URL, upstream.Client()),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()

	first, err := provider.Quota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stale || first.SampledAt == 0 {
		t.Fatalf("first sample = %#v", first)
	}
	if _, err := provider.Quota(ctx); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (second call must hit the cache)", got)
	}

	failing.Store(true)
	provider.quotaExpiry = time.Now().Add(-time.Second)
	stale, err := provider.Quota(ctx)
	if err != nil {
		t.Fatalf("stale fallback returned an error: %v", err)
	}
	if !stale.Stale {
		t.Fatalf("fallback sample is not marked stale: %#v", stale)
	}
	if stale.DailyRemainingPercent == nil || *stale.DailyRemainingPercent != 100 {
		t.Fatalf("fallback lost the last sample: %#v", stale)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}

	// 重试窗口内继续返回 stale 快照，不再打上游。
	again, err := provider.Quota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Stale {
		t.Fatalf("cached sample lost the stale mark: %#v", again)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 inside the retry window", got)
	}

	// 上游恢复后，下一次刷新应清除 stale。
	failing.Store(false)
	provider.quotaExpiry = time.Now().Add(-time.Second)
	recovered, err := provider.Quota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Stale {
		t.Fatalf("recovered sample is still stale: %#v", recovered)
	}
}

// TestQuotaSurfacesUpstreamErrorWithoutSample 验证没有历史快照时错误向上传递。
func TestQuotaSurfacesUpstreamErrorWithoutSample(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"code":"unauthenticated","message":"bad token"}`))
	}))
	defer upstream.Close()

	provider := &Adapter{
		config:      Config{BaseURL: upstream.URL, Token: "test", Model: "swe-2"},
		unaryClient: connect.NewClient(upstream.URL, upstream.Client()),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := provider.Quota(context.Background()); err == nil {
		t.Fatal("expected the upstream error to surface")
	}
}
