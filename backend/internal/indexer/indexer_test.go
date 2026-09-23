package indexer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

func sampleMaterial() *model.Material {
	return &model.Material{ID: 42, ClassID: 7, OriginalName: "讲义.txt", FileType: "txt"}
}

func TestIndexMaterial_Success(t *testing.T) {
	st := newFakeStates()
	q := newFakeQdrant(t, false)
	mats := &fakeMaterials{m: sampleMaterial(), data: []byte("第一行内容\n第二行内容\n第三行内容")}
	svc := newService(st, mats, &fakeEmbedder{dim: 8}, q.server.URL)

	if err := svc.IndexMaterial(context.Background(), 42); err != nil {
		t.Fatalf("索引应成功: %v", err)
	}
	if n := st.ready[42]; n != 1 {
		t.Fatalf("状态应为 ready 且 1 片: %+v", st.ready)
	}
	if _, failed := st.failed[42]; failed {
		t.Fatal("不应落 failed")
	}
	if q.deletes != 1 || q.upserts != 1 {
		t.Fatalf("应先删后写: delete=%d upsert=%d", q.deletes, q.upserts)
	}
	count, err := svc.Vectors.CountByDocument(context.Background(), 42)
	if err != nil || count != 1 {
		t.Fatalf("向量库点数应为 1: count=%d err=%v", count, err)
	}
	p := q.lastUpserts[0].Payload
	if p.ClassID != 7 || p.DocumentID != 42 || p.FileName != "讲义.txt" || p.FileType != "txt" {
		t.Fatalf("payload 元数据错误: %+v", p)
	}
	if p.Locator.Kind != model.LocatorLines || p.Locator.StartLine != 1 || p.Locator.EndLine != 3 {
		t.Fatalf("locator 未透传: %+v", p.Locator)
	}
	// 点 ID 必须确定性，重索引指向同一点。
	if q.lastUpserts[0].ID == "" || q.lastUpserts[0].ID != vector.DeterministicID(42, 0) {
		t.Fatalf("点 ID 必须为确定性 ID: %s", q.lastUpserts[0].ID)
	}
}

func TestIndexMaterial_EmbeddingFailureMarksFailed(t *testing.T) {
	st := newFakeStates()
	q := newFakeQdrant(t, false)
	mats := &fakeMaterials{m: sampleMaterial(), data: []byte("内容一\n内容二")}
	svc := newService(st, mats, &fakeEmbedder{dim: 4, err: errors.New("embedding boom")}, q.server.URL)

	if err := svc.IndexMaterial(context.Background(), 42); err == nil {
		t.Fatal("embedding 失败应返回错误")
	}
	if _, ok := st.failed[42]; !ok {
		t.Fatal("必须落 failed 状态")
	}
	if !strings.Contains(st.failed[42], "向量化") {
		t.Fatalf("失败原因应透传: %s", st.failed[42])
	}
	if q.upserts != 0 || q.deletes != 0 {
		t.Fatal("embedding 失败后不得触碰向量库")
	}
}

func TestIndexMaterial_QdrantFailureMarksFailed(t *testing.T) {
	st := newFakeStates()
	q := newFakeQdrant(t, true)
	mats := &fakeMaterials{m: sampleMaterial(), data: []byte("内容一\n内容二")}
	svc := newService(st, mats, &fakeEmbedder{dim: 4}, q.server.URL)

	if err := svc.IndexMaterial(context.Background(), 42); err == nil {
		t.Fatal("向量库 500 应失败")
	}
	if _, ok := st.failed[42]; !ok {
		t.Fatal("必须落 failed 状态")
	}
	if q.deletes != 1 || q.upserts != 1 {
		t.Fatalf("应已尝试删旧与写入: delete=%d upsert=%d", q.deletes, q.upserts)
	}
	if _, ok := st.ready[42]; ok {
		t.Fatal("失败时不得置 ready")
	}
}

func TestIndexMaterial_RebuildIsIdempotent(t *testing.T) {
	st := newFakeStates()
	q := newFakeQdrant(t, false)
	mats := &fakeMaterials{m: sampleMaterial()}
	svc := newService(st, mats, &fakeEmbedder{dim: 4}, q.server.URL)

	mats.data = []byte("第一行\n第二行\n第三行")
	if err := svc.IndexMaterial(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	first, _ := svc.Vectors.CountByDocument(context.Background(), 42)
	if first != 1 {
		t.Fatalf("首次应为 1 点: %d", first)
	}

	// 重新索引同一文档（切块数不变），点数不应翻倍。
	if err := svc.IndexMaterial(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	second, _ := svc.Vectors.CountByDocument(context.Background(), 42)
	if second != first {
		t.Fatalf("重索引幂等性破坏: %d -> %d", first, second)
	}
}

func TestQueue_EnqueueAndDedupe(t *testing.T) {
	st := newFakeStates()
	q := newFakeQdrant(t, false)
	mats := &fakeMaterials{m: sampleMaterial(), data: []byte("内容行\n第二行")}
	gate := make(chan struct{})
	svc := newService(st, mats, &fakeEmbedder{dim: 4, gate: gate}, q.server.URL)

	queue := NewQueue(svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue.Start(ctx)

	// 同一 id 连续入队三次（作业尚未开始执行），只索引一次。
	for range 3 {
		if err := queue.Enqueue(ctx, 42); err != nil {
			t.Fatal(err)
		}
	}
	queue.mu.Lock()
	pending := len(queue.queued)
	queue.mu.Unlock()
	if pending != 1 {
		t.Fatalf("队列中同一 handout 只能有 1 个作业，实际 %d", pending)
	}
	close(gate)

	if !waitFor(func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		_, ok := st.ready[42]
		return ok
	}) {
		t.Fatal("队列未完成索引")
	}
	if q.upserts != 1 {
		t.Fatalf("重复入队必须去重，upsert 次数=%d", q.upserts)
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
