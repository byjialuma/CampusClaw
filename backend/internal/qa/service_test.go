package qa

import (
	"context"
	"fmt"
	"testing"

	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// mockEmbedder 返回固定向量。
type mockEmbedder struct {
	vec []float32
	err error
}

func (m *mockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = m.vec
	}
	return out, nil
}

func (m *mockEmbedder) Dim() int { return len(m.vec) }

// mockVector 返回固定命中。
type mockVector struct {
	hits []vector.Hit
	err  error
}

func (m *mockVector) Search(ctx context.Context, queryVec []float32, classID int64, topK int) ([]vector.Hit, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.hits, nil
}

// mockLLM 返回固定回答，并记录是否被调用。
type mockLLM struct {
	answer string
	err    error
	called bool
}

func (m *mockLLM) Ask(ctx context.Context, prompt string) (string, error) {
	m.called = true
	if m.err != nil {
		return "", m.err
	}
	return m.answer, nil
}

func TestAskWithHits(t *testing.T) {
	embedder := &mockEmbedder{vec: []float32{0.1, 0.2}}
	vectors := &mockVector{hits: []vector.Hit{
		{
			ID:    "chunk-1",
			Score: 0.9,
			Payload: vector.Payload{
				DocumentID: 101,
				FileName:   "集合论.txt",
				FileType:   "txt",
				Text:       "集合是数学的基本概念...",
				Locator:    model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 5},
			},
		},
		{
			ID:    "chunk-2",
			Score: 0.8,
			Payload: vector.Payload{
				DocumentID: 102,
				FileName:   "离散数学.pdf",
				FileType:   "pdf",
				Text:       "集合论是离散数学的基础...",
				Locator:    model.Locator{Kind: model.LocatorPage, Page: 3},
			},
		},
	}}
	llm := &mockLLM{answer: "集合是数学中的基本概念。"}

	svc := &Service{Embedder: embedder, Vectors: vectors, LLM: llm, TopK: 5}
	result, err := svc.Ask(context.Background(), "什么是集合？", 1)
	if err != nil {
		t.Fatalf("Ask 应成功: %v", err)
	}
	if result.Answer != "集合是数学中的基本概念。" {
		t.Fatalf("回答内容错误: %q", result.Answer)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("引用数量错误: %d", len(result.Citations))
	}
	if result.Citations[0].DocumentID != 101 || result.Citations[0].FileName != "集合论.txt" {
		t.Fatalf("第一条引用错误: %+v", result.Citations[0])
	}
	if result.Citations[1].DocumentID != 102 || result.Citations[1].Locator.Page != 3 {
		t.Fatalf("第二条引用错误: %+v", result.Citations[1])
	}
	if result.Citations[0].Snippet != "集合是数学的基本概念..." {
		t.Fatalf("第一条引用 snippet 错误: %q", result.Citations[0].Snippet)
	}
	if result.Citations[1].Snippet != "集合论是离散数学的基础..." {
		t.Fatalf("第二条引用 snippet 错误: %q", result.Citations[1].Snippet)
	}
}

func TestAskNoHits(t *testing.T) {
	embedder := &mockEmbedder{vec: []float32{0.1, 0.2}}
	vectors := &mockVector{hits: []vector.Hit{}}
	llm := &mockLLM{answer: "SHOULD NOT BE CALLED"}

	svc := &Service{Embedder: embedder, Vectors: vectors, LLM: llm, TopK: 5}
	result, err := svc.Ask(context.Background(), "什么是量子力学？", 1)
	if err != nil {
		t.Fatalf("Ask 应成功: %v", err)
	}
	if llm.called {
		t.Fatal("无切片时不应调用 LLM")
	}
	if result.Answer != "根据已有材料无法回答该问题" {
		t.Fatalf("回答应为固定句，得到: %q", result.Answer)
	}
	if len(result.Citations) != 0 {
		t.Fatalf("无召回时应无引用，得到 %d 条", len(result.Citations))
	}
}

func TestAskFiltersZeroScore(t *testing.T) {
	embedder := &mockEmbedder{vec: []float32{0.1, 0.2}}
	vectors := &mockVector{hits: []vector.Hit{
		{
			ID:    "chunk-1",
			Score: 0.9,
			Payload: vector.Payload{
				DocumentID: 101,
				FileName:   "相关.txt",
				FileType:   "txt",
				Text:       "相关内容",
				Locator:    model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 1},
			},
		},
		{
			ID:    "chunk-2",
			Score: 0,
			Payload: vector.Payload{
				DocumentID: 102,
				FileName:   "不相关.txt",
				FileType:   "txt",
				Text:       "不相关内容",
				Locator:    model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 1},
			},
		},
	}}
	llm := &mockLLM{answer: "回答"}

	svc := &Service{Embedder: embedder, Vectors: vectors, LLM: llm, TopK: 5}
	result, err := svc.Ask(context.Background(), "test", 1)
	if err != nil {
		t.Fatalf("Ask 应成功: %v", err)
	}
	if len(result.Citations) != 1 {
		t.Fatalf("零分命中应被过滤，引用数量应为 1，得到 %d", len(result.Citations))
	}
	if result.Citations[0].DocumentID != 101 {
		t.Fatalf("保留的引用应为相关文档，得到 %+v", result.Citations[0])
	}
}

func TestAskEmbedFail(t *testing.T) {
	embedder := &mockEmbedder{err: fmt.Errorf("embedding 服务不可用")}
	vectors := &mockVector{}
	llm := &mockLLM{}

	svc := &Service{Embedder: embedder, Vectors: vectors, LLM: llm, TopK: 5}
	_, err := svc.Ask(context.Background(), "test", 1)
	if err == nil {
		t.Fatal("Embed 失败应返回错误")
	}
}

func TestAskSearchFail(t *testing.T) {
	embedder := &mockEmbedder{vec: []float32{0.1, 0.2}}
	vectors := &mockVector{err: fmt.Errorf("向量库不可用")}
	llm := &mockLLM{}

	svc := &Service{Embedder: embedder, Vectors: vectors, LLM: llm, TopK: 5}
	_, err := svc.Ask(context.Background(), "test", 1)
	if err == nil {
		t.Fatal("Search 失败应返回错误")
	}
}

func TestAskLLMFail(t *testing.T) {
	embedder := &mockEmbedder{vec: []float32{0.1, 0.2}}
	vectors := &mockVector{hits: []vector.Hit{
		{
			ID:    "chunk-1",
			Score: 0.9,
			Payload: vector.Payload{
				DocumentID: 101,
				FileName:   "test.txt",
				FileType:   "txt",
				Text:       "内容",
				Locator:    model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 1},
			},
		},
	}}
	llm := &mockLLM{err: fmt.Errorf("LLM 服务不可用")}

	svc := &Service{Embedder: embedder, Vectors: vectors, LLM: llm, TopK: 5}
	_, err := svc.Ask(context.Background(), "test", 1)
	if err == nil {
		t.Fatal("LLM 失败应返回错误")
	}
}

func TestBuildPromptWithHits(t *testing.T) {
	hits := []vector.Hit{
		{
			Payload: vector.Payload{
				FileName: "集合论.txt",
				Text:     "集合是数学的基本概念",
				Locator:  model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 5},
			},
		},
		{
			Payload: vector.Payload{
				FileName: "离散数学.pdf",
				Text:     "集合论是离散数学的基础",
				Locator:  model.Locator{Kind: model.LocatorPage, Page: 3},
			},
		},
		{
			Payload: vector.Payload{
				FileName: "笔记.md",
				Text:     "集合的基本运算",
				Locator:  model.Locator{Kind: model.LocatorHeading, Path: "第一章 > 集合"},
			},
		},
	}

	prompt := buildPrompt("什么是集合？", hits)

	// 断言 prompt 包含问题
	if !containsString(prompt, "问题：什么是集合？") {
		t.Fatalf("prompt 应包含问题: %s", prompt)
	}
	// 断言 prompt 包含文件和 locator
	if !containsString(prompt, "[集合论.txt 第1-5行]") {
		t.Fatalf("prompt 应包含 TXT 定位: %s", prompt)
	}
	if !containsString(prompt, "[离散数学.pdf 第3页]") {
		t.Fatalf("prompt 应包含 PDF 定位: %s", prompt)
	}
	if !containsString(prompt, "[笔记.md 章节: 第一章 > 集合]") {
		t.Fatalf("prompt 应包含 Markdown 定位: %s", prompt)
	}
	// 断言 prompt 包含片段内容
	if !containsString(prompt, "集合是数学的基本概念") {
		t.Fatalf("prompt 应包含片段内容: %s", prompt)
	}
}

func TestBuildPromptNoHits(t *testing.T) {
	prompt := buildPrompt("什么是量子力学？", []vector.Hit{})
	if !containsString(prompt, "没有找到与问题相关的内容") {
		t.Fatalf("无召回时 prompt 应说明无材料: %s", prompt)
	}
	if !containsString(prompt, "根据已有材料无法回答该问题") {
		t.Fatalf("无召回时 prompt 应包含兜底文案: %s", prompt)
	}
}

func TestFormatLocator(t *testing.T) {
	tests := []struct {
		locator  model.Locator
		expected string
	}{
		{model.Locator{Kind: model.LocatorPage, Page: 3}, "第3页"},
		{model.Locator{Kind: model.LocatorHeading, Path: "第一章 > 集合"}, "章节: 第一章 > 集合"},
		{model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 5}, "第1-5行"},
		{model.Locator{Kind: "unknown"}, "未知位置"},
	}
	for _, tt := range tests {
		got := formatLocator(tt.locator)
		if got != tt.expected {
			t.Errorf("formatLocator(%+v) = %q, 期望 %q", tt.locator, got, tt.expected)
		}
	}
}

// containsString 检查 s 是否包含 substr。
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
