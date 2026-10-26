// 本文件验证配置未知字段拒绝和时间字段解析行为。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadRejectsUnknownFields 验证未知配置字段会被严格拒绝。
func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':18089'\n  typo: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want unknown field error")
	}
}

// TestLoadParsesListenAddress 验证服务监听地址来自 YAML 配置。
func TestLoadParsesListenAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.Listen != ":9090" {
		t.Fatalf("Listen = %q, want :9090", config.Server.Listen)
	}
}

// TestLoadDisablesDebugLoggingByDefault 的测试动机是保证生产配置未显式开启时不会写入请求内容。
func TestLoadDisablesDebugLoggingByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Debug.Enabled {
		t.Fatal("Debug.Enabled = true, want disabled by default")
	}
}

// TestLoadParsesAuthAPIKey 验证可选的 API Key 可从配置中读取。
func TestLoadParsesAuthAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\nauth:\n  api_key: 'my-secret-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Auth.APIKey != "my-secret-key" {
		t.Fatalf("Auth.APIKey = %q, want my-secret-key", config.Auth.APIKey)
	}
}

// TestLoadEnablesDebugLoggingExplicitly 的测试动机是保留排查协议问题时主动开启日志的能力。
func TestLoadEnablesDebugLoggingExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':9090'\ndebug:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Debug.Enabled {
		t.Fatal("Debug.Enabled = false, want explicitly enabled")
	}
}

// TestValidateUsesDefaultListen 的测试动机是未配置监听地址时仍有一个不常用的默认端口可用。
func TestValidateUsesDefaultListen(t *testing.T) {
	var loaded Config
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen != ":18089" {
		t.Fatalf("Listen = %q, want :18089", loaded.Server.Listen)
	}
	if loaded.Server.MaxConcurrency != 1024 {
		t.Fatalf("MaxConcurrency = %d, want 1024", loaded.Server.MaxConcurrency)
	}
	if loaded.Devin.ForceHTTP1 == nil || !*loaded.Devin.ForceHTTP1 {
		t.Fatal("ForceHTTP1 default = false, want true")
	}
}

// TestLoadAppliesEnvironmentOverrides 验证环境变量优先于 YAML，方便容器部署。
func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `server:
  listen: ":18089"
devin:
  token: "from-yaml"
  model: "yaml-model"
  stream_idle_timeout: "5m"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVIN_TOKEN", "from-env")
	t.Setenv("DEVIN_MODEL", "env-model")
	t.Setenv("SERVER_LISTEN", ":19999")
	t.Setenv("AUTH_API_KEY", "secret")
	t.Setenv("DEVIN_STREAM_IDLE_TIMEOUT", "30s")
	t.Setenv("DEVIN_FORCE_HTTP1", "false")

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen != ":19999" {
		t.Fatalf("Listen = %q", loaded.Server.Listen)
	}
	if loaded.Devin.Token != "from-env" || loaded.Devin.Model != "env-model" {
		t.Fatalf("devin = %#v", loaded.Devin)
	}
	if loaded.Auth.APIKey != "secret" {
		t.Fatalf("APIKey = %q", loaded.Auth.APIKey)
	}
	if loaded.Devin.IdleTimeout != 30*time.Second {
		t.Fatalf("IdleTimeout = %v, want 30s", loaded.Devin.IdleTimeout)
	}
	if loaded.Devin.ForceHTTP1 == nil || *loaded.Devin.ForceHTTP1 {
		t.Fatal("ForceHTTP1 should be false from environment")
	}
}

// TestValidateRejectsInvalidValues 验证非法超时与上游 scheme 会被拒绝。
func TestValidateRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"duration", "devin:\n  stream_idle_timeout: five\n", "stream_idle_timeout"},
		{"scheme", "devin:\n  base_url: ftp://example.com\n", "scheme"},
		{"host", "devin:\n  base_url: https://\n", "host"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(testCase.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

// TestValidateDefaultsIdleTimeout 验证未配置时使用 5 分钟空闲超时。
func TestValidateDefaultsIdleTimeout(t *testing.T) {
	var loaded Config
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.Devin.IdleTimeout != 5*time.Minute {
		t.Fatalf("IdleTimeout = %v, want 5m", loaded.Devin.IdleTimeout)
	}
}

// TestLoadRejectsInvalidBoolEnvironment 验证错误的环境变量值有明确报错。
func TestLoadRejectsInvalidBoolEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen: ':18089'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEBUG_ENABLED", "maybe")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "DEBUG_ENABLED") {
		t.Fatalf("error = %v", err)
	}
}

// TestValidateNormalizesReasoningTiers 验证档位键统一小写且空值被拒绝。
func TestValidateNormalizesReasoningTiers(t *testing.T) {
	var loaded Config
	loaded.Devin.ReasoningTiers = map[string]map[string]string{
		" swe-2 ": {"HIGH": " swe-2-high "},
	}
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Devin.ReasoningTiers["swe-2"]["high"]; got != "swe-2-high" {
		t.Fatalf("normalized tier = %q", got)
	}

	var invalid Config
	invalid.Devin.ReasoningTiers = map[string]map[string]string{"swe-2": {"high": ""}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("empty target should be rejected")
	}
}

// TestValidateCatalogRefresh 验证默认刷新间隔与非法值。
func TestValidateCatalogRefresh(t *testing.T) {
	var loaded Config
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.Catalog.RefreshInterval != 5*time.Minute {
		t.Fatalf("RefreshInterval = %v", loaded.Catalog.RefreshInterval)
	}
	var invalid Config
	invalid.Catalog.Refresh = "soon"
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid refresh should be rejected")
	}
}

// TestExampleConfigLoads 验证仓库里的 config.example.yaml 始终可解析。
func TestExampleConfigLoads(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "config.example.yaml")); err != nil {
		t.Fatal(err)
	}
}

// TestValidateCatalogModelMode 验证展示模式默认值与非法值。
func TestValidateCatalogModelMode(t *testing.T) {
	var loaded Config
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.Catalog.ModelMode != "raw" {
		t.Fatalf("ModelMode = %q", loaded.Catalog.ModelMode)
	}
	loaded.Catalog.ModelMode = "weird"
	if err := loaded.Validate(); err == nil {
		t.Fatal("invalid model mode should be rejected")
	}
}
