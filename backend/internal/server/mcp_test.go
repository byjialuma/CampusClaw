package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"campusclaw/internal/mcp"
	tutorpkg "campusclaw/internal/tutor"
)

// ---------- 5.x 测试替身 ----------

// mcpEntry 记录一台服务器与其所属班级（MCPServer 结构本身不含 class_id）。
type mcpEntry struct {
	server  tutorpkg.MCPServer
	classID int64
}

// mcpCallEntry 记录一条审计与其所属班级。
type mcpCallEntry struct {
	classID int64
	call    tutorpkg.MCPCall
}

// fakeMCPStore 是 mcpStore 的内存实现。
type fakeMCPStore struct {
	mu      sync.Mutex
	nextID  int64
	entries map[int64]*mcpEntry
	calls   []mcpCallEntry
}

func newFakeMCPStore() *fakeMCPStore {
	return &fakeMCPStore{entries: map[int64]*mcpEntry{}}
}

func (s *fakeMCPStore) ListMCPServers(_ context.Context, classID int64) ([]tutorpkg.MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []tutorpkg.MCPServer
	for _, e := range s.entries {
		if e.classID == classID {
			out = append(out, e.server)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeMCPStore) find(classID, id int64) (*mcpEntry, bool) {
	e, ok := s.entries[id]
	if !ok || e.classID != classID {
		return nil, false
	}
	return e, true
}

func (s *fakeMCPStore) GetMCPServer(_ context.Context, classID, id int64) (*tutorpkg.MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.find(classID, id)
	if !ok {
		return nil, tutorpkg.ErrMCPServerNotFound
	}
	cp := e.server
	return &cp, nil
}

func (s *fakeMCPStore) GetMCPServerByName(_ context.Context, classID int64, name string) (*tutorpkg.MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.classID == classID && e.server.Name == name {
			cp := e.server
			return &cp, nil
		}
	}
	return nil, tutorpkg.ErrMCPServerNotFound
}

func (s *fakeMCPStore) nameTaken(classID int64, name string, excludeID int64) bool {
	for id, e := range s.entries {
		if e.classID == classID && e.server.Name == name && id != excludeID {
			return true
		}
	}
	return false
}

func (s *fakeMCPStore) CreateMCPServer(_ context.Context, classID int64, name, baseURL, token string) (*tutorpkg.MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameTaken(classID, name, 0) {
		return nil, tutorpkg.ErrMCPServerNameConflict
	}
	s.nextID++
	srv := tutorpkg.MCPServer{
		ID: s.nextID, Name: name, BaseURL: baseURL,
		HasAuth: token != "", Connected: false, Tools: []mcp.Tool{}, AuthToken: token,
	}
	s.entries[s.nextID] = &mcpEntry{server: srv, classID: classID}
	cp := srv
	return &cp, nil
}

func (s *fakeMCPStore) UpdateMCPServer(_ context.Context, classID, id int64, name, baseURL string, token *string) (*tutorpkg.MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.find(classID, id)
	if !ok {
		return nil, tutorpkg.ErrMCPServerNotFound
	}
	if s.nameTaken(classID, name, id) {
		return nil, tutorpkg.ErrMCPServerNameConflict
	}
	e.server.Name = name
	e.server.BaseURL = baseURL
	if token != nil {
		e.server.AuthToken = *token
		e.server.HasAuth = *token != ""
	}
	e.server.Connected = false
	e.server.Tools = []mcp.Tool{}
	e.server.LastError = ""
	cp := e.server
	return &cp, nil
}

func (s *fakeMCPStore) DeleteMCPServer(_ context.Context, classID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.find(classID, id); !ok {
		return tutorpkg.ErrMCPServerNotFound
	}
	delete(s.entries, id)
	return nil
}

func (s *fakeMCPStore) MarkMCPServerConnected(_ context.Context, classID, id int64, tools []mcp.Tool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.find(classID, id)
	if !ok {
		return tutorpkg.ErrMCPServerNotFound
	}
	if len(tools) > tutorpkg.MCPToolCacheLimit {
		tools = tools[:tutorpkg.MCPToolCacheLimit]
	}
	if tools == nil {
		tools = []mcp.Tool{}
	}
	e.server.Connected = true
	e.server.Tools = tools
	e.server.LastError = ""
	return nil
}

func (s *fakeMCPStore) MarkMCPServerConnectFailed(_ context.Context, classID, id int64, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.find(classID, id)
	if !ok {
		return tutorpkg.ErrMCPServerNotFound
	}
	e.server.Connected = false
	e.server.Tools = []mcp.Tool{}
	e.server.LastError = lastError
	return nil
}

func (s *fakeMCPStore) DisconnectMCPServer(_ context.Context, classID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.find(classID, id)
	if !ok {
		return tutorpkg.ErrMCPServerNotFound
	}
	e.server.Connected = false
	e.server.Tools = []mcp.Tool{}
	e.server.LastError = ""
	return nil
}

func (s *fakeMCPStore) RecentMCPCalls(_ context.Context, classID int64, limit int) ([]tutorpkg.MCPCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []tutorpkg.MCPCall{}
	for i := len(s.calls) - 1; i >= 0 && len(out) < limit; i-- {
		if s.calls[i].classID == classID {
			out = append(out, s.calls[i].call)
		}
	}
	return out, nil
}

// server 返回指定 id 的副本（测试断言数据库侧状态用）。
func (s *fakeMCPStore) server(id int64) (tutorpkg.MCPServer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return tutorpkg.MCPServer{}, false
	}
	return e.server, true
}

// recordCall 直接写入一条审计（测试准备数据）。
func (s *fakeMCPStore) recordCall(classID int64, c tutorpkg.MCPCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, mcpCallEntry{classID: classID, call: c})
}

// fakeProbe 是连接探测替身。
type fakeProbe struct {
	initErr error
	listErr error
	tools   []mcp.Tool
}

func (p *fakeProbe) Initialize(context.Context) (*mcp.ServerInfo, error) {
	if p.initErr != nil {
		return nil, p.initErr
	}
	return &mcp.ServerInfo{ProtocolVersion: "2024-11-05", Name: "campus-tools", Version: "1.0.0"}, nil
}

func (p *fakeProbe) ListTools(context.Context) ([]mcp.Tool, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	return p.tools, nil
}

// dialRecorder 记录连接时使用的 base_url 与令牌，并按需返回探测结果。
type dialRecorder struct {
	mu      sync.Mutex
	probe   *fakeProbe
	baseURL string
	token   string
	timeout time.Duration
	calls   int
}

func (d *dialRecorder) dial(baseURL, token string, timeout time.Duration) MCPProbe {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseURL = baseURL
	d.token = token
	d.timeout = timeout
	d.calls++
	return d.probe
}

// ---------- 装配 ----------

// newMCPTestAPI 装配带 MCP 依赖的 API；TutorStore 复用既有 srvTutorStore。
func newMCPTestAPI(store *fakeMCPStore, dial MCPDialer) (*API, *srvTutorStore) {
	tstore := newSrvTutorStore()
	a, _ := newTutorTestAPI(tstore, &srvFakeStreamLLM{}, nil, nil)
	a.MCPStore = store
	a.MCPDial = dial
	a.MCPTimeout = time.Second
	return a, tstore
}

// toolNames 返回工具清单的名字序列。
func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// ---------- 5.1 鉴权与角色 ----------

func TestMCPRoutesRequireAuth(t *testing.T) {
	api, _ := newMCPTestAPI(newFakeMCPStore(), nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/tutor/mcp/servers"},
		{http.MethodPost, "/api/tutor/mcp/servers"},
		{http.MethodPut, "/api/tutor/mcp/servers/1"},
		{http.MethodDelete, "/api/tutor/mcp/servers/1"},
		{http.MethodPost, "/api/tutor/mcp/servers/1/connect"},
		{http.MethodPost, "/api/tutor/mcp/servers/1/disconnect"},
		{http.MethodGet, "/api/tutor/mcp/calls"},
	} {
		req, _ := http.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录期望 401，得到 %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestMCPRoutesStudentForbidden(t *testing.T) {
	api, _ := newMCPTestAPI(newFakeMCPStore(), nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/tutor/mcp/servers"},
		{http.MethodPost, "/api/tutor/mcp/servers"},
		{http.MethodPut, "/api/tutor/mcp/servers/1"},
		{http.MethodDelete, "/api/tutor/mcp/servers/1"},
		{http.MethodPost, "/api/tutor/mcp/servers/1/connect"},
		{http.MethodPost, "/api/tutor/mcp/servers/1/disconnect"},
		{http.MethodGet, "/api/tutor/mcp/calls"},
	} {
		req := signedReq(t, api, testUser("studentA1"), tc.method, tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("学生 %s %s 期望 403，得到 %d", tc.method, tc.path, rec.Code)
		}
	}
}

// ---------- 5.2 登记/修改只回 hasAuth，令牌永不回传 ----------

func TestMCPRegisterOnlyHasAuth(t *testing.T) {
	store := newFakeMCPStore()
	api, _ := newMCPTestAPI(store, nil)

	body := `{"name":"campus-tools","baseUrl":"http://mcp-demo:8090","authToken":"s3cr3t-token"}`
	req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/mcp/servers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("登记期望 201，得到 %d: %s", rec.Code, rec.Body)
	}
	var created mcpServerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || !created.HasAuth || created.Connected || created.Name != "campus-tools" {
		t.Fatalf("登记响应字段错误: %+v", created)
	}
	assertNoTokenLeak(t, rec.Body.String(), "s3cr3t-token")

	// 令牌只在服务端保存。
	srv, ok := store.server(created.ID)
	if !ok || srv.AuthToken != "s3cr3t-token" {
		t.Fatalf("服务端应保存令牌: %+v", srv)
	}

	// 列表同样只回 hasAuth。
	req = signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/mcp/servers", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表期望 200，得到 %d", rec.Code)
	}
	assertNoTokenLeak(t, rec.Body.String(), "s3cr3t-token")
	var list []mcpServerResponse
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || !list[0].HasAuth {
		t.Fatalf("列表错误: %+v", list)
	}
}

// assertNoTokenLeak 断言响应体不含令牌键与令牌原文。
func assertNoTokenLeak(t *testing.T, body, token string) {
	t.Helper()
	if strings.Contains(body, token) {
		t.Fatalf("响应体泄露令牌原文: %s", body)
	}
	for _, key := range []string{`"authToken"`, `"token"`, `"auth_token"`} {
		if strings.Contains(body, key) {
			t.Fatalf("响应体不得含令牌键 %s: %s", key, body)
		}
	}
}

func TestMCPRegisterDuplicate400(t *testing.T) {
	store := newFakeMCPStore()
	api, _ := newMCPTestAPI(store, nil)
	body := `{"name":"campus-tools","baseUrl":"http://mcp-demo:8090","authToken":"t"}`

	req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/mcp/servers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("首次登记期望 201，得到 %d", rec.Code)
	}
	req = signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/mcp/servers", strings.NewReader(body))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重名期望 400，得到 %d", rec.Code)
	}
}

func TestMCPRegisterValidation400(t *testing.T) {
	api, _ := newMCPTestAPI(newFakeMCPStore(), nil)
	for _, tc := range []struct{ name, body string }{
		{"空名称", `{"name":"  ","baseUrl":"http://x"}`},
		{"非法名称", `{"name":"Campus Tools","baseUrl":"http://x"}`},
		{"空地址", `{"name":"ok","baseUrl":"  "}`},
		{"非 http 地址", `{"name":"ok","baseUrl":"ftp://x"}`},
		{"缺 host", `{"name":"ok","baseUrl":"http://"}`},
	} {
		req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/mcp/servers", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，得到 %d", tc.name, rec.Code)
		}
	}
}

// ---------- 5.1 跨班/不存在 404 ----------

func TestMCPCrossClassAndMissing404(t *testing.T) {
	store := newFakeMCPStore()
	// 直接在本班（class=2）写入一台，教师A（class=1）不可见。
	other, err := store.CreateMCPServer(context.Background(), 2, "other-tools", "http://x:1", "")
	if err != nil {
		t.Fatal(err)
	}
	api, _ := newMCPTestAPI(store, (&dialRecorder{probe: &fakeProbe{}}).dial)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, fmt.Sprintf("/api/tutor/mcp/servers/%d", other.ID), `{"name":"n","baseUrl":"http://x"}`},
		{http.MethodDelete, fmt.Sprintf("/api/tutor/mcp/servers/%d", other.ID), ``},
		{http.MethodPost, fmt.Sprintf("/api/tutor/mcp/servers/%d/connect", other.ID), ``},
		{http.MethodPost, fmt.Sprintf("/api/tutor/mcp/servers/%d/disconnect", other.ID), ``},
		{http.MethodPut, "/api/tutor/mcp/servers/999", `{"name":"n","baseUrl":"http://x"}`},
		{http.MethodPut, "/api/tutor/mcp/servers/abc", `{"name":"n","baseUrl":"http://x"}`},
	} {
		req := signedReq(t, api, testUser("teacherA"), tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s 期望 404，得到 %d", tc.method, tc.path, rec.Code)
		}
	}
}

// ---------- 5.3 连接成功/失败 ----------

func TestMCPConnectSuccessAndDisconnect(t *testing.T) {
	store := newFakeMCPStore()
	probe := &fakeProbe{tools: []mcp.Tool{{Name: "formula_hint"}, {Name: "unit_convert"}}}
	dial := &dialRecorder{probe: probe}
	api, _ := newMCPTestAPI(store, dial.dial)

	created, _ := store.CreateMCPServer(context.Background(), 1, "campus-tools", "http://mcp-demo:8090", "s3cr3t-token")

	req := signedReq(t, api, testUser("teacherA"), http.MethodPost,
		fmt.Sprintf("/api/tutor/mcp/servers/%d/connect", created.ID), nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("连接期望 200，得到 %d: %s", rec.Code, rec.Body)
	}
	var connected mcpServerResponse
	json.Unmarshal(rec.Body.Bytes(), &connected)
	if !connected.Connected || len(connected.Tools) != 2 {
		t.Fatalf("连接响应错误: %+v", connected)
	}
	if got := toolNames(connected.Tools); got[0] != "formula_hint" || got[1] != "unit_convert" {
		t.Fatalf("工具清单错误: %v", got)
	}
	// 连接使用登记的地址与令牌，令牌不回传。
	if dial.baseURL != "http://mcp-demo:8090" || dial.token != "s3cr3t-token" {
		t.Fatalf("连接参数错误: baseURL=%s token=%s", dial.baseURL, dial.token)
	}
	assertNoTokenLeak(t, rec.Body.String(), "s3cr3t-token")

	// 断开清空清单。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPost,
		fmt.Sprintf("/api/tutor/mcp/servers/%d/disconnect", created.ID), nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("断开期望 200，得到 %d", rec.Code)
	}
	var off mcpServerResponse
	json.Unmarshal(rec.Body.Bytes(), &off)
	if off.Connected || len(off.Tools) != 0 {
		t.Fatalf("断开后应未连接且清单为空: %+v", off)
	}
}

func TestMCPConnectFailure502(t *testing.T) {
	store := newFakeMCPStore()
	dial := &dialRecorder{probe: &fakeProbe{initErr: fmt.Errorf("dial tcp: connection refused")}}
	api, _ := newMCPTestAPI(store, dial.dial)

	created, _ := store.CreateMCPServer(context.Background(), 1, "campus-tools", "http://unreachable:9", "t")
	req := signedReq(t, api, testUser("teacherA"), http.MethodPost,
		fmt.Sprintf("/api/tutor/mcp/servers/%d/connect", created.ID), nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("连接失败期望 502，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "连接 MCP 服务器失败") {
		t.Fatalf("502 文案应可读: %s", rec.Body)
	}
	// 数据库状态：保持未连接、清单为空，并记录失败摘要。
	srv, _ := store.server(created.ID)
	if srv.Connected || len(srv.Tools) != 0 || srv.LastError == "" {
		t.Fatalf("失败后状态错误: %+v", srv)
	}
	// 不得泄露内部地址与令牌。
	if strings.Contains(rec.Body.String(), "unreachable:9") {
		t.Fatalf("响应不得泄露内部地址: %s", rec.Body)
	}
}

// 工具发现失败同样 502 且保持未连接。
func TestMCPConnectListToolsFailure502(t *testing.T) {
	store := newFakeMCPStore()
	dial := &dialRecorder{probe: &fakeProbe{listErr: fmt.Errorf("tools/list 失败")}}
	api, _ := newMCPTestAPI(store, dial.dial)
	created, _ := store.CreateMCPServer(context.Background(), 1, "campus-tools", "http://x:1", "")
	req := signedReq(t, api, testUser("teacherA"), http.MethodPost,
		fmt.Sprintf("/api/tutor/mcp/servers/%d/connect", created.ID), nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("工具发现失败期望 502，得到 %d", rec.Code)
	}
	srv, _ := store.server(created.ID)
	if srv.Connected {
		t.Fatal("失败后必须保持未连接")
	}
}

// ---------- 5.4 技能保存引用校验 ----------

func TestSkillToolRefValidation(t *testing.T) {
	newConnectedAPI := func(tools ...string) (*API, *fakeMCPStore) {
		store := newFakeMCPStore()
		api, _ := newMCPTestAPI(store, nil)
		srv, _ := store.CreateMCPServer(context.Background(), 1, "campus-tools", "http://mcp-demo:8090", "t")
		ts := make([]mcp.Tool, 0, len(tools))
		for _, n := range tools {
			ts = append(ts, mcp.Tool{Name: n})
		}
		_ = store.MarkMCPServerConnected(context.Background(), 1, srv.ID, ts)
		return api, store
	}

	post := func(t *testing.T, api *API, content string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": "技能", "content": content})
		req := signedReq(t, api, testUser("teacherA"), http.MethodPost,
			"/api/tutor/assistant/skills", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		return rec
	}

	t.Run("合法引用通过", func(t *testing.T) {
		api, _ := newConnectedAPI("formula_hint", "unit_convert")
		if rec := post(t, api, "工具：formula_hint@campus-tools, unit_convert@campus-tools"); rec.Code != http.StatusCreated {
			t.Fatalf("合法引用期望 201，得到 %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("内置引用直接通过", func(t *testing.T) {
		api, _ := newConnectedAPI()
		if rec := post(t, api, "工具：knowledge_search"); rec.Code != http.StatusCreated {
			t.Fatalf("内置引用期望 201，得到 %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("工具不在清单", func(t *testing.T) {
		api, _ := newConnectedAPI("unit_convert")
		rec := post(t, api, "工具：formula_hint@campus-tools")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("期望 400，得到 %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "formula_hint") {
			t.Fatalf("拒绝原因须指明具体引用: %s", rec.Body)
		}
	})

	t.Run("服务器未连接", func(t *testing.T) {
		store := newFakeMCPStore()
		api, _ := newMCPTestAPI(store, nil)
		store.CreateMCPServer(context.Background(), 1, "campus-tools", "http://mcp-demo:8090", "t")
		rec := post(t, api, "工具：formula_hint@campus-tools")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "尚未连接") {
			t.Fatalf("未连接应 400 且提示未连接，得到 %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("服务器不存在", func(t *testing.T) {
		api, _ := newConnectedAPI("formula_hint")
		rec := post(t, api, "工具：formula_hint@ghost-tools")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ghost-tools") {
			t.Fatalf("不存在应 400 且指明服务器，得到 %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("非法引用语法", func(t *testing.T) {
		api, _ := newConnectedAPI("formula_hint")
		rec := post(t, api, "工具：formula-hint@campus-tools")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("非法语法期望 400，得到 %d", rec.Code)
		}
	})

	// 上传 md 路径共用同一校验。
	t.Run("上传路径非法引用 400", func(t *testing.T) {
		api, _ := newConnectedAPI("unit_convert")
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "技能.md")
		fw.Write([]byte("工具：formula_hint@campus-tools"))
		mw.Close()
		req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/assistant/skills/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "formula_hint") {
			t.Fatalf("上传路径应 400 并指明引用，得到 %d: %s", rec.Code, rec.Body)
		}
	})
}

// ---------- 修改语义：保留令牌 ----------

func TestMCPUpdateKeepsTokenWhenOmitted(t *testing.T) {
	store := newFakeMCPStore()
	api, _ := newMCPTestAPI(store, nil)
	created, _ := store.CreateMCPServer(context.Background(), 1, "campus-tools", "http://old:1", "keep-token")

	req := signedReq(t, api, testUser("teacherA"), http.MethodPut,
		fmt.Sprintf("/api/tutor/mcp/servers/%d", created.ID),
		strings.NewReader(`{"name":"campus-tools","baseUrl":"http://new:2"}`))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("修改期望 200，得到 %d: %s", rec.Code, rec.Body)
	}
	var got mcpServerResponse
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.HasAuth {
		t.Fatal("未传令牌时应保留原令牌")
	}
	srv, _ := store.server(created.ID)
	if srv.AuthToken != "keep-token" || srv.BaseURL != "http://new:2" || srv.Connected {
		t.Fatalf("修改后状态错误: %+v", srv)
	}
	assertNoTokenLeak(t, rec.Body.String(), "keep-token")
}

// ---------- 审计查询按本班隔离 ----------

func TestMCPCallsClassScoped(t *testing.T) {
	store := newFakeMCPStore()
	api, _ := newMCPTestAPI(store, nil)
	store.recordCall(1, tutorpkg.MCPCall{ID: 2, ServerName: "campus-tools", ToolRef: "formula_hint@campus-tools", Status: "success"})
	store.recordCall(2, tutorpkg.MCPCall{ID: 3, ServerName: "other", ToolRef: "x@other", Status: "denied"})

	req := signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/mcp/calls", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", rec.Code)
	}
	var got []tutorpkg.MCPCall
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("只应见本班记录: %+v", got)
	}
}
