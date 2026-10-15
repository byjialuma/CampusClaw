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

	JWT JWTConfig

	Embedding EmbeddingConfig
	Qdrant    QdrantConfig
	LLM       LLMConfig
	MCP       MCPConfig

	SeedTeacherA  SeedAccount
	SeedStudentA1 SeedAccount
	SeedStudentB1 SeedAccount
}

// JWTConfig 是 JWT access token 参数。
type JWTConfig struct {
	Secret      string
	ExpiryHours int
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
// MinScore 是相似度下限（SEARCH_MIN_SCORE，取值区间 [0,1]）：低于该值的召回分片不作为回答依据。
type QdrantConfig struct {
	URL      string
	TopK     int
	MinScore float64
}

// LLMConfig 是 Chat Completions 端点参数。
type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// MCPConfig 是主机侧（CampusClaw）调用自建 MCP 工具服务的参数。
// AuthToken 仅用于播种预置服务器，可为空（为空时只写无令牌的服务器行）。
type MCPConfig struct {
	AuthToken string
	Timeout   time.Duration
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

	llm, err := loadLLMConfig(req)
	if err != nil {
		return nil, err
	}

	mcp, err := loadMCPConfig()
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
		DataDir: getenv("DATA_DIR", "/app/data"),
		SeedDir: getenv("SEED_DIR", "/app/seed/materials"),
		JWT: JWTConfig{
			Secret:      req("JWT_SECRET"),
			ExpiryHours: getenvInt("JWT_EXPIRY_HOURS", 2),
		},
		Embedding: emb,
		Qdrant:    qdr,
		LLM:       llm,
		MCP:       mcp,
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

	// JWT_SECRET 长度至少 32 字节（HS256 最低安全要求）。
	if len(cfg.JWT.Secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET 长度必须至少 32 字节，当前 %d 字节", len(cfg.JWT.Secret))
	}

	return cfg, nil
}

// loadLLMConfig 加载 Chat Completions 端点配置。
func loadLLMConfig(req func(string) string) (LLMConfig, error) {
	llm := LLMConfig{
		BaseURL: req("LLM_BASE_URL"),
		APIKey:  req("LLM_API_KEY"),
		Model:   req("LLM_MODEL"),
		Timeout: time.Duration(getenvInt("LLM_TIMEOUT_SECONDS", 30)) * time.Second,
	}
	if llm.Timeout < 5*time.Second {
		return LLMConfig{}, fmt.Errorf("LLM_TIMEOUT_SECONDS 必须至少 5，当前为 %d", int(llm.Timeout.Seconds()))
	}
	// 去除末尾斜杠，避免拼接出双斜杠。
	if len(llm.BaseURL) > 0 && llm.BaseURL[len(llm.BaseURL)-1] == '/' {
		llm.BaseURL = llm.BaseURL[:len(llm.BaseURL)-1]
	}
	return llm, nil
}

// loadMCPConfig 加载主机侧 MCP 调用参数：令牌可空，超时不得低于 1 秒，非法值报错。
func loadMCPConfig() (MCPConfig, error) {
	cfg := MCPConfig{AuthToken: os.Getenv("MCP_AUTH_TOKEN")}
	raw := os.Getenv("MCP_TIMEOUT_SECONDS")
	if raw == "" {
		cfg.Timeout = 10 * time.Second
		return cfg, nil
	}
	sec, err := strconv.Atoi(raw)
	if err != nil {
		return MCPConfig{}, fmt.Errorf("MCP_TIMEOUT_SECONDS 必须是整数，当前为 %q", raw)
	}
	if sec < 1 {
		return MCPConfig{}, fmt.Errorf("MCP_TIMEOUT_SECONDS 必须至少 1，当前为 %d", sec)
	}
	cfg.Timeout = time.Duration(sec) * time.Second
	return cfg, nil
}

// loadSearchConfig 校验向量检索相关环境变量。
func loadSearchConfig(req func(string) string) (EmbeddingConfig, QdrantConfig, error) {
	provider := req("EMBEDDING_PROVIDER")
	qdr := QdrantConfig{
		URL:      req("QDRANT_URL"),
		TopK:     getenvInt("SEARCH_TOP_K", 5),
		MinScore: getenvFloat("SEARCH_MIN_SCORE", 0.0),
	}
	if qdr.TopK < 1 || qdr.TopK > 10 {
		return EmbeddingConfig{}, QdrantConfig{}, fmt.Errorf("SEARCH_TOP_K 必须在 1-10 之间，当前为 %d", qdr.TopK)
	}
	// 阈值越界或非法一律回落到默认 0.0，不阻断启动（默认等价于「仅丢弃 Score <= 0」，向后兼容）。
	if qdr.MinScore < 0 || qdr.MinScore > 1 {
		qdr.MinScore = 0.0
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

func getenvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}
