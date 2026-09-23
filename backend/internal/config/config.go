// Package config 负责从环境变量读取配置并做启动期校验。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是应用运行所需的全部配置。
type Config struct {
	HTTPAddr string

	MySQL MySQLConfig

	DataDir string
	SeedDir string

	SessionTTL time.Duration

	Embedding EmbeddingConfig
	Qdrant    QdrantConfig

	SeedTeacherA  SeedAccount
	SeedStudentA1 SeedAccount
	SeedStudentB1 SeedAccount
}

// Embedding 提供方取值。
const (
	EmbeddingProviderOpenAI = "openai" // 任意 OpenAI 兼容 /embeddings 端点（国内云厂商）
	EmbeddingProviderStub   = "stub"   // 确定性假向量，仅供测试/离线，禁止用于生产语义检索
)

// EmbeddingConfig 是文本向量化参数。
type EmbeddingConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Dim      int
}

// QdrantConfig 是向量库连接与检索参数。
type QdrantConfig struct {
	URL  string
	TopK int
}

// MySQLConfig 是数据库连接参数。
type MySQLConfig struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
}

// SeedAccount 是一个预置账号的用户名与初始密码（仅首次播种使用）。
type SeedAccount struct {
	Username string
	Password string
}

// Load 从环境变量加载配置；缺少必需变量时返回列明缺项的错误。
func Load() (*Config, error) {
	var missing []string
	req := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	emb, qdr, err := loadSearchConfig(req)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		HTTPAddr: getenv("HTTP_ADDR", ":8080"),
		MySQL: MySQLConfig{
			Host:     getenv("MYSQL_HOST", "mysql"),
			Port:     getenv("MYSQL_PORT", "3306"),
			Database: getenv("MYSQL_DATABASE", "campusclaw"),
			User:     req("MYSQL_USER"),
			Password: req("MYSQL_PASSWORD"),
		},
		DataDir:    getenv("DATA_DIR", "/app/data"),
		SeedDir:    getenv("SEED_DIR", "/app/seed/materials"),
		SessionTTL: time.Duration(getenvInt("SESSION_TTL_SECONDS", 28800)) * time.Second,
		Embedding:  emb,
		Qdrant:     qdr,
		SeedTeacherA: SeedAccount{
			Username: req("SEED_TEACHER_A_USERNAME"),
			Password: req("SEED_TEACHER_A_PASSWORD"),
		},
		SeedStudentA1: SeedAccount{
			Username: req("SEED_STUDENT_A1_USERNAME"),
			Password: req("SEED_STUDENT_A1_PASSWORD"),
		},
		SeedStudentB1: SeedAccount{
			Username: req("SEED_STUDENT_B1_USERNAME"),
			Password: req("SEED_STUDENT_B1_PASSWORD"),
		},
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("缺少必需环境变量: %v", missing)
	}
	return cfg, nil
}

// loadSearchConfig 校验向量检索相关环境变量。
func loadSearchConfig(req func(string) string) (EmbeddingConfig, QdrantConfig, error) {
	provider := req("EMBEDDING_PROVIDER")
	qdr := QdrantConfig{
		URL:  req("QDRANT_URL"),
		TopK: getenvInt("SEARCH_TOP_K", 5),
	}
	if qdr.TopK < 1 || qdr.TopK > 10 {
		return EmbeddingConfig{}, QdrantConfig{}, fmt.Errorf("SEARCH_TOP_K 必须在 1-10 之间，当前为 %d", qdr.TopK)
	}

	dimRaw := req("EMBEDDING_DIM")
	dim, err := strconv.Atoi(dimRaw)
	if err != nil || dim <= 0 {
		return EmbeddingConfig{}, QdrantConfig{}, fmt.Errorf("EMBEDDING_DIM 必须是正整数，当前为 %q", dimRaw)
	}
	emb := EmbeddingConfig{Provider: provider, Dim: dim}

	switch provider {
	case EmbeddingProviderStub:
		// stub 无需端点/密钥/模型名。
	case EmbeddingProviderOpenAI:
		emb.BaseURL = req("EMBEDDING_BASE_URL")
		emb.APIKey = req("EMBEDDING_API_KEY")
		emb.Model = req("EMBEDDING_MODEL")
	default:
		return EmbeddingConfig{}, QdrantConfig{}, fmt.Errorf("EMBEDDING_PROVIDER 仅支持 %q 或 %q，当前为 %q",
			EmbeddingProviderOpenAI, EmbeddingProviderStub, provider)
	}
	return emb, qdr, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
