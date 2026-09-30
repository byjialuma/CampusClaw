// Package qa 编排知识问答：问题向量化 → 班级内召回 → LLM 生成回答。
package qa

import (
	"context"
	"fmt"
	"strings"

	"campusclaw/internal/embedding"
	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// vectorSearcher 是向量召回能力（*vector.Client 满足）。
type vectorSearcher interface {
	Search(ctx context.Context, queryVec []float32, classID int64, topK int) ([]vector.Hit, error)
}

// LLM 是知识问答的大模型生成接口。
type LLM interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

// Citation 是回答中引用的一条材料来源。
type Citation struct {
	DocumentID int64         `json:"documentId"`
	FileName   string        `json:"fileName"`
	FileType   string        `json:"fileType"`
	Snippet    string        `json:"snippet"`
	Locator    model.Locator `json:"locator"`
	Score      float32       `json:"score"`
}

// Result 是知识问答的完整响应。
type Result struct {
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
}

// Service 执行班级范围内的知识问答。
type Service struct {
	Embedder embedding.Embedder
	Vectors  vectorSearcher
	LLM      LLM
	TopK     int
}

// Ask 在指定班级内回答用户问题；classID 由服务端会话决定，与客户端参数无关。
func (s *Service) Ask(ctx context.Context, question string, classID int64) (*Result, error) {
	// 1. 向量化问题
	vecs, err := s.Embedder.Embed(ctx, []string{question})
	if err != nil {
		return nil, fmt.Errorf("问题向量化失败: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("向量化返回条数异常: %d", len(vecs))
	}

	// 2. 向量库召回
	hits, err := s.Vectors.Search(ctx, vecs[0], classID, s.TopK)
	if err != nil {
		return nil, fmt.Errorf("向量召回失败: %w", err)
	}

	// 3. 过滤零相关度命中
	filtered := make([]vector.Hit, 0, len(hits))
	for _, h := range hits {
		if h.Score <= 0 {
			continue
		}
		filtered = append(filtered, h)
	}

	// 4. 无切片时不调用 LLM，直接返回固定句与空引用
	if len(filtered) == 0 {
		return &Result{
			Answer:    "根据已有材料无法回答该问题",
			Citations: []Citation{},
		}, nil
	}

	// 5. 构造 Prompt
	prompt := buildPrompt(question, filtered)

	// 6. LLM 生成回答
	answer, err := s.LLM.Ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM 生成回答失败: %w", err)
	}

	// 7. 组装响应
	citations := make([]Citation, 0, len(filtered))
	for _, h := range filtered {
		citations = append(citations, Citation{
			DocumentID: h.Payload.DocumentID,
			FileName:   h.Payload.FileName,
			FileType:   h.Payload.FileType,
			Snippet:    h.Payload.Text,
			Locator:    h.Payload.Locator,
			Score:      h.Score,
		})
	}

	return &Result{Answer: answer, Citations: citations}, nil
}

// buildPrompt 将问题与召回片段构造为 LLM prompt。
func buildPrompt(question string, hits []vector.Hit) string {
	if len(hits) == 0 {
		return fmt.Sprintf("问题：%s\n\n注意：当前班级材料中没有找到与问题相关的内容。请明确告知用户\"根据已有材料无法回答该问题\"。", question)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("问题：%s\n\n材料片段：\n", question))
	for i, h := range hits {
		locator := formatLocator(h.Payload.Locator)
		sb.WriteString(fmt.Sprintf("%d. [%s %s] %s\n", i+1, h.Payload.FileName, locator, h.Payload.Text))
	}
	return sb.String()
}

// formatLocator 将分片定位信息格式化为可读文本。
func formatLocator(l model.Locator) string {
	switch l.Kind {
	case model.LocatorPage:
		return fmt.Sprintf("第%d页", l.Page)
	case model.LocatorHeading:
		return fmt.Sprintf("章节: %s", l.Path)
	case model.LocatorLines:
		return fmt.Sprintf("第%d-%d行", l.StartLine, l.EndLine)
	default:
		return "未知位置"
	}
}
