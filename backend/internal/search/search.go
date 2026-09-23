// Package search 编排知识库语义检索：查询向量化 → Qdrant 班级内召回 → 结果映射。
package search

import (
	"context"
	"fmt"

	"campusclaw/internal/embedding"
	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// vectorSearcher 是向量召回能力（*vector.Client 满足）。
type vectorSearcher interface {
	Search(ctx context.Context, queryVec []float32, classID int64, topK int) ([]vector.Hit, error)
}

// Result 是面向 API 的一条检索命中。
type Result struct {
	DocumentID int64         `json:"documentId"`
	ChunkID    string        `json:"chunkId"`
	FileName   string        `json:"fileName"`
	FileType   string        `json:"fileType"`
	Snippet    string        `json:"snippet"`
	Score      float32       `json:"score"`
	Locator    model.Locator `json:"locator"`
}

// Service 执行班级范围内的语义检索。
type Service struct {
	Embedder embedding.Embedder
	Vectors  vectorSearcher
	TopK     int
}

// Search 在指定班级内检索；classID 由服务端会话决定，与客户端参数无关。
func (s *Service) Search(ctx context.Context, query string, classID int64) ([]Result, error) {
	vecs, err := s.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("查询向量化失败: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("向量化返回条数异常: %d", len(vecs))
	}

	hits, err := s.Vectors.Search(ctx, vecs[0], classID, s.TopK)
	if err != nil {
		return nil, fmt.Errorf("向量召回失败: %w", err)
	}

	results := make([]Result, 0, len(hits))
	for _, h := range hits {
		// Qdrant 默认不设 score_threshold，正交命中（cosine=0）也会返回；
		// 零相关度不构成语义命中，必须剔除（stub 向量化器下无共享词项即为 0）。
		if h.Score <= 0 {
			continue
		}
		results = append(results, Result{
			DocumentID: h.Payload.DocumentID,
			ChunkID:    h.ID,
			FileName:   h.Payload.FileName,
			FileType:   h.Payload.FileType,
			Snippet:    h.Payload.Text,
			Score:      h.Score,
			Locator:    h.Payload.Locator,
		})
	}
	return results, nil
}
