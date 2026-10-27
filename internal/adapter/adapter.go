// 本文件定义供应商适配器与中间 LLM 模型之间的边界。
//
// Package adapter 定义供应商适配器与中间 LLM 模型之间的边界。
package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// ErrUnavailable 表示尚未配置可用的供应商适配器。
var ErrUnavailable = errors.New("provider adapter is not configured")

// ErrStreamIdle 表示上游流在约定时间内没有产生任何帧。
var ErrStreamIdle = errors.New("upstream stream idle timeout")

// CodedError 是带稳定错误码的上游错误，供 HTTP 层映射状态码。
type CodedError interface {
	error
	// ErrorCode 返回 Connect / 上游错误码，例如 unauthenticated。
	ErrorCode() string
}

// ModelInfo 是模型目录条目（OpenAI /v1/models 形状 + 目录元数据）。
type ModelInfo struct {
	// ID 是模型标识（OpenAI model id / Devin model_uid）。
	ID string `json:"id"`
	// Created 是目录条目的 Unix 秒时间戳；未知时可为 0。
	Created int64 `json:"created"`
	// OwnedBy 是模型归属方展示名。
	OwnedBy string `json:"owned_by"`
	// SupportsImages 表示该模型是否支持多模态图片输入；目录未知时为 false。
	SupportsImages bool `json:"supports_images"`
	// Label 是上游展示名，可能为空。
	Label string `json:"label,omitempty"`
	// Description 是上游模型描述，可能为空。
	Description string `json:"description,omitempty"`
	// MaxTokens 是上游声明的上下文上限；未知时为 0。
	MaxTokens int64 `json:"max_tokens,omitempty"`
	// Family 是上游 family 展示名，例如 "SWE-2"。
	Family string `json:"family,omitempty"`
	// Effort 是 family metadata 声明的推理档位（low/medium/high），可能为空。
	Effort string `json:"effort,omitempty"`
	// FastMode 表示该条目是 family 里的 fast 变体。
	FastMode bool `json:"fast_mode,omitempty"`
	// DefaultInFamily 表示上游将其标记为 family 默认模型。
	DefaultInFamily bool `json:"default_in_family,omitempty"`
	// ReasoningEfforts 是虚拟基础模型支持的推理档位；实际模型为空。
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	// RequiresReasoningEffort 表示调用该模型时必须显式传档位。
	RequiresReasoningEffort bool `json:"requires_reasoning_effort,omitempty"`
}

// Manifest 是自动生成模型目录的版本化契约。
type Manifest struct {
	// SchemaVersion 是 manifest 结构版本。
	SchemaVersion int `json:"schema_version"`
	// GeneratedAt 是目录生成时间。
	GeneratedAt time.Time `json:"generated_at"`
	// Source 是目录来源（上游 base URL）。
	Source string `json:"source"`
	// CatalogHash 是目录内容的稳定摘要。
	CatalogHash string `json:"catalog_hash"`
	// Stale 表示当前内容来自本地快照，上游刷新失败。
	Stale bool `json:"stale"`
	// Models 是完整模型条目。
	Models []ModelInfo `json:"models"`
	// Families 是根据 family metadata 推导出的档位建议；不会自动生效。
	Families map[string]map[string]string `json:"families,omitempty"`
}

// ManifestProvider 是可选能力：适配器可提供版本化模型目录。
type ManifestProvider interface {
	// ModelManifest 返回当前内存中的目录快照。
	ModelManifest() Manifest
}

// Quota 是一次上游账户额度快照；百分比为 nil 表示上游未提供该字段。
type Quota struct {
	// Plan 是账户套餐展示名，例如 Pro；未知时为空。
	Plan string `json:"plan,omitempty"`
	// DailyRemainingPercent 是当日额度剩余百分比（0-100）。
	DailyRemainingPercent *int64 `json:"daily_remaining_percent"`
	// WeeklyRemainingPercent 是本周额度剩余百分比（0-100）。
	WeeklyRemainingPercent *int64 `json:"weekly_remaining_percent"`
	// DailyResetAt 是当日额度重置的 Unix 秒时间戳；0 表示未知。
	DailyResetAt int64 `json:"daily_reset_at"`
	// WeeklyResetAt 是本周额度重置的 Unix 秒时间戳；0 表示未知。
	WeeklyResetAt int64 `json:"weekly_reset_at"`
	// SampledAt 是本地取得该快照的 Unix 秒时间戳。
	SampledAt int64 `json:"sampled_at"`
	// Stale 表示上游刷新失败，本次返回的是上一次成功快照。
	Stale bool `json:"stale"`
}

// QuotaProvider 是可选能力：适配器可提供账户额度快照。
type QuotaProvider interface {
	// Quota 返回当前账户额度；实现应缓存，避免每次调用都请求上游。
	Quota(context.Context) (Quota, error)
}

// Adapter 将供应商无关的请求上下文转换为具体供应商调用，并返回有序响应流。
type Adapter interface {
	// Stream 开始一次或多次助手响应的流式生成。
	Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error)
	// ListModels 返回当前账号可用的模型目录；失败时返回错误。
	ListModels(context.Context) ([]ModelInfo, error)
}
