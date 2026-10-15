package config

import (
	"testing"
	"time"
)

// TestLoadMCPConfig 覆盖 MCP 配置的默认值、下限校验、非法值报错与合法值。
func TestLoadMCPConfig(t *testing.T) {
	t.Run("默认值", func(t *testing.T) {
		t.Setenv("MCP_TIMEOUT_SECONDS", "")
		t.Setenv("MCP_AUTH_TOKEN", "")
		cfg, err := loadMCPConfig()
		if err != nil {
			t.Fatalf("空配置不应报错: %v", err)
		}
		if cfg.Timeout != 10*time.Second {
			t.Fatalf("默认超时应为 10s，得到 %v", cfg.Timeout)
		}
		if cfg.AuthToken != "" {
			t.Fatalf("令牌应可空，得到 %q", cfg.AuthToken)
		}
	})

	t.Run("下限校验", func(t *testing.T) {
		t.Setenv("MCP_TIMEOUT_SECONDS", "0")
		if _, err := loadMCPConfig(); err == nil {
			t.Fatal("0 低于下限，应报错")
		}
	})

	t.Run("非法值报错", func(t *testing.T) {
		t.Setenv("MCP_TIMEOUT_SECONDS", "abc")
		if _, err := loadMCPConfig(); err == nil {
			t.Fatal("非整数应报错")
		}
	})

	t.Run("合法值", func(t *testing.T) {
		t.Setenv("MCP_TIMEOUT_SECONDS", "3")
		t.Setenv("MCP_AUTH_TOKEN", "secret-token")
		cfg, err := loadMCPConfig()
		if err != nil {
			t.Fatalf("合法值不应报错: %v", err)
		}
		if cfg.Timeout != 3*time.Second || cfg.AuthToken != "secret-token" {
			t.Fatalf("解析错误: %+v", cfg)
		}
	})
}
