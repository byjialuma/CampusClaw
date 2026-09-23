package vector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"campusclaw/internal/model"
)

func TestDeterministicID(t *testing.T) {
	a := DeterministicID(7, 3)
	b := DeterministicID(7, 3)
	if a != b {
		t.Fatal("同文档同分片必须产生相同 ID")
	}
	if c := DeterministicID(7, 4); c == a {
		t.Fatal("不同分片不应共用 ID")
	}
	matched, err := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, a)
	if err != nil || !matched {
		t.Fatalf("ID 不是合法 UUID: %q", a)
	}
}

func TestEnsureCollection(t *testing.T) {
	var mu sync.Mutex
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":true,"status":"ok"}`)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.EnsureCollection(context.Background(), 320); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(paths, "|")
	if !strings.Contains(joined, "PUT /collections/chunks") {
		t.Fatalf("未创建集合: %s", joined)
	}
	if !strings.Contains(joined, "/index") {
		t.Fatalf("未创建负载索引: %s", joined)
	}
	if !strings.Contains(bodies[0], `"Cosine"`) || !strings.Contains(bodies[0], `"size":320`) {
		t.Fatalf("集合参数异常: %s", bodies[0])
	}
	indexBodies := strings.Join(bodies[1:], "|")
	if !strings.Contains(indexBodies, `"class_id"`) || !strings.Contains(indexBodies, `"document_id"`) {
		t.Fatalf("必须为 class_id/document_id 建索引: %s", indexBodies)
	}
}

func TestEnsureCollection_ToleratesExistingIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/index") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"status":{"error":"Field index already exists"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"result":true,"status":"ok"}`)
	}))
	defer srv.Close()

	if err := New(srv.URL).EnsureCollection(context.Background(), 8); err != nil {
		t.Fatalf("索引已存在的 400 应被容忍: %v", err)
	}
}

func TestEnsureCollection_ToleratesExistingCollection409(t *testing.T) {
	// Qdrant v1.12 对已存在集合返回 409（服务重启场景必须幂等成功）。
	var indexCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/index") {
			indexCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"status":{"error":"Field index already exists"}}`)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"status":{"error":"Wrong input: Collection `+"`chunks`"+` already exists!"}}`)
	}))
	defer srv.Close()

	if err := New(srv.URL).EnsureCollection(context.Background(), 8); err != nil {
		t.Fatalf("集合已存在的 409 应被容忍（重启幂等）: %v", err)
	}
	if indexCalls != 2 {
		t.Fatalf("409 后仍应继续确保负载索引，实际调用 %d 次", indexCalls)
	}
}

func TestUpsertPoints(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "wait=true") {
			t.Error("upsert 必须 wait=true")
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = io.WriteString(w, `{"result":{"operation_id":0},"status":"ok"}`)
	}))
	defer srv.Close()

	pts := []Point{{
		ID:     DeterministicID(1, 0),
		Vector: []float32{1, 0},
		Payload: Payload{
			ClassID: 2, DocumentID: 1, ChunkIndex: 0, FileName: "讲义.txt", FileType: "txt",
			Text: "正文", Locator: model.Locator{Kind: model.LocatorLines, StartLine: 1, EndLine: 2},
		},
	}}
	if err := New(srv.URL).UpsertPoints(context.Background(), pts); err != nil {
		t.Fatal(err)
	}
	points := got["points"].([]any)
	if len(points) != 1 {
		t.Fatalf("应写入 1 个点: %v", points)
	}
}

func TestDeleteAndCountByDocument(t *testing.T) {
	var deleteBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/delete"):
			_ = json.Unmarshal(raw, &deleteBody)
		case strings.HasSuffix(r.URL.Path, "/count"):
			_, _ = io.WriteString(w, `{"result":{"count":4}}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.DeleteByDocument(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	n, err := c.CountByDocument(context.Background(), 9)
	if err != nil || n != 4 {
		t.Fatalf("清点结果异常 n=%d err=%v", n, err)
	}
	filterJSON, _ := json.Marshal(deleteBody["filter"])
	if !strings.Contains(string(filterJSON), `"document_id"`) {
		t.Fatalf("删除过滤必须按 document_id: %s", filterJSON)
	}
}

func TestSearch_ClassIDFilterIsMandatory(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/chunks/points/query" {
			t.Fatalf("检索端点异常: %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &reqBody)
		_, _ = io.WriteString(w, `{
		  "result": {"points": [
		    {"id": "11111111-1111-4111-8111-111111111111", "score": 0.91,
		     "payload": {"class_id": 1, "document_id": 5, "chunk_index": 2,
		       "file_name": "A班讲义.txt", "file_type": "txt", "text": "交集片段",
		       "locator": {"kind": "lines", "startLine": 10, "endLine": 12}}}
		  ]}
		}`)
	}))
	defer srv.Close()

	hits, err := New(srv.URL).Search(context.Background(), []float32{0.1, 0.2}, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("应命中 1 条，实际 %d", len(hits))
	}
	h := hits[0]
	if h.Score < 0.9 || h.Payload.DocumentID != 5 || h.Payload.Locator.StartLine != 10 ||
		h.Payload.Locator.Kind != model.LocatorLines {
		t.Fatalf("命中映射异常: %+v", h)
	}

	// 核心断言：发往向量库的请求体必须强制携带 class_id must 过滤。
	filter, ok := reqBody["filter"].(map[string]any)
	if !ok {
		t.Fatal("检索请求缺少 filter")
	}
	must, ok := filter["must"].([]any)
	if !ok || len(must) != 1 {
		t.Fatalf("filter.must 必须恰好 1 条: %v", filter)
	}
	cond := must[0].(map[string]any)
	if cond["key"] != "class_id" {
		t.Fatalf("过滤键必须是 class_id: %v", cond)
	}
	value := cond["match"].(map[string]any)["value"]
	if number, _ := value.(float64); number != 1 {
		t.Fatalf("过滤值必须是会话班级 1: %v", value)
	}
	if topK, _ := reqBody["limit"].(float64); topK != 5 {
		t.Fatalf("limit 应为 topK=5: %v", reqBody["limit"])
	}
	// /points/query（新版 Query API）的向量字段名必须是 query；
	// 误用旧版 /points/search 的 "vector" 会被忽略，退化为无序滚动、score 全 0。
	if _, ok := reqBody["query"].([]any); !ok {
		t.Fatalf("检索请求必须使用 query 字段携带查询向量: %v", reqBody)
	}
	if _, present := reqBody["vector"]; present {
		t.Fatalf("禁止使用旧版 vector 字段（Query API 会忽略）: %v", reqBody)
	}
}

func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("健康探测端点异常: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "healthz check passed")
	}))
	defer srv.Close()
	if err := New(srv.URL).Ping(context.Background()); err != nil {
		t.Fatalf("健康探测失败: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if err := New(bad.URL).Ping(context.Background()); err == nil {
		t.Fatal("503 时健康探测必须返回错误")
	}
}
