// 本文件实现账户额度查询：调用上游 GetUserStatus 并提取剩余额度。
package devin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
)

// GetUserStatusResponse 中本适配器读取的字段号，见 docs/upstream-protocol.md。
const (
	// userStatusField 是响应中嵌套 UserStatus 的字段号。
	userStatusField = 1
	// planStatusField 是 UserStatus 中嵌套 PlanStatus 的字段号。
	planStatusField = 13
	// planInfoField 是 PlanStatus 中套餐信息的字段号。
	planInfoField = 1
	// planLabelField 是套餐信息中展示名的字段号。
	planLabelField = 2
	// dailyPercentField 是当日额度剩余百分比。
	dailyPercentField = 14
	// weeklyPercentField 是本周额度剩余百分比。
	weeklyPercentField = 15
	// dailyResetField 是当日额度重置的 Unix 秒时间戳。
	dailyResetField = 17
	// weeklyResetField 是本周额度重置的 Unix 秒时间戳。
	weeklyResetField = 18
)

const (
	// procedureGetUserStatus 是 SeatManagement 的账户状态一元接口，响应携带额度。
	procedureGetUserStatus = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"
	// quotaRefresh 是额度缓存时长；上游响应约 130 KiB，不应每次请求都拉取。
	quotaRefresh = 5 * time.Minute
	// quotaRetry 是刷新失败后的重试间隔，避免故障期间每个请求都打上游。
	quotaRetry = 30 * time.Second
	// quotaTimeout 限制一次额度查询的总时长，慢响应不应长时间占用调用方。
	quotaTimeout = 20 * time.Second
)

// quotaRequest 对应 GetUserStatusRequest 中本适配器设置的字段。
type quotaRequest struct {
	metadata *metadata
}

// encode 序列化为 GetUserStatusRequest。
func (request *quotaRequest) encode() []byte {
	var encoded buffer
	encoded.message(1, request.metadata.encode)
	return encoded.data
}

// Quota 返回账户额度快照，带缓存；刷新失败时回退到上一次成功快照并标记 stale。
func (a *Adapter) Quota(ctx context.Context) (adapter.Quota, error) {
	a.quotaMu.Lock()
	defer a.quotaMu.Unlock()
	if a.quotaCached != nil && time.Now().Before(a.quotaExpiry) {
		cached := *a.quotaCached
		cached.Stale = a.quotaStale
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	sample, err := a.fetchQuota(ctx)
	if err != nil {
		if a.quotaCached != nil {
			stale := *a.quotaCached
			stale.Stale = true
			a.quotaStale = true
			a.quotaExpiry = time.Now().Add(quotaRetry)
			a.logger.Warn("using stale quota sample", "error", err)
			return stale, nil
		}
		return adapter.Quota{}, err
	}
	a.quotaCached = &sample
	a.quotaExpiry = time.Now().Add(quotaRefresh)
	a.quotaStale = false
	return sample, nil
}

// RefreshQuota 跳过缓存强制向上游取一次额度；供 GET /v1/quota?fresh=1 使用。
func (a *Adapter) RefreshQuota(ctx context.Context) (adapter.Quota, error) {
	a.quotaMu.Lock()
	a.quotaExpiry = time.Time{}
	a.quotaMu.Unlock()
	return a.Quota(ctx)
}

// fetchQuota 调用上游并解析一次额度快照。
func (a *Adapter) fetchQuota(ctx context.Context) (adapter.Quota, error) {
	payload, err := a.unaryClient.Unary(ctx, procedureGetUserStatus, (&quotaRequest{
		metadata: &metadata{
			ideName:          clientName,
			ideVersion:       clientVersion,
			extensionName:    clientName,
			extensionVersion: clientVersion,
			apiKey:           a.config.Token,
			locale:           "en",
			os:               "mac",
		},
	}).encode())
	if err != nil {
		return adapter.Quota{}, fmt.Errorf("call GetUserStatus: %w", err)
	}
	quota, err := decodeUserStatus(payload)
	if err != nil {
		return adapter.Quota{}, fmt.Errorf("decode Devin user status: %w", err)
	}
	quota.SampledAt = time.Now().Unix()
	return quota, nil
}

// decodeUserStatus 从 GetUserStatusResponse 中提取额度字段。
func decodeUserStatus(data []byte) (adapter.Quota, error) {
	userStatus, ok, err := nestedField(data, userStatusField)
	if err != nil {
		return adapter.Quota{}, err
	}
	if !ok {
		return adapter.Quota{}, errors.New("response carries no user_status")
	}
	planStatus, ok, err := nestedField(userStatus, planStatusField)
	if err != nil {
		return adapter.Quota{}, err
	}
	if !ok {
		return adapter.Quota{}, errors.New("user_status carries no plan_status")
	}

	quota := adapter.Quota{}
	reader := newReader(planStatus)
	for {
		field, ok := reader.next()
		if !ok {
			break
		}
		switch field {
		case planInfoField:
			raw, ok := reader.bytes()
			if !ok {
				continue
			}
			label, ok, err := nestedField(raw, planLabelField)
			if err != nil {
				return adapter.Quota{}, err
			}
			if ok {
				quota.Plan = string(label)
			}
		case dailyPercentField:
			if value, ok := reader.u64(); ok {
				quota.DailyRemainingPercent = percentValue(value)
			}
		case weeklyPercentField:
			if value, ok := reader.u64(); ok {
				quota.WeeklyRemainingPercent = percentValue(value)
			}
		case dailyResetField:
			if value, ok := reader.u64(); ok {
				quota.DailyResetAt = int64(value)
			}
		case weeklyResetField:
			if value, ok := reader.u64(); ok {
				quota.WeeklyResetAt = int64(value)
			}
		default:
			reader.skip()
		}
	}
	if err := reader.errOrNil(); err != nil {
		return adapter.Quota{}, err
	}
	return quota, nil
}

// nestedField 返回首个指定字段的嵌套消息；字段缺失时 ok 为 false。
func nestedField(data []byte, number int) ([]byte, bool, error) {
	reader := newReader(data)
	for {
		field, ok := reader.next()
		if !ok {
			break
		}
		if field == number {
			raw, ok := reader.bytes()
			if !ok {
				if err := reader.errOrNil(); err != nil {
					return nil, false, err
				}
				return nil, false, fmt.Errorf("protobuf field %d is not length-delimited", number)
			}
			return raw, true, nil
		}
		reader.skip()
	}
	return nil, false, reader.errOrNil()
}

// percentValue 把上游百分比转换为指针；超出 0-100 的哨兵值视为未提供。
func percentValue(value uint64) *int64 {
	if value > 100 {
		return nil
	}
	converted := int64(value)
	return &converted
}
