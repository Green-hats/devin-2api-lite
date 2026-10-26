// 本文件实现 /v1/models 的展示投影和自动档位解析。
package devin

import (
	"context"
	"sort"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
)

// effectiveTiers 返回显式配置 + 自动 family 建议的合并结果，显式配置优先。
func (a *Adapter) effectiveTiers() map[string]map[string]string {
	tiers := cloneTiers(a.config.ReasoningTiers)
	if !a.config.AutoTiers {
		return tiers
	}
	a.modelsMu.RLock()
	families := cloneFamilies(a.catalogFamilies)
	a.modelsMu.RUnlock()
	return mergeTiers(tiers, families)
}

// resolveReasoning 在自动档位开启但目录尚未加载时，先拉一次目录再解析。
func (a *Adapter) resolveReasoning(ctx context.Context, model, effort string) (string, error) {
	tiers := a.effectiveTiers()
	if a.config.AutoTiers && a.familyCount() == 0 {
		// 失败时继续用显式配置；上游错误会在真正调用时体现。
		_, _ = a.ListModels(ctx)
		tiers = a.effectiveTiers()
	}
	return resolveReasoningModel(model, effort, tiers)
}

func (a *Adapter) familyCount() int {
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	return len(a.catalogFamilies)
}

// visibleModels 返回当前配置下对外的模型投影。
func (a *Adapter) visibleModels(models []adapter.ModelInfo) []adapter.ModelInfo {
	return projectModels(models, a.effectiveTiers(), a.config.ModelMode)
}

// projectModels 按 mode 把实际目录投影成对外可见模型。
// raw: 原样；openai: 基础模型 + 非档位模型；both: 两者都保留。
func projectModels(actual []adapter.ModelInfo, tiers map[string]map[string]string, mode string) []adapter.ModelInfo {
	if mode == "" || mode == "raw" || len(tiers) == 0 {
		return cloneModelInfo(actual)
	}
	byID := make(map[string]adapter.ModelInfo, len(actual))
	for _, model := range actual {
		byID[model.ID] = model
	}

	bases := make([]adapter.ModelInfo, 0, len(tiers))
	hidden := make(map[string]struct{})
	for base, efforts := range tiers {
		if len(efforts) == 0 {
			continue
		}
		if _, exists := byID[base]; exists {
			// 基础 id 本身是真实模型，不再合成，也不藏真实条目。
			continue
		}
		targets := make([]adapter.ModelInfo, 0, len(efforts))
		available := make(map[string]string, len(efforts))
		for effort, target := range efforts {
			if model, ok := byID[target]; ok {
				targets = append(targets, model)
				available[effort] = target
			}
		}
		if len(targets) == 0 {
			continue
		}
		if mode == "openai" {
			for _, target := range targets {
				hidden[target.ID] = struct{}{}
			}
		}
		bases = append(bases, baseModel(base, targets, available))
	}
	sort.Slice(bases, func(left, right int) bool { return bases[left].ID < bases[right].ID })

	visible := make([]adapter.ModelInfo, 0, len(bases)+len(actual))
	visible = append(visible, bases...)
	for _, model := range actual {
		if _, ok := hidden[model.ID]; ok {
			continue
		}
		visible = append(visible, model)
	}
	return visible
}

// baseModel 根据映射目标合成一个虚拟基础模型。
func baseModel(id string, targets []adapter.ModelInfo, efforts map[string]string) adapter.ModelInfo {
	base := adapter.ModelInfo{
		ID:                      id,
		RequiresReasoningEffort: true,
		ReasoningEfforts:        sortedTiers(efforts),
	}
	for index, target := range targets {
		if index == 0 || (target.Created > 0 && (base.Created == 0 || target.Created < base.Created)) {
			base.Created = target.Created
		}
		if target.SupportsImages {
			base.SupportsImages = true
		}
		if target.MaxTokens > base.MaxTokens {
			base.MaxTokens = target.MaxTokens
		}
		if base.Label == "" {
			base.Label = target.Family
		}
		if base.OwnedBy == "" {
			base.OwnedBy = target.OwnedBy
		} else if base.OwnedBy != target.OwnedBy {
			base.OwnedBy = "devin"
		}
	}
	if base.Label == "" {
		base.Label = id
	}
	if base.OwnedBy == "" {
		base.OwnedBy = "devin"
	}
	return base
}

// sortedTiers 返回排序后的档位键。
func sortedTiers(tiers map[string]string) []string {
	values := make([]string, 0, len(tiers))
	for effort := range tiers {
		values = append(values, effort)
	}
	sort.Strings(values)
	return values
}

// cloneTiers 复制档位映射。
func cloneTiers(tiers map[string]map[string]string) map[string]map[string]string {
	return cloneFamilies(tiers)
}

// mergeTiers 合并自动建议与显式配置；显式配置优先。
func mergeTiers(explicit, automatic map[string]map[string]string) map[string]map[string]string {
	merged := cloneFamilies(explicit)
	if merged == nil {
		merged = make(map[string]map[string]string)
	}
	for base, efforts := range automatic {
		if _, exists := merged[base]; exists {
			continue
		}
		merged[base] = efforts
	}
	return merged
}
