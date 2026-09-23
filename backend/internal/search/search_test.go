package search

import (
	"context"
	"errors"
	"testing"

	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

type fakeEmbedder struct {
	vec []float32
	err error
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	if len(texts) != 1 {
		t := errors.New("检索只应向量化单条查询")
		return nil, t
	}
	return [][]float32{e.vec}, nil
}
func (e *fakeEmbedder) Dim() int { return len(e.vec) }

type fakeVectors struct {
	hits    []vector.Hit
	err     error
	gotVec  []float32
	gotCls  int64
	gotTopK int
}

func (f *fakeVectors) Search(_ context.Context, vec []float32, classID int64, topK int) ([]vector.Hit, error) {
	f.gotVec = vec
	f.gotCls = classID
	f.gotTopK = topK
	return f.hits, f.err
}

func TestServiceSearch_MapsHits(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	v := &fakeVectors{hits: []vector.Hit{
		{ID: "p-1", Score: 0.88, Payload: vector.Payload{
			ClassID: 3, DocumentID: 9, ChunkIndex: 2, FileName: "a.pdf", FileType: "pdf",
			Text:    "第三章 函数的概念",
			Locator: model.Locator{Kind: model.LocatorPage, Page: 4},
		}},
		{ID: "p-2", Score: 0.55, Payload: vector.Payload{
			DocumentID: 9, FileName: "a.pdf", FileType: "pdf", Text: "附录",
			Locator: model.Locator{Kind: model.LocatorPage, Page: 9},
		}},
	}}
	svc := &Service{Embedder: emb, Vectors: v, TopK: 6}

	results, err := svc.Search(context.Background(), "函数", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("应映射 2 条命中: %d", len(results))
	}
	r0 := results[0]
	if r0.DocumentID != 9 || r0.ChunkID != "p-1" || r0.FileName != "a.pdf" ||
		r0.Snippet != "第三章 函数的概念" || r0.Locator.Page != 4 || r0.Score != 0.88 {
		t.Fatalf("首条映射错误: %+v", r0)
	}
	if v.gotCls != 3 || v.gotTopK != 6 || len(v.gotVec) != 3 {
		t.Fatalf("召回参数错误: class=%d topK=%d", v.gotCls, v.gotTopK)
	}
}

func TestServiceSearch_EmptyIsNonNullSlice(t *testing.T) {
	svc := &Service{Embedder: &fakeEmbedder{vec: []float32{1}}, Vectors: &fakeVectors{}, TopK: 5}
	results, err := svc.Search(context.Background(), "x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("无命中必须返回非 nil 空切片")
	}
}

func TestServiceSearch_FiltersZeroScoreHits(t *testing.T) {
	v := &fakeVectors{hits: []vector.Hit{
		{ID: "zero", Score: 0, Payload: vector.Payload{DocumentID: 1, FileName: "a.txt", FileType: "txt"}},
		{ID: "neg", Score: -0.2, Payload: vector.Payload{DocumentID: 1, FileName: "a.txt", FileType: "txt"}},
		{ID: "good", Score: 0.31, Payload: vector.Payload{
			DocumentID: 1, FileName: "a.txt", FileType: "txt", Text: "命中",
			Locator: model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 1},
		}},
	}}
	svc := &Service{Embedder: &fakeEmbedder{vec: []float32{1}}, Vectors: v, TopK: 5}
	results, err := svc.Search(context.Background(), "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ChunkID != "good" {
		t.Fatalf("零分/负分命中必须剔除: %+v", results)
	}
}

func TestServiceSearch_Failures(t *testing.T) {
	// 向量化失败。
	svc := &Service{
		Embedder: &fakeEmbedder{err: errors.New("embed boom")},
		Vectors:  &fakeVectors{},
	}
	if _, err := svc.Search(context.Background(), "q", 1); err == nil {
		t.Fatal("embedding 故障应返回错误")
	}

	// 向量库失败。
	v := &fakeVectors{err: errors.New("qdrant boom")}
	svc = &Service{Embedder: &fakeEmbedder{vec: []float32{1}}, Vectors: v}
	if _, err := svc.Search(context.Background(), "q", 1); err == nil {
		t.Fatal("向量库故障应返回错误")
	}
}
