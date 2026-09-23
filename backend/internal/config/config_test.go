package config

import (
	"strings"
	"testing"
)

// setBaseEnv 写入除检索配置外的全部必需变量。
func setBaseEnv(t *testing.T) {
	t.Helper()
	for _, kv := range [][2]string{
		{"MYSQL_USER", "u"},
		{"MYSQL_PASSWORD", "p"},
		{"SEED_TEACHER_A_USERNAME", "ta"},
		{"SEED_TEACHER_A_PASSWORD", "tap"},
		{"SEED_STUDENT_A1_USERNAME", "a1"},
		{"SEED_STUDENT_A1_PASSWORD", "a1p"},
		{"SEED_STUDENT_B1_USERNAME", "b1"},
		{"SEED_STUDENT_B1_PASSWORD", "b1p"},
		{"QDRANT_URL", "http://qdrant:6333"},
	} {
		t.Setenv(kv[0], kv[1])
	}
}

func TestLoad_StubProviderNeedsNoCredentials(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("EMBEDDING_PROVIDER", "stub")
	t.Setenv("EMBEDDING_DIM", "256")
	// openai 专用变量保持未设置/空，stub 不应要求它们。
	t.Setenv("EMBEDDING_API_KEY", "")
	t.Setenv("EMBEDDING_BASE_URL", "")
	t.Setenv("EMBEDDING_MODEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("stub provider 应免密钥启动，得到错误: %v", err)
	}
	if cfg.Embedding.Provider != EmbeddingProviderStub || cfg.Embedding.Dim != 256 {
		t.Fatalf("embedding 配置异常: %+v", cfg.Embedding)
	}
	if cfg.Qdrant.URL != "http://qdrant:6333" || cfg.Qdrant.TopK != 5 {
		t.Fatalf("qdrant 默认配置异常: %+v", cfg.Qdrant)
	}
}

func TestLoad_OpenAIRequiresCredentials(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("EMBEDDING_PROVIDER", "openai")
	t.Setenv("EMBEDDING_DIM", "2048")
	t.Setenv("EMBEDDING_BASE_URL", "https://example.invalid/v1")
	t.Setenv("EMBEDDING_MODEL", "embedding-x")
	t.Setenv("EMBEDDING_API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("openai provider 缺少 API_KEY 时必须报错")
	}

	t.Setenv("EMBEDDING_API_KEY", "sk-test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("凭证齐全时应通过: %v", err)
	}
	if cfg.Embedding.Model != "embedding-x" {
		t.Fatalf("模型名未载入: %+v", cfg.Embedding)
	}
}

func TestLoad_InvalidProvider(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("EMBEDDING_PROVIDER", "wat")
	t.Setenv("EMBEDDING_DIM", "8")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "EMBEDDING_PROVIDER") {
		t.Fatalf("非法 provider 应报明确错误，得到: %v", err)
	}
}

func TestLoad_BadDim(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("EMBEDDING_PROVIDER", "stub")
	for _, v := range []string{"", "0", "-3", "abc"} {
		t.Setenv("EMBEDDING_DIM", v)
		if _, err := Load(); err == nil {
			t.Fatalf("EMBEDDING_DIM=%q 必须报错", v)
		}
	}
}

func TestLoad_TopKRange(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("EMBEDDING_PROVIDER", "stub")
	t.Setenv("EMBEDDING_DIM", "8")
	t.Setenv("SEARCH_TOP_K", "0")
	if _, err := Load(); err == nil {
		t.Fatal("SEARCH_TOP_K=0 必须报错")
	}
	t.Setenv("SEARCH_TOP_K", "11")
	if _, err := Load(); err == nil {
		t.Fatal("SEARCH_TOP_K=11 必须报错")
	}
	t.Setenv("SEARCH_TOP_K", "3")
	if cfg, err := Load(); err != nil || cfg.Qdrant.TopK != 3 {
		t.Fatalf("SEARCH_TOP_K=3 应通过，得到 cfg=%v err=%v", cfg, err)
	}
}

func TestLoad_MissingCoreVarsReported(t *testing.T) {
	// 完全不设任何变量：必须返回列明缺项的错误。
	t.Setenv("MYSQL_USER", "")
	if _, err := Load(); err == nil {
		t.Fatal("缺少必需环境变量时必须返回错误")
	}
}
