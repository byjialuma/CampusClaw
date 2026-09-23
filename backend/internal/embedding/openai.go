package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"campusclaw/internal/config"
)

// requestTimeout 是单批向量化请求的超时。
const requestTimeout = 20 * time.Second

// openAIClient 调用任意 OpenAI 兼容的 /embeddings 端点（国内主流云厂商均兼容）。
type openAIClient struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
	http    *http.Client
}

func newOpenAIClient(cfg config.EmbeddingConfig) (*openAIClient, error) {
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" || cfg.Dim <= 0 {
		return nil, fmt.Errorf("openai 向量化器需要 EMBEDDING_BASE_URL/API_KEY/MODEL/DIM")
	}
	return &openAIClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		dim:     cfg.Dim,
		http:    &http.Client{Timeout: requestTimeout},
	}, nil
}

func (c *openAIClient) Dim() int { return c.dim }

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed 分批请求并按输入顺序返回向量。
func (c *openAIClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return embedBatched(ctx, texts, c.embedBatch)
}

func (c *openAIClient) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{Model: c.model, Input: batch})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用向量化接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var ae apiError
		_ = json.NewDecoder(resp.Body).Decode(&ae)
		if ae.Error.Message != "" {
			return nil, fmt.Errorf("向量化接口返回 %d: %s", resp.StatusCode, ae.Error.Message)
		}
		return nil, fmt.Errorf("向量化接口返回状态码 %d", resp.StatusCode)
	}

	var out embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析向量化响应失败: %w", err)
	}
	if len(out.Data) != len(batch) {
		return nil, fmt.Errorf("向量化返回条数不符: 期望 %d，实际 %d", len(batch), len(out.Data))
	}

	// 厂商不保证 data 顺序，按 index 归位。
	vecs := make([][]float32, len(batch))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("向量化返回非法 index: %d", d.Index)
		}
		if len(d.Embedding) != c.dim {
			return nil, fmt.Errorf("向量维度不符: 期望 %d，实际 %d", c.dim, len(d.Embedding))
		}
		vecs[d.Index] = d.Embedding
	}
	for i, v := range vecs {
		if v == nil {
			return nil, fmt.Errorf("向量化结果缺少 index=%d", i)
		}
	}
	return vecs, nil
}
