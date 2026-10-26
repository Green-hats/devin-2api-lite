// 本文件实现 -check 自检与 -version 版本输出。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
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
func newProviderAdapter(serviceConfig config.Config) (adapter.Adapter, error) {
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
	})
}

// runCheck 拉取一次模型目录，验证 token、上游地址与配置是否可用。
func runCheck(serviceConfig *config.Config, configPath string, out io.Writer) error {
	if serviceConfig.Devin.Token == "" {
		return errors.New("devin.token is empty; set it in config.yaml or DEVIN_TOKEN")
	}
	providerAdapter, err := newProviderAdapter(*serviceConfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := providerAdapter.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("upstream check failed: %w", err)
	}

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
	return nil
}
