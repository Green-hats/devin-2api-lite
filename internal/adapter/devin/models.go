// 本文件实现模型目录、视觉能力缓存与图片输入校验。
package devin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// validateImagesForModel 在本地尽早拒绝「无视觉能力模型 + 图片」组合。
// 优先使用模型目录学到的 supports_images，未知时回退到内置名单。
func (adapter *Adapter) validateImagesForModel(request llm.RequestMessages, model string) error {
	if !requestHasImages(request) {
		return nil
	}
	if supported, known := adapter.cachedImageSupport(model); known {
		if supported {
			return nil
		}
		return imageUnsupportedError(model)
	}
	if !modelLikelySupportsImages(model) {
		return imageUnsupportedError(model)
	}
	return nil
}

func imageUnsupportedError(model string) error {
	return fmt.Errorf("model %q does not support image inputs (supports_images=false); use a vision-capable model or remove images", model)
}

// cachedImageSupport 返回模型目录中记录的图片能力。
func (adapter *Adapter) cachedImageSupport(model string) (bool, bool) {
	adapter.imagesMu.RLock()
	defer adapter.imagesMu.RUnlock()
	supported, ok := adapter.imageSupport[model]
	return supported, ok
}

func requestHasImages(request llm.RequestMessages) bool {
	for _, message := range request.Messages {
		var content []llm.Content
		switch m := message.(type) {
		case llm.UserMessage:
			content = m.Content
		case llm.ToolResultMessage:
			content = m.Content
		default:
			continue
		}
		for _, block := range content {
			if _, ok := block.(llm.ImageContent); ok {
				return true
			}
		}
	}
	return false
}

// modelLikelySupportsImages 用已知无视觉模型名单；不确定时放行让上游裁决。
func modelLikelySupportsImages(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	// 与 GetCascadeModelConfigs.supports_images=false 的常见 uid 对齐。
	noVisionPrefixes := []string{
		"glm-5-2", "glm-5", "glm-4.7", "glm-4-7", "glm-4",
		"deepseek", "kimi-k2", "qwen3-coder",
	}
	for _, p := range noVisionPrefixes {
		if m == p || strings.HasPrefix(m, p+"-") || strings.HasPrefix(m, p+"_") {
			return false
		}
	}
	if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3-mini") || strings.HasPrefix(m, "o4-mini") {
		return false
	}
	return true
}

// resolveReasoningModel 在配置了档位表时把基础模型改写成实际 uid。
// 未配置的模型保持原样；基础模型缺少 effort 或档位不存在时返回 400 类错误。
func resolveReasoningModel(model, effort string, tiers map[string]map[string]string) (string, error) {
	model = strings.TrimSpace(model)
	effort = strings.ToLower(strings.TrimSpace(effort))
	if len(tiers) == 0 {
		return model, nil
	}
	byEffort, ok := tiers[model]
	if !ok {
		return model, nil
	}
	supported := supportedEfforts(byEffort)
	if effort == "" {
		return "", fmt.Errorf("invalid_argument: model %q requires reasoning_effort (supported: %s)", model, supported)
	}
	target, ok := byEffort[effort]
	if !ok {
		return "", fmt.Errorf("invalid_argument: model %q does not support reasoning_effort %q (supported: %s)", model, effort, supported)
	}
	return target, nil
}

// supportedEfforts 返回排序后的档位列表，保证错误信息稳定。
func supportedEfforts(tiers map[string]string) string {
	efforts := make([]string, 0, len(tiers))
	for effort := range tiers {
		efforts = append(efforts, effort)
	}
	sort.Strings(efforts)
	return strings.Join(efforts, ", ")
}

// ListModels 通过 GetCascadeModelConfigs 拉取可用模型目录，结果带 TTL 缓存。
// 上游失败时回退到本地快照，并在 manifest 中标记 stale。
func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	if cached, ok := a.cachedModels(); ok {
		return cached, nil
	}
	models, err := a.fetchCatalog(ctx)
	if err != nil {
		if fallback, ok := a.loadSnapshotFallback(err); ok {
			return a.visibleModels(fallback), nil
		}
		return nil, err
	}
	families := suggestFamilies(models)
	generatedAt := time.Now().UTC()
	hash := catalogHash(models, families)
	return a.applyCatalog(models, families, generatedAt, hash), nil
}

// cachedModels 返回 TTL 内缓存的可见模型。
func (a *Adapter) cachedModels() ([]adapter.ModelInfo, bool) {
	a.modelsMu.RLock()
	ready := a.models != nil && time.Now().Before(a.modelsExpiry)
	if ready && a.config.AutoTiers && len(a.catalogFamilies) == 0 {
		ready = false
	}
	if !ready {
		a.modelsMu.RUnlock()
		return nil, false
	}
	cached := cloneModelInfo(a.models)
	a.modelsMu.RUnlock()
	return a.visibleModels(cached), true
}

// fetchCatalog 调用上游并把响应转换为模型条目。
func (a *Adapter) fetchCatalog(ctx context.Context) ([]adapter.ModelInfo, error) {
	payload, err := a.unaryClient.Unary(ctx, procedureGetCascadeModelConfigs, (&modelConfigsRequest{
		metadata: &metadata{
			ideName:          clientName,
			ideVersion:       clientVersion,
			extensionName:    clientName,
			extensionVersion: clientVersion,
			apiKey:           a.config.Token,
			locale:           "en",
			os:               "win",
		},
	}).encode())
	if err != nil {
		return nil, fmt.Errorf("call GetCascadeModelConfigs: %w", err)
	}
	response, err := decodeModelConfigs(payload)
	if err != nil {
		return nil, fmt.Errorf("decode Devin model configs: %w", err)
	}

	now := time.Now().Unix()
	models := make([]adapter.ModelInfo, 0, len(response.configs))
	seen := make(map[string]struct{}, len(response.configs))
	for _, c := range response.configs {
		uid := c.modelUID
		if uid == "" || c.disabled {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		family := ""
		if c.family != nil {
			family = c.family.label
		}
		defaultInFamily := c.defaultInFamily
		if !defaultInFamily && c.family != nil {
			defaultInFamily = c.family.defaultInFamily
		}
		models = append(models, adapter.ModelInfo{
			ID:              uid,
			Created:         now,
			OwnedBy:         providerName(c.provider),
			SupportsImages:  c.supportsImages,
			Label:           c.label,
			Description:     c.description,
			MaxTokens:       c.maxTokens,
			Family:          family,
			Effort:          c.effort(),
			FastMode:        c.fastMode(),
			DefaultInFamily: defaultInFamily,
		})
	}
	// 用户显式配置的 model（如 gpt5.6）即使不在 Devin 返回的列表中，也应可被发现和调用。
	if configured := strings.TrimSpace(a.config.Model); configured != "" {
		if _, ok := seen[configured]; !ok {
			models = append(models, adapter.ModelInfo{
				ID: configured, Created: now, OwnedBy: "devin",
				// 与 Stream 的本地校验使用同一判断，避免 /v1/models 与真实行为矛盾。
				SupportsImages: modelLikelySupportsImages(configured),
			})
		}
	}
	return models, nil
}

// applyCatalog 更新缓存和图片能力，必要时发布快照，并返回可见投影。
func (a *Adapter) applyCatalog(models []adapter.ModelInfo, families map[string]map[string]string, generatedAt time.Time, hash string) []adapter.ModelInfo {
	a.recordImageSupport(models)

	a.modelsMu.Lock()
	// 请求期间可能有其他请求已写入缓存，避免覆盖更热的数据。
	if a.models != nil && time.Now().Before(a.modelsExpiry) {
		cached := cloneModelInfo(a.models)
		a.modelsMu.Unlock()
		return a.visibleModels(cached)
	}
	previousHash := a.catalogHash
	a.models = models
	a.modelsExpiry = time.Now().Add(a.modelsCacheTTL)
	a.catalogGeneratedAt = generatedAt
	a.catalogSource = a.config.BaseURL
	a.catalogHash = hash
	a.catalogFamilies = families
	a.catalogStale = false
	a.modelsMu.Unlock()

	if previousHash != hash {
		a.publishSnapshot(models, families, generatedAt, hash)
	}
	return a.visibleModels(models)
}

// publishSnapshot 把当前目录写入本地快照；失败只记录日志。
func (a *Adapter) publishSnapshot(models []adapter.ModelInfo, families map[string]map[string]string, generatedAt time.Time, hash string) {
	a.persistSnapshot(catalogSnapshot{
		SchemaVersion: catalogSnapshotSchemaVersion,
		GeneratedAt:   generatedAt,
		Source:        a.config.BaseURL,
		CatalogHash:   hash,
		Models:        models,
		Families:      families,
	})
}

// recordImageSupport 把模型目录中的图片能力写入本地缓存。
func (a *Adapter) recordImageSupport(models []adapter.ModelInfo) {
	a.imagesMu.Lock()
	defer a.imagesMu.Unlock()
	for _, model := range models {
		a.imageSupport[model.ID] = model.SupportsImages
	}
}

// cloneModelInfo 返回缓存切片副本，避免调用方修改内部状态。
func cloneModelInfo(models []adapter.ModelInfo) []adapter.ModelInfo {
	cloned := make([]adapter.ModelInfo, len(models))
	copy(cloned, models)
	return cloned
}

// providerName 把 ModelProvider 枚举值映射为展示用归属方。
func providerName(provider int) string {
	switch provider {
	case 1:
		return "windsurf"
	case 2:
		return "openai"
	case 3:
		return "anthropic"
	case 4:
		return "google"
	case 5:
		return "xai"
	case 6:
		return "deepseek"
	case 7:
		return "moonshot"
	case 8:
		return "qwen"
	case 9:
		return "zai"
	case 10:
		return "minimax"
	case 11:
		return "nvidia"
	case 12:
		return "thinking-machines"
	default:
		return "devin"
	}
}
