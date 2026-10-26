// 本文件实现模型目录快照、family 档位建议和 manifest 输出。
package devin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
)

const catalogSnapshotSchemaVersion = 1

// catalogSnapshot 是磁盘上的模型目录快照。
type catalogSnapshot struct {
	SchemaVersion int                          `json:"schema_version"`
	GeneratedAt   time.Time                    `json:"generated_at"`
	Source        string                       `json:"source"`
	CatalogHash   string                       `json:"catalog_hash"`
	Models        []adapter.ModelInfo          `json:"models"`
	Families      map[string]map[string]string `json:"families,omitempty"`
}

// suggestFamilies 根据 family metadata 推导「基础模型 -> 档位 -> uid」建议。
// Fast Mode 变体不参与，避免把 lightning 当成普通档位。
func suggestFamilies(models []adapter.ModelInfo) map[string]map[string]string {
	families := make(map[string]map[string]string)
	for _, model := range models {
		if model.Family == "" || model.Effort == "" || model.FastMode {
			continue
		}
		base := familySlug(model.Family)
		if base == "" {
			continue
		}
		tiers := families[base]
		if tiers == nil {
			tiers = make(map[string]string)
			families[base] = tiers
		}
		if _, exists := tiers[model.Effort]; !exists {
			tiers[model.Effort] = model.ID
		}
	}
	return families
}

// familySlug 把展示名变成稳定的基础模型名，例如 "GLM-5.3" -> "glm-5-3"。
func familySlug(label string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

// catalogHash 返回目录内容的稳定摘要，用于 manifest 与变更检测。
func catalogHash(models []adapter.ModelInfo, families map[string]map[string]string) string {
	stable := make([]adapter.ModelInfo, len(models))
	copy(stable, models)
	for index := range stable {
		stable[index].Created = 0
	}
	payload, err := json.Marshal(struct {
		Models   []adapter.ModelInfo          `json:"models"`
		Families map[string]map[string]string `json:"families"`
	}{Models: stable, Families: families})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// cloneFamilies 复制嵌套 map，避免调用方修改内部状态。
func cloneFamilies(families map[string]map[string]string) map[string]map[string]string {
	if len(families) == 0 {
		return nil
	}
	cloned := make(map[string]map[string]string, len(families))
	for base, tiers := range families {
		copied := make(map[string]string, len(tiers))
		for effort, target := range tiers {
			copied[effort] = target
		}
		cloned[base] = copied
	}
	return cloned
}

// persistSnapshot 原子写入目录快照；失败只记录日志，不影响请求。
func (a *Adapter) persistSnapshot(snapshot catalogSnapshot) {
	if a.snapshotPath == "" {
		return
	}
	if err := writeCatalogSnapshot(a.snapshotPath, snapshot); err != nil {
		a.logger.Warn("write model catalog snapshot failed", "path", a.snapshotPath, "error", err)
	}
}

// writeCatalogSnapshot 先写临时文件再 rename，避免读到半截 JSON。
func writeCatalogSnapshot(path string, snapshot catalogSnapshot) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(directory, ".models-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp snapshot: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp snapshot: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp snapshot: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp snapshot: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("close temp snapshot: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		// Windows 不允许 rename 覆盖已有文件；退化为先删除再 rename。
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tempName)
			return fmt.Errorf("replace snapshot: %w", err)
		}
		if renameErr := os.Rename(tempName, path); renameErr != nil {
			_ = os.Remove(tempName)
			return fmt.Errorf("replace snapshot: %w", renameErr)
		}
	}
	return nil
}

// loadCatalogSnapshot 读取并校验快照。
func loadCatalogSnapshot(path string) (*catalogSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snapshot catalogSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	if snapshot.SchemaVersion != catalogSnapshotSchemaVersion {
		return nil, fmt.Errorf("unsupported snapshot schema %d", snapshot.SchemaVersion)
	}
	if len(snapshot.Models) == 0 {
		return nil, fmt.Errorf("snapshot contains no models")
	}
	return &snapshot, nil
}

// loadSnapshotFallback 在上游失败时加载本地快照。
func (a *Adapter) loadSnapshotFallback(cause error) ([]adapter.ModelInfo, bool) {
	if a.snapshotPath == "" {
		return nil, false
	}
	snapshot, err := loadCatalogSnapshot(a.snapshotPath)
	if err != nil {
		a.logger.Warn("load model catalog snapshot failed", "path", a.snapshotPath, "error", err)
		return nil, false
	}
	a.recordImageSupport(snapshot.Models)
	a.modelsMu.Lock()
	a.models = snapshot.Models
	a.modelsExpiry = time.Now().Add(a.modelsCacheTTL)
	a.catalogGeneratedAt = snapshot.GeneratedAt
	a.catalogSource = snapshot.Source
	a.catalogHash = snapshot.CatalogHash
	a.catalogFamilies = snapshot.Families
	a.catalogStale = true
	a.modelsMu.Unlock()
	a.logger.Warn("using stale model catalog snapshot", "path", a.snapshotPath, "cause", cause)
	return cloneModelInfo(snapshot.Models), true
}

// ModelManifest 返回当前内存目录的版本化视图。
func (a *Adapter) ModelManifest() adapter.Manifest {
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	source := a.catalogSource
	if source == "" {
		source = a.config.BaseURL
	}
	return adapter.Manifest{
		SchemaVersion: catalogSnapshotSchemaVersion,
		GeneratedAt:   a.catalogGeneratedAt,
		Source:        source,
		CatalogHash:   a.catalogHash,
		Stale:         a.catalogStale,
		Models:        cloneModelInfo(a.models),
		Families:      cloneFamilies(a.catalogFamilies),
	}
}
