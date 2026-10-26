// 本文件定义环境变量对 YAML 配置的覆盖规则。
package config

import (
	"fmt"
	"os"
	"strconv"
)

// applyEnvironment 用环境变量覆盖 YAML 配置；未设置的环境变量保持原值。
// 这样容器和 systemd 场景可以不在磁盘上保存 token。
func applyEnvironment(config *Config) error {
	stringEnv("SERVER_LISTEN", &config.Server.Listen)
	if err := intEnv("SERVER_MAX_CONCURRENCY", &config.Server.MaxConcurrency); err != nil {
		return err
	}
	stringEnv("DEVIN_BASE_URL", &config.Devin.BaseURL)
	stringEnv("DEVIN_TOKEN", &config.Devin.Token)
	stringEnv("DEVIN_MODEL", &config.Devin.Model)
	stringEnv("DEVIN_PROXY", &config.Devin.Proxy)
	stringEnv("DEVIN_STREAM_IDLE_TIMEOUT", &config.Devin.StreamIdleTimeout)
	if err := boolEnv("DEVIN_FORCE_HTTP1", &config.Devin.ForceHTTP1); err != nil {
		return err
	}
	if err := boolValueEnv("DEBUG_ENABLED", &config.Debug.Enabled); err != nil {
		return err
	}
	stringEnv("CATALOG_SNAPSHOT", &config.Catalog.Snapshot)
	stringEnv("CATALOG_REFRESH", &config.Catalog.Refresh)
	stringEnv("AUTH_API_KEY", &config.Auth.APIKey)
	return nil
}

// stringEnv 在环境变量存在时覆盖字符串配置。
func stringEnv(name string, target *string) {
	if value, ok := os.LookupEnv(name); ok {
		*target = value
	}
}

// intEnv 在环境变量存在时覆盖整数配置。
func intEnv(name string, target *int) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", name, value, err)
	}
	*target = parsed
	return nil
}

// boolEnv 在环境变量存在时覆盖指针布尔配置。
func boolEnv(name string, target **bool) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", name, value, err)
	}
	*target = &parsed
	return nil
}

// boolValueEnv 在环境变量存在时覆盖普通布尔配置。
func boolValueEnv(name string, target *bool) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", name, value, err)
	}
	*target = parsed
	return nil
}
