// 本文件定义服务启动配置及其 YAML 加载和校验逻辑。
//
// Package config 负责加载和校验服务启动配置。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// defaultListen 是未配置 server.listen 时使用的监听地址。
const defaultListen = ":18089"

// Config 保存服务启动所需的全部配置；服务运行期间不会热更新。
type Config struct {
	// Server 保存 HTTP 服务配置。
	Server ServerConfig `yaml:"server"`
	// Devin 保存 Devin Connect 上游配置。
	Devin DevinConfig `yaml:"devin"`
	// Debug 保存仅用于本地诊断的日志配置。
	Debug DebugConfig `yaml:"debug"`
	// Auth 保存对外 OpenAI 兼容接口的访问控制配置。
	Auth AuthConfig `yaml:"auth"`
}

// ServerConfig 保存 HTTP 服务监听配置。
type ServerConfig struct {
	// Listen 是 HTTP 服务监听地址。
	Listen string `yaml:"listen"`
	// MaxConcurrency 是同时处理的 /v1/* 请求数上限；0 表示使用默认值。
	MaxConcurrency int `yaml:"max_concurrency"`
}

// DevinConfig 保存 Devin Connect 上游调用配置。
type DevinConfig struct {
	// BaseURL 是 Devin Connect 服务的基础地址。
	BaseURL string `yaml:"base_url"`
	// Token 是 Devin session token；不会写入日志。
	Token string `yaml:"token"`
	// Model 是 Devin chat model UID。
	Model string `yaml:"model"`
	// Proxy 是可选的 HTTP/HTTPS 代理地址；为空时直连或走系统环境变量。
	Proxy string `yaml:"proxy"`
	// ForceHTTP1 为 true 时强制使用 HTTP/1.1，每请求独立 TCP 连接，
	// 避免 HTTP/2 单连接多 stream 复用导致的上游并发瓶颈（首字延迟飙升/卡住）。
	// 行为对齐 Devin 客户端多窗口各自独立连接的模式。默认 true。
	ForceHTTP1 *bool `yaml:"force_http1"`
	// StreamIdleTimeout 是上游流两次帧之间的最长等待时间，例如 5m；0 表示不限制。
	StreamIdleTimeout string `yaml:"stream_idle_timeout"`
	// IdleTimeout 是 StreamIdleTimeout 解析后的值，由 Validate 填充。
	IdleTimeout time.Duration `yaml:"-"`
	// ReasoningTiers 把「基础模型 + 推理档位」映射成实际 Devin uid。
	// 例如 swe-2 + high -> swe-2-high。
	ReasoningTiers map[string]map[string]string `yaml:"reasoning_tiers"`
}

// DebugConfig 保存请求级调试日志配置。
type DebugConfig struct {
	// Enabled 表示是否在配置文件同目录的 logs 下写入请求调试日志。
	Enabled bool `yaml:"enabled"`
}

// AuthConfig 保存对外 OpenAI 兼容接口的访问控制配置。
type AuthConfig struct {
	// APIKey 是客户端访问 /v1/* 接口所需的密钥；为空时不启用鉴权。
	APIKey string `yaml:"api_key"`
}

// Load 从 YAML 文件读取并校验配置。
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var config Config
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := applyEnvironment(&config); err != nil {
		return Config{}, fmt.Errorf("apply environment for %q: %w", path, err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return config, nil
}

// Validate 检查配置并填充默认值。
func (config *Config) Validate() error {
	if config.Server.Listen == "" {
		config.Server.Listen = defaultListen
	}
	if config.Server.MaxConcurrency <= 0 {
		config.Server.MaxConcurrency = 1024
	}
	// ForceHTTP1 默认开启：HTTP/2 单连接多 stream 复用是并发首字延迟飙升的根因。
	if config.Devin.ForceHTTP1 == nil {
		force := true
		config.Devin.ForceHTTP1 = &force
	}
	if config.Devin.StreamIdleTimeout == "" {
		config.Devin.StreamIdleTimeout = "5m"
	}
	idleTimeout, err := time.ParseDuration(config.Devin.StreamIdleTimeout)
	if err != nil {
		return fmt.Errorf("devin.stream_idle_timeout %q: %w", config.Devin.StreamIdleTimeout, err)
	}
	if idleTimeout < 0 {
		return errors.New("devin.stream_idle_timeout cannot be negative")
	}
	config.Devin.IdleTimeout = idleTimeout
	if err := validateBaseURL(config.Devin.BaseURL); err != nil {
		return err
	}
	if err := config.normalizeReasoningTiers(); err != nil {
		return err
	}
	return nil
}

// normalizeReasoningTiers 统一 effort 大小写并拒绝空模型/空档位。
func (config *Config) normalizeReasoningTiers() error {
	if len(config.Devin.ReasoningTiers) == 0 {
		return nil
	}
	normalized := make(map[string]map[string]string, len(config.Devin.ReasoningTiers))
	for model, tiers := range config.Devin.ReasoningTiers {
		model = strings.TrimSpace(model)
		if model == "" {
			return errors.New("devin.reasoning_tiers contains an empty model")
		}
		if len(tiers) == 0 {
			return fmt.Errorf("devin.reasoning_tiers[%s] is empty", model)
		}
		normalizedTiers := make(map[string]string, len(tiers))
		for effort, target := range tiers {
			effort = strings.ToLower(strings.TrimSpace(effort))
			target = strings.TrimSpace(target)
			if effort == "" || target == "" {
				return fmt.Errorf("devin.reasoning_tiers[%s] contains an empty effort or target", model)
			}
			normalizedTiers[effort] = target
		}
		normalized[model] = normalizedTiers
	}
	config.Devin.ReasoningTiers = normalized
	return nil
}

// validateBaseURL 检查上游地址的 scheme 与 host，避免把 token 发到意外的协议上。
func validateBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("devin.base_url %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("devin.base_url %q: scheme must be http or https", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("devin.base_url %q: host is required", raw)
	}
	return nil
}
