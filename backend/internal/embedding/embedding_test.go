package embedding

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"campusclaw/internal/config"
)

func TestNew_UnknownProvider(t *testing.T) {
	if _, err := New(config.EmbeddingConfig{Provider: "wat", Dim: 4}); err == nil {
		t.Fatal("未知 provider 必须返回错误")
	}
}

// fakeEmbeddingServer 记录请求次数/鉴权头，并把每条输入回送固定维度向量。
func fakeEmbeddingServer(t *testing.T, dim int, calls *int32, seenAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		*seenAuth = r.Header.Get("Authorization")

		body, _ := io.ReadAll(r.Body)
		var req embeddingRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("model 未透传: %s", req.Model)
		}

		// 故意逆序返回，验证客户端按 index 归位。
		resp := embeddingResponse{}
		for i := len(req.Input) - 1; i >= 0; i-- {
			v := make([]float32, dim)
			v[0] = float32(len(req.Input[i])) // 首维携带文本长度，供断言
			resp.Data = append(resp.Data, embeddingData{Embedding: v, Index: i})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestOpenAIClient_AuthHeaderBatchingAndOrder(t *testing.T) {
	var calls int32
	var auth string
	srv := fakeEmbeddingServer(t, 8, &calls, &auth)
	defer srv.Close()

	c, err := New(config.EmbeddingConfig{
		Provider: config.EmbeddingProviderOpenAI,
		BaseURL:  srv.URL, APIKey: "sk-secret", Model: "test-model", Dim: 8,
	})
	if err != nil {
		t.Fatal(err)
	}

	texts := make([]string, MaxBatchSize+5)
	for i := range texts {
		texts[i] = "txt"
	}
	vecs, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("返回条数 %d，期望 %d", len(vecs), len(texts))
	}
	if calls != 2 {
		t.Fatalf("21 条文本应拆成 2 批，实际请求 %d 次", calls)
	}
	if auth != "Bearer sk-secret" {
		t.Fatalf("鉴权头异常: %q", auth)
	}
	// 逆序响应必须按 index 归位：第 0 条长度 3。
	if vecs[0][0] != 3 {
		t.Fatalf("结果未按 index 归位，vecs[0][0]=%v", vecs[0][0])
	}
}

func TestOpenAIClient_DimMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{1, 2}, "index": 0}},
		})
	}))
	defer srv.Close()

	c, _ := New(config.EmbeddingConfig{
		Provider: config.EmbeddingProviderOpenAI,
		BaseURL:  srv.URL, APIKey: "k", Model: "m", Dim: 4,
	})
	if _, err := c.Embed(context.Background(), []string{"hi"}); err == nil {
		t.Fatal("维度不符必须报错")
	}
}

func TestOpenAIClient_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer srv.Close()

	c, _ := New(config.EmbeddingConfig{
		Provider: config.EmbeddingProviderOpenAI,
		BaseURL:  srv.URL, APIKey: "k", Model: "m", Dim: 4,
	})
	_, err := c.Embed(context.Background(), []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("应透传状态码与错误信息，得到: %v", err)
	}
}

func TestStub_DeterministicAndDim(t *testing.T) {
	e := newStubEmbedder(64)
	v1, err := e.Embed(context.Background(), []string{"集合的运算 intersection"})
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := e.Embed(context.Background(), []string{"集合的运算 intersection"})
	if len(v1[0]) != 64 {
		t.Fatalf("维度应为 64，实际 %d", len(v1[0]))
	}
	if !vectorsEqual(v1[0], v2[0]) {
		t.Fatal("相同文本必须产生相同向量")
	}
}

func TestStub_DifferentTextDifferentVector(t *testing.T) {
	e := newStubEmbedder(256)
	a, _ := e.Embed(context.Background(), []string{"集合论基础知识"})
	b, _ := e.Embed(context.Background(), []string{"体育课游泳教学"})
	if vectorsEqual(a[0], b[0]) {
		t.Fatal("完全不同的文本不应产生相同向量")
	}
}

func TestStub_SharedTermsAreSimilar(t *testing.T) {
	e := newStubEmbedder(1024)
	chunk, _ := e.Embed(context.Background(), []string{"本章讲解 集合的交集与并集 运算规则"})
	hit, _ := e.Embed(context.Background(), []string{"集合的交集"})
	miss, _ := e.Embed(context.Background(), []string{"游泳课安全须知"})

	simHit := cosine(chunk[0], hit[0])
	simMiss := cosine(chunk[0], miss[0])
	if !(simHit > simMiss) {
		t.Fatalf("共享词项的相似度应更高: hit=%.3f miss=%.3f", simHit, simMiss)
	}
}

func vectorsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
