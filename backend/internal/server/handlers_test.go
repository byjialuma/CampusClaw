package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"campusclaw/internal/model"
	"campusclaw/internal/store"
)

// ---------- 测试替身 ----------

type fakeUsers struct {
	users    map[string]*model.User
	password map[string]string
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{
		users: map[string]*model.User{
			"teacherA":  {ID: 1, Username: "teacherA", DisplayName: "教师A", Role: model.RoleTeacher, ClassID: 1, ClassName: "A班"},
			"studentA1": {ID: 2, Username: "studentA1", DisplayName: "学生A1", Role: model.RoleStudent, ClassID: 1, ClassName: "A班"},
			"studentB1": {ID: 3, Username: "studentB1", DisplayName: "学生B1", Role: model.RoleStudent, ClassID: 2, ClassName: "B班"},
		},
		password: map[string]string{
			"teacherA":  "ta-pw",
			"studentA1": "a1-pw",
			"studentB1": "b1-pw",
		},
	}
}

func (f *fakeUsers) Authenticate(_ context.Context, username, password string) (*model.User, error) {
	u, ok := f.users[username]
	if !ok || f.password[username] != password {
		return nil, errors.New("用户名或密码错误")
	}
	cp := *u
	return &cp, nil
}

// fakeJWT 是内存中的 JWT 测试替身。
type fakeJWT struct {
	tokens map[string]*model.User
	mu     sync.Mutex
}

func newFakeJWT() *fakeJWT {
	return &fakeJWT{tokens: map[string]*model.User{}}
}

func (f *fakeJWT) Sign(user *model.User) (string, time.Time, error) {
	tok := "jwt-" + user.Username
	cp := *user
	f.mu.Lock()
	f.tokens[tok] = &cp
	f.mu.Unlock()
	return tok, time.Now().Add(time.Hour), nil
}

func (f *fakeJWT) Verify(token string) (*model.User, error) {
	f.mu.Lock()
	u, ok := f.tokens[token]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("token 无效")
	}
	cp := *u
	return &cp, nil
}

type fakeMaterials struct {
	items map[int64]*model.Material
	data  map[int64][]byte
	maxID int64
}

func newFakeMaterials() *fakeMaterials {
	m := &fakeMaterials{items: map[int64]*model.Material{}, data: map[int64][]byte{}}
	m.add(1, "A班种子讲义.txt", "txt", []byte("A 班内容"), 1)
	m.add(2, "B班种子讲义.txt", "txt", []byte("B 班内容"), 2)
	m.add(3, "A班课件.pdf", "pdf", []byte("%PDF-1.7 fake"), 1)
	return m
}

func (f *fakeMaterials) add(id int64, name, ftype string, data []byte, classID int64) {
	f.items[id] = &model.Material{
		ID: id, ClassID: classID, OriginalName: name, StoredPath: name,
		FileType: ftype, SizeBytes: int64(len(data)), UploaderName: "教师A",
		CreatedAt: time.Date(2026, 9, 23, 8, 0, 0, 0, time.Local),
	}
	f.data[id] = data
	if id > f.maxID {
		f.maxID = id
	}
}

func (f *fakeMaterials) ListByClass(_ context.Context, classID int64) ([]model.Material, error) {
	var out []model.Material
	for _, m := range f.items {
		if m.ClassID == classID {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (f *fakeMaterials) GetByID(_ context.Context, id int64) (*model.Material, error) {
	if m, ok := f.items[id]; ok {
		cp := *m
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

// SaveAndCreate 复刻真实仓储的关键安全行为：先校验，且班级/上传者只信参数。
func (f *fakeMaterials) SaveAndCreate(_ context.Context, classID, uploaderID int64,
	originalName string, data []byte) (*model.Material, error) {
	ftype, err := store.Validate(originalName, data)
	if err != nil {
		return nil, err
	}
	f.maxID++
	id := f.maxID
	m := &model.Material{
		ID: id, ClassID: classID, UploaderUserID: &uploaderID, OriginalName: originalName,
		StoredPath: "stored/" + originalName, FileType: ftype, SizeBytes: int64(len(data)),
		UploaderName: "教师A", CreatedAt: time.Now(),
	}
	f.items[id] = m
	f.data[id] = data
	cp := *m
	return &cp, nil
}

func (f *fakeMaterials) Open(m *model.Material) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data[m.ID])), nil
}

type fakeTickets struct {
	tickets map[string]*model.Ticket
}

func (f *fakeTickets) Create(_ context.Context, handoutID, userID, classID int64) (string, error) {
	tok := "ticket-" + url.QueryEscape(time.Now().Format("150405.000000")) + "-" + strconv.FormatInt(handoutID, 10)
	f.tickets[tok] = &model.Ticket{Token: tok, HandoutID: handoutID, UserID: userID, ClassID: classID,
		ExpiresAt: time.Now().Add(time.Minute)}
	return tok, nil
}

func (f *fakeTickets) Consume(_ context.Context, token string) (*model.Ticket, error) {
	t, ok := f.tickets[token]
	if !ok || t.UsedAt != nil || time.Now().After(t.ExpiresAt) {
		return nil, store.ErrTicketInvalid
	}
	now := time.Now()
	t.UsedAt = &now
	cp := *t
	return &cp, nil
}

func (f *fakeTickets) DeleteExpired(_ context.Context) error { return nil }

// fakeIndexQueue 记录入队的 handout_id，可注入故障。
type fakeIndexQueue struct {
	mu       sync.Mutex
	enqueued []int64
	err      error
}

func (q *fakeIndexQueue) Enqueue(_ context.Context, id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued = append(q.enqueued, id)
	return q.err
}

func (q *fakeIndexQueue) ids() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int64(nil), q.enqueued...)
}

// ---------- 测试辅助 ----------

func newTestAPI() *API {
	return &API{
		Users:     newFakeUsers(),
		JWT:       newFakeJWT(),
		Materials: newFakeMaterials(),
		Tickets:   &fakeTickets{tickets: map[string]*model.Ticket{}},
		DB:        &fakePinger{},
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *API) {
	t.Helper()
	a := newTestAPI()
	srv := httptest.NewServer(a.NewHandler())
	t.Cleanup(srv.Close)
	return srv, a
}

func loginResponse(t *testing.T, base, username, password string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(base+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	return resp
}

func loginJarClient(t *testing.T, base, username, password string) *http.Client {
	t.Helper()
	// 登录获取 token
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(base+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录失败: %d", resp.StatusCode)
	}
	var loginResp struct {
		AccessToken string `json:"accessToken"`
	}
	json.NewDecoder(resp.Body).Decode(&loginResp)
	resp.Body.Close()
	if loginResp.AccessToken == "" {
		t.Fatal("登录响应缺少 accessToken")
	}
	// 使用 Bearer token 的 client
	return &http.Client{
		Transport: &bearerTransport{token: loginResp.AccessToken},
	}
}

// bearerTransport 自动为每个请求注入 Authorization: Bearer 头。
type bearerTransport struct {
	token string
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

func uploadRequest(t *testing.T, fieldName, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	fw, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	return buf, mw.FormDataContentType()
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---------- 健康检查 ----------

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DB 正常时期望 200，得到 %d", resp.StatusCode)
	}
	var ok map[string]string
	json.NewDecoder(resp.Body).Decode(&ok)
	resp.Body.Close()
	if ok["status"] != "ok" || ok["db"] != "up" {
		t.Fatalf("健康响应异常: %v", ok)
	}

	// DB 故障分支（直接用降级 API 起内存服务）。
	a := newTestAPI()
	a.DB = &fakePinger{err: errors.New("db down")}
	bad := httptest.NewServer(a.NewHandler())
	defer bad.Close()
	resp2, err := http.Get(bad.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("DB 故障时期望 503，得到 %d", resp2.StatusCode)
	}
}

// ---------- 登录/退出/会话 ----------

func TestLoginSuccessReturnsJWT(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := loginResponse(t, srv.URL, "teacherA", "ta-pw")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", resp.StatusCode)
	}
	// 不再返回会话 Cookie。
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			t.Fatal("JWT 认证不应返回会话 Cookie")
		}
	}
	var lr struct {
		User        meResponse `json:"user"`
		AccessToken string     `json:"accessToken"`
		ExpiresAt   string     `json:"expiresAt"`
	}
	json.NewDecoder(resp.Body).Decode(&lr)
	if lr.AccessToken == "" {
		t.Fatal("登录响应必须包含 accessToken")
	}
	if lr.ExpiresAt == "" {
		t.Fatal("登录响应必须包含 expiresAt")
	}
	if lr.User.Role != "teacher" || lr.User.ClassName != "A班" || lr.User.DisplayName != "教师A" {
		t.Fatalf("登录返回的身份信息错误: %+v", lr.User)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	srv, _ := newTestServer(t)

	r1 := loginResponse(t, srv.URL, "studentA1", "wrong")
	b1, _ := io.ReadAll(r1.Body)
	r1.Body.Close()

	r2 := loginResponse(t, srv.URL, "ghost", "whatever")
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()

	if r1.StatusCode != http.StatusUnauthorized || r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("两种失败都应为 401，得到 %d/%d", r1.StatusCode, r2.StatusCode)
	}
	if string(b1) != string(b2) {
		t.Fatalf("错误密码与用户不存在的响应体必须一致:\n%s\n%s", b1, b2)
	}
	if !strings.Contains(string(b1), "用户名或密码错误") {
		t.Fatalf("应返回统一文案，实际 %s", b1)
	}
}

func TestProtectedEndpointsRequireToken(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/me"},
		{http.MethodGet, "/api/search?q=x"},
		{http.MethodGet, "/api/materials"},
		{http.MethodGet, "/api/materials/1/content"},
		{http.MethodPost, "/api/materials/1/download-ticket"},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s 无 token 时期望 401，得到 %d", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestProtectedEndpointsRejectInvalidToken(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无效 token 期望 401，得到 %d", resp.StatusCode)
	}
}

func TestLogoutReturns204(t *testing.T) {
	srv, _ := newTestServer(t)
	client := loginJarClient(t, srv.URL, "studentA1", "a1-pw")

	if resp, _ := client.Get(srv.URL + "/api/me"); resp.StatusCode != http.StatusOK {
		t.Fatalf("登录后 /api/me 应 200，得到 %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("退出时期望 204，得到 %d", resp.StatusCode)
	}
	// JWT 无状态：退出后 token 仍有效（客户端负责丢弃）。
	if resp2, _ := client.Get(srv.URL + "/api/me"); resp2.StatusCode != http.StatusOK {
		t.Fatalf("JWT 无状态，退出后 token 仍应有效，得到 %d", resp2.StatusCode)
	}
}

// ---------- 角色与班级隔离 ----------

func TestStudentUploadForbidden(t *testing.T) {
	srv, api := newTestServer(t)
	fm := api.Materials.(*fakeMaterials)
	before := len(fm.items)

	client := loginJarClient(t, srv.URL, "studentA1", "a1-pw")
	buf, ct := uploadRequest(t, "file", "第一章.txt", []byte("学生想上传"))
	resp, err := client.Post(srv.URL+"/api/materials", ct, buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("学生上传必须 403，得到 %d", resp.StatusCode)
	}
	body := readAll(t, resp.Body)
	if !strings.Contains(string(body), "学生无权限上传文件") {
		t.Fatalf("403 文案必须是“学生无权限上传文件”，实际: %s", body)
	}
	if len(fm.items) != before {
		t.Fatal("被拒绝的上传不得产生材料记录")
	}
}

func TestListIsFilteredBySessionClass(t *testing.T) {
	srv, _ := newTestServer(t)
	client := loginJarClient(t, srv.URL, "studentA1", "a1-pw")
	resp, err := client.Get(srv.URL + "/api/materials")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Materials []model.Material `json:"materials"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Materials) != 2 {
		t.Fatalf("A 班应只见 2 条材料，得到 %d", len(got.Materials))
	}
	for _, m := range got.Materials {
		if strings.Contains(m.OriginalName, "B班") {
			t.Fatalf("列表不得出现 B 班材料: %s", m.OriginalName)
		}
	}
}

func TestContentCrossClassAndNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	// 同班 200，Content-Type 与内容正确。
	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")
	resp, _ := a1.Get(srv.URL + "/api/materials/1/content")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("同班内容期望 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("txt Content-Type 错误: %s", ct)
	}
	if string(readAll(t, resp.Body)) != "A 班内容" {
		t.Fatal("内容与文件不符")
	}
	resp.Body.Close()

	resp, _ = a1.Get(srv.URL + "/api/materials/3/content")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/pdf") {
		t.Fatalf("pdf Content-Type 错误: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()

	// 跨班 403。
	resp, _ = a1.Get(srv.URL + "/api/materials/2/content")
	body := readAll(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨班访问期望 403，得到 %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "无权访问该班级") {
		t.Fatalf("跨班文案错误: %s", body)
	}

	// 不存在 404。
	resp, _ = a1.Get(srv.URL + "/api/materials/999/content")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在时期望 404，得到 %d", resp.StatusCode)
	}

	// B 班学生反取 A 班材料同样 403。
	b1 := loginJarClient(t, srv.URL, "studentB1", "b1-pw")
	respB, err := b1.Get(srv.URL + "/api/materials/1/content")
	if err != nil {
		t.Fatal(err)
	}
	respB.Body.Close()
	if respB.StatusCode != http.StatusForbidden {
		t.Fatalf("B 班取 A 班材料期望 403，得到 %d", respB.StatusCode)
	}
}

// ---------- 教师上传与上传后可查 ----------

func TestTeacherUploadValidationAndList(t *testing.T) {
	srv, _ := newTestServer(t)
	client := loginJarClient(t, srv.URL, "teacherA", "ta-pw")

	// 非法类型 400。
	buf, ct := uploadRequest(t, "file", "教案.docx", []byte("word"))
	resp, err := client.Post(srv.URL+"/api/materials", ct, buf)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("docx 期望 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 伪造 PDF 400。
	buf, ct = uploadRequest(t, "file", "fake.pdf", []byte("not pdf"))
	resp, _ = client.Post(srv.URL+"/api/materials", ct, buf)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("伪造 pdf 期望 400，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 合法 txt 201。
	name := "教师新讲义.txt"
	buf, ct = uploadRequest(t, "file", name, []byte("新讲义正文"))
	resp, _ = client.Post(srv.URL+"/api/materials", ct, buf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("合法上传期望 201，得到 %d", resp.StatusCode)
	}
	var created model.Material
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.OriginalName != name {
		t.Fatalf("记录名称应等于原始文件名，得到 %q", created.OriginalName)
	}

	// 本班列表立即出现；B 班列表不出现。
	resp, _ = client.Get(srv.URL + "/api/materials")
	var list struct {
		Materials []model.Material `json:"materials"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	found := false
	for _, m := range list.Materials {
		if m.OriginalName == name {
			found = true
		}
	}
	if !found {
		t.Fatal("上传后本班材料列表必须能查到该记录")
	}

	b1 := loginJarClient(t, srv.URL, "studentB1", "b1-pw")
	resp, _ = b1.Get(srv.URL + "/api/materials")
	var listB struct {
		Materials []model.Material `json:"materials"`
	}
	json.NewDecoder(resp.Body).Decode(&listB)
	resp.Body.Close()
	for _, m := range listB.Materials {
		if m.OriginalName == name {
			t.Fatal("外班列表不得出现新上传材料")
		}
	}
}

// 上传成功后必须把新材料异步入索引队列；非法文件与入队故障都不影响 201/4xx 语义。
func TestTeacherUploadEnqueuesIndex(t *testing.T) {
	api := newTestAPI()
	queue := &fakeIndexQueue{}
	api.Indexer = queue
	srv := httptest.NewServer(api.NewHandler())
	t.Cleanup(srv.Close)
	client := loginJarClient(t, srv.URL, "teacherA", "ta-pw")

	// 非法上传不入队。
	buf, ct := uploadRequest(t, "file", "bad.docx", []byte("x"))
	resp, _ := client.Post(srv.URL+"/api/materials", ct, buf)
	resp.Body.Close()
	if len(queue.ids()) != 0 {
		t.Fatal("非法文件不得入索引队列")
	}

	// 合法上传 201 且入队一次，ID 与返回材料一致。
	buf, ct = uploadRequest(t, "file", "可检索讲义.txt", []byte("集合的确定性内容"))
	resp, _ = client.Post(srv.URL+"/api/materials", ct, buf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("期望 201，得到 %d", resp.StatusCode)
	}
	var created model.Material
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	ids := queue.ids()
	if len(ids) != 1 || ids[0] != created.ID {
		t.Fatalf("应恰好入队新材料 %d，实际 %v", created.ID, ids)
	}

	// 入队本身失败（如队列满）不得拖垮上传响应。
	queue.err = errors.New("queue full")
	buf, ct = uploadRequest(t, "file", "第二份.txt", []byte("更多内容"))
	resp, _ = client.Post(srv.URL+"/api/materials", ct, buf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("入队故障时上传仍应 201，得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ---------- 一次性票据下载 ----------

func TestDownloadTicketFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	a1 := loginJarClient(t, srv.URL, "studentA1", "a1-pw")

	// 跨班申请票据 403。
	resp, _ := a1.Post(srv.URL+"/api/materials/2/download-ticket", "application/json", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨班申请票据期望 403，得到 %d", resp.StatusCode)
	}

	// 同班申请成功。
	resp, _ = a1.Post(srv.URL+"/api/materials/1/download-ticket", "application/json", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("申请票据期望 200，得到 %d", resp.StatusCode)
	}
	var tr struct{ Ticket, FileName string }
	json.NewDecoder(resp.Body).Decode(&tr)
	resp.Body.Close()
	if tr.Ticket == "" || tr.FileName != "A班种子讲义.txt" {
		t.Fatalf("票据响应错误: %+v", tr)
	}

	// 首次下载 200，文件名保真。
	resp, _ = http.Get(srv.URL + "/api/download?ticket=" + url.QueryEscape(tr.Ticket))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首次下载期望 200，得到 %d", resp.StatusCode)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, url.PathEscape(tr.FileName)) {
		t.Fatalf("Content-Disposition 缺少 attachment/编码文件名: %s", cd)
	}
	if string(readAll(t, resp.Body)) != "A 班内容" {
		t.Fatal("下载内容不符")
	}
	resp.Body.Close()

	// 第二次（模拟下载页刷新）410。
	resp, _ = http.Get(srv.URL + "/api/download?ticket=" + url.QueryEscape(tr.Ticket))
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("票据复用期望 410，得到 %d", resp.StatusCode)
	}

	// 无票据 401。
	resp, _ = http.Get(srv.URL + "/api/download")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("缺少票据期望 401，得到 %d", resp.StatusCode)
	}
}
