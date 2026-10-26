// 本文件实现 -check 自检与 -version 版本输出。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/adapter/devin"
	"github.com/Green-hats/devin-2api-lite/internal/config"
)

// version 是未注入构建信息时的默认版本。
var version = "dev"

// versionString 返回可读版本号；优先使用模块版本与 Git 提交。
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	revision := ""
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
			revision = setting.Value[:7]
		}
	}
	if revision == "" {
		return version
	}
	return version + " (" + revision + ")"
}

// newProviderAdapter 按配置创建适配器；token 为空时返回可识别为 503 的占位实现。
func newProviderAdapter(serviceConfig config.Config, snapshotPath string, logger *slog.Logger) (adapter.Adapter, error) {
	if serviceConfig.Devin.Token == "" {
		return adapter.Unavailable{Reason: "devin.token is not configured"}, nil
	}
	return devin.New(devin.Config{
		BaseURL:        serviceConfig.Devin.BaseURL,
		Token:          serviceConfig.Devin.Token,
		Model:          serviceConfig.Devin.Model,
		Proxy:          serviceConfig.Devin.Proxy,
		ForceHTTP1:     serviceConfig.Devin.ForceHTTP1 != nil && *serviceConfig.Devin.ForceHTTP1,
		IdleTimeout:    serviceConfig.Devin.IdleTimeout,
		ReasoningTiers: serviceConfig.Devin.ReasoningTiers,
		SnapshotPath:   snapshotPath,
		CatalogRefresh: serviceConfig.Catalog.RefreshInterval,
		Logger:         logger,
	})
}

// resolveCatalogSnapshotPath 把相对快照路径解析到 config.yaml 所在目录。
func resolveCatalogSnapshotPath(configPath, snapshot string) string {
	if snapshot == "" {
		return ""
	}
	if filepath.IsAbs(snapshot) {
		return snapshot
	}
	return filepath.Join(filepath.Dir(configPath), snapshot)
}

// runCheck 拉取一次模型目录，验证 token、上游地址与配置是否可用。
func runCheck(serviceConfig *config.Config, configPath string, out io.Writer, logger *slog.Logger) error {
	if serviceConfig.Devin.Token == "" {
		return errors.New("devin.token is empty; set it in config.yaml or DEVIN_TOKEN")
	}
	snapshotPath := resolveCatalogSnapshotPath(configPath, serviceConfig.Catalog.Snapshot)
	providerAdapter, err := newProviderAdapter(*serviceConfig, snapshotPath, logger)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := providerAdapter.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("upstream check failed: %w", err)
	}

	stale := false
	vision := 0
	configuredFound := false
	for _, model := range models {
		if model.SupportsImages {
			vision++
		}
		if model.ID == serviceConfig.Devin.Model {
			configuredFound = true
		}
	}
	fmt.Fprintf(out, "config      %s\n", configPath)
	fmt.Fprintf(out, "upstream    %s\n", serviceConfig.Devin.BaseURL)
	fmt.Fprintf(out, "token       set\n")
	fmt.Fprintf(out, "model       %s\n", serviceConfig.Devin.Model)
	if manifestProvider, ok := providerAdapter.(adapter.ManifestProvider); ok {
		manifest := manifestProvider.ModelManifest()
		state := "live"
		if manifest.Stale {
			state = "stale-snapshot"
			stale = true
		}
		fmt.Fprintf(out, "catalog     %s (%d families, generated %s)\n", state, len(manifest.Families), manifest.GeneratedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "models      %d (vision %d)\n", len(models), vision)
	fmt.Fprintf(out, "configured  %s (found=%v)\n", serviceConfig.Devin.Model, configuredFound)
	const preview = 12
	for index, model := range models {
		if index >= preview {
			fmt.Fprintf(out, "  ... %d more\n", len(models)-preview)
			break
		}
		fmt.Fprintf(out, "  %-28s vision=%v\n", model.ID, model.SupportsImages)
	}
	if stale {
		return errors.New("upstream unavailable; using stale model catalog snapshot")
	}
	return nil
}

// runSyncModels 拉取一次上游目录并通过 ListModels 原子写入快照。
func runSyncModels(serviceConfig *config.Config, configPath string, out io.Writer, logger *slog.Logger) error {
	if serviceConfig.Devin.Token == "" {
		return errors.New("devin.token is empty; set it in config.yaml or DEVIN_TOKEN")
	}
	if serviceConfig.Catalog.Snapshot == "" {
		return errors.New("catalog.snapshot is empty; set it in config.yaml or CATALOG_SNAPSHOT")
	}
	snapshotPath := resolveCatalogSnapshotPath(configPath, serviceConfig.Catalog.Snapshot)
	providerAdapter, err := newProviderAdapter(*serviceConfig, snapshotPath, logger)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	models, err := providerAdapter.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("sync models failed: %w", err)
	}
	manifestProvider, ok := providerAdapter.(adapter.ManifestProvider)
	if !ok {
		return errors.New("adapter does not expose a model manifest")
	}
	manifest := manifestProvider.ModelManifest()
	if manifest.Stale {
		return errors.New("upstream unavailable; stale snapshot was used and was not refreshed")
	}
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if manifest.CatalogHash == "" || !strings.Contains(string(data), manifest.CatalogHash) {
		return errors.New("snapshot does not match the refreshed catalog hash")
	}
	fmt.Fprintf(out, "snapshot    %s\n", snapshotPath)
	fmt.Fprintf(out, "source      %s\n", manifest.Source)
	fmt.Fprintf(out, "models      %d\n", len(models))
	fmt.Fprintf(out, "families    %d\n", len(manifest.Families))
	fmt.Fprintf(out, "hash        %s\n", manifest.CatalogHash)
	return nil
}
