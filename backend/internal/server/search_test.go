package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"campusclaw/internal/model"
	searchpkg "campusclaw/internal/search"
)

// fakeSearcher 记录查询与班级，返回预置结果或故障。
type fakeSearcher struct {
	gotQuery  string
	gotClass  int64
	callCount int
	results   []searchpkg.Result
	err       error
}

func (f *fakeSearcher) Search(_ context.Context, query string, classID int64) ([]searchpkg.Result, error) {
	f.callCount++
	f.gotQuery = query
	f.gotClass = classID
	return f.results, f.err
}

func newSearchServer(t *testing.T, f *fakeSearcher) (*httptest.Server, *API) {
	t.Helper()
	a := newTestAPI()
	a.SearchSvc = f
	srv := httptest.NewServer(a.NewHandler())
	t.Cleanup(srv.Close)
	return srv, a
}

func TestSearchValidation(t *testing.T) {
	f := &fakeSearcher{}
	srv, _ := newSearchServer(t, f)
	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")

	for _, q := range []string{"", "q=", "q=" + "%20%20%20"} {
		resp, _ := a1.Get(srv.URL + "/api/search?" + q)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("查询 %q 期望 400，得到 %d", q, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// 超过 500 rune → 400。
	long := strings.Repeat("字", 501)
	resp, _ := a1.Get(srv.URL + "/api/search?q=" + url.QueryEscape(long))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("超长查询期望 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	if f.callCount != 0 {
		t.Fatal("校验失败不得触发检索")
	}
}

func TestSearchUsesSessionClassAndIgnoresParams(t *testing.T) {
	f := &fakeSearcher{}
	srv, _ := newSearchServer(t, f)

	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")
	// 伪造 class_id / classId 参数必须全部被忽略。
	resp, _ := a1.Get(srv.URL + "/api/search?q=集合&class_id=999&classId=999")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	if f.gotClass != 1 || f.gotQuery != "集合" {
		t.Fatalf("必须使用会话班级 1：class=%d query=%q", f.gotClass, f.gotQuery)
	}

	b1 := loginJarClient(t, srv.URL, "studentB1", "b1-pw")
	resp, _ = b1.Get(srv.URL + "/api/search?q=sets&class_id=1")
	resp.Body.Close()
	if f.gotClass != 2 {
		t.Fatalf("B 班学生必须锁定班级 2，实际 %d", f.gotClass)
	}
}

func TestSearchResultsShape(t *testing.T) {
	f := &fakeSearcher{results: []searchpkg.Result{
		{
			DocumentID: 7, ChunkID: "uuid-abc", FileName: "讲义.txt", FileType: "txt",
			Snippet: "集合是确定的整体", Score: 0.91,
			Locator: model.Locator{Kind: model.LocatorLines, StartLine: 3, EndLine: 5},
		},
	}}
	srv, _ := newSearchServer(t, f)
	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")

	resp, err := a1.Get(srv.URL + "/api/search?q=集合")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Results []struct {
			DocumentID int64         `json:"documentId"`
			ChunkID    string        `json:"chunkId"`
			FileName   string        `json:"fileName"`
			FileType   string        `json:"fileType"`
			Snippet    string        `json:"snippet"`
			Score      float64       `json:"score"`
			Locator    model.Locator `json:"locator"`
		} `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Results) != 1 {
		t.Fatalf("应返回 1 条结果")
	}
	r := body.Results[0]
	if r.DocumentID != 7 || r.ChunkID != "uuid-abc" || r.FileName != "讲义.txt" ||
		r.FileType != "txt" || r.Snippet == "" || r.Locator.Kind != model.LocatorLines ||
		r.Locator.StartLine != 3 {
		t.Fatalf("结果字段或来源缺失: %+v", r)
	}
}

func TestSearchEmptyResultsIsArray(t *testing.T) {
	f := &fakeSearcher{} // nil results
	srv, _ := newSearchServer(t, f)
	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")

	resp, err := a1.Get(srv.URL + "/api/search?q=无命中词")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"results":[]`) {
		t.Fatalf("无命中必须返回空数组而非 null: %s", string(raw))
	}
}

func TestSearchDownstreamFailureIs503(t *testing.T) {
	f := &fakeSearcher{err: errors.New("dial tcp 10.0.0.5:6333 connect: connection refused")}
	srv, _ := newSearchServer(t, f)
	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")

	resp, err := a1.Get(srv.URL + "/api/search?q=集合")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("下游故障期望 503，得到 %d", resp.StatusCode)
	}
	var msg map[string]string
	json.NewDecoder(resp.Body).Decode(&msg)
	if msg["error"] != searchUnavailableMessage {
		t.Fatalf("必须返回固定文案: %v", msg)
	}
	for _, leaked := range []string{"http://", "6333", "10.0.0.5"} {
		if strings.Contains(msg["error"], leaked) {
			t.Fatalf("错误响应泄漏内部信息 %q: %s", leaked, msg["error"])
		}
	}
}

// ---------- /health 双探测分支 ----------

func TestHealthVectorBranches(t *testing.T) {
	// Qdrant 挂：MySQL 正常也必须 503。
	a := newTestAPI()
	a.Vectors = &fakeVectorPinger{err: errors.New("qdrant down")}
	bad := httptest.NewServer(a.NewHandler())
	defer bad.Close()
	resp, err := http.Get(bad.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable ||
		body["vector"] != "down" || body["db"] != "up" {
		t.Fatalf("Qdrant 故障分支异常: %d %v", resp.StatusCode, body)
	}

	// 双正常：200。
	a2 := newTestAPI()
	a2.Vectors = &fakeVectorPinger{}
	good := httptest.NewServer(a2.NewHandler())
	defer good.Close()
	resp2, err := http.Get(good.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp2.Body).Decode(&body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || body["vector"] != "up" || body["db"] != "up" {
		t.Fatalf("双正常分支异常: %d %v", resp2.StatusCode, body)
	}
}
