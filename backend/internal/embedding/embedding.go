// Package embedding 把文本批量向量化。生产走 OpenAI 兼容云接口，测试/离线可用确定性 stub。
package embedding

import (
	"context"
	"fmt"

	"campusclaw/internal/config"
)

// MaxBatchSize 是单次向量化请求允许的最大文本条数。
const MaxBatchSize = 16

// Embedder 把一批文本变成与顺序对应的等长向量。
type Embedder interface {
	// Embed 返回的向量条数必须与 texts 相同，每条长度必须等于 Dim。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dim 返回向量维度。
	Dim() int
}

// New 按配置创建向量化器。
func New(cfg config.EmbeddingConfig) (Embedder, error) {
	switch cfg.Provider {
	case config.EmbeddingProviderOpenAI:
		return newOpenAIClient(cfg)
	case config.EmbeddingProviderStub:
		return newStubEmbedder(cfg.Dim), nil
	default:
		return nil, fmt.Errorf("未知 EMBEDDING_PROVIDER: %q", cfg.Provider)
	}
}

// embedBatched 把 texts 按 MaxBatchSize 分批调用 fn，并按原顺序拼接结果。
func embedBatched(ctx context.Context, texts []string,
	fn func(context.Context, []string) ([][]float32, error)) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += MaxBatchSize {
		end := start + MaxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := fn(ctx, batch)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(batch) {
			return nil, fmt.Errorf("向量化返回条数不符: 期望 %d，实际 %d", len(batch), len(vecs))
		}
		out = append(out, vecs...)
	}
	return out, nil
}
