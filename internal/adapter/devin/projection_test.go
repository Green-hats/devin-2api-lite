// 本文件验证 /v1/models 的 raw/openai/both 投影。
package devin

import (
	"testing"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
)

// TestProjectModelsModes 验证 openai 模式隐藏档位 id 并合成基础模型。
func TestProjectModelsModes(t *testing.T) {
	actual := []adapter.ModelInfo{
		{ID: "swe-2-medium", Family: "SWE-2", Effort: "medium", OwnedBy: "windsurf", SupportsImages: true, MaxTokens: 100, Created: 10},
		{ID: "swe-2-high", Family: "SWE-2", Effort: "high", OwnedBy: "windsurf", SupportsImages: true, MaxTokens: 200, Created: 11},
		{ID: "swe-2-medium-lightning", Family: "SWE-2", Effort: "medium", FastMode: true, OwnedBy: "windsurf"},
		{ID: "glm-5-2", OwnedBy: "zai"},
	}
	tiers := map[string]map[string]string{"swe-2": {"medium": "swe-2-medium", "high": "swe-2-high"}}

	visible := projectModels(actual, tiers, "openai")
	if len(visible) != 3 {
		t.Fatalf("openai visible = %d, want 3: %#v", len(visible), visible)
	}
	base := visible[0]
	if base.ID != "swe-2" || !base.RequiresReasoningEffort {
		t.Fatalf("base = %#v", base)
	}
	if len(base.ReasoningEfforts) != 2 || base.ReasoningEfforts[0] != "high" || base.ReasoningEfforts[1] != "medium" {
		t.Fatalf("efforts = %#v", base.ReasoningEfforts)
	}
	if !base.SupportsImages || base.MaxTokens != 200 || base.OwnedBy != "windsurf" || base.Label != "SWE-2" {
		t.Fatalf("base metadata = %#v", base)
	}
	if visible[1].ID != "swe-2-medium-lightning" || visible[2].ID != "glm-5-2" {
		t.Fatalf("visible = %#v", visible)
	}

	both := projectModels(actual, tiers, "both")
	if len(both) != 5 {
		t.Fatalf("both visible = %d, want 5", len(both))
	}
	raw := projectModels(actual, tiers, "raw")
	if len(raw) != 4 || raw[0].ID != "swe-2-medium" {
		t.Fatalf("raw = %#v", raw)
	}
}
