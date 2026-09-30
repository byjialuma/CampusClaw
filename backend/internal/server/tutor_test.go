package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"campusclaw/internal/llm"
	"campusclaw/internal/model"
	tutorpkg "campusclaw/internal/tutor"
	"campusclaw/internal/vector"
)

// ---------- 组件假件 ----------

type srvFakeEmbedder struct {
	vec []float32
	err error
}

func (f *srvFakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}
func (f *srvFakeEmbedder) Dim() int { return len(f.vec) }

type srvFakeVectors struct {
	hits []vector.Hit
	err  error
}

func (f *srvFakeVectors) Search(_ context.Context, _ []float32, _ int64, _ int) ([]vector.Hit, error) {
	return f.hits, f.err
}

type srvFakeStreamLLM struct {
	mu              sync.Mutex
	deltas          []string
	err             error
	blockAfterFirst bool
	firstDelta      chan struct{}
	gotMessages     []llm.Message
}

func (f *srvFakeStreamLLM) StreamAsk(ctx context.Context, msgs []llm.Message,
	onDelta func(string) error) (string, error) {
	f.mu.Lock()
	f.gotMessages = msgs
	f.mu.Unlock()

	var sb strings.Builder
	for i, d := range f.deltas {
		if err := ctx.Err(); err != nil {
			return sb.String(), err
		}
		sb.WriteString(d)
		if err := onDelta(d); err != nil {
			return sb.String(), err
		}
		if f.firstDelta != nil && i == 0 {
			close(f.firstDelta)
		}
		if f.blockAfterFirst && i == 0 {
			<-ctx.Done()
			return sb.String(), ctx.Err()
		}
	}
	return sb.String(), f.err
}

// storedMessage 记录一条已持久化消息。
type storedMessage struct {
	id int64
	m  tutorpkg.NewMessage
}

// srvTutorStore 同时满足 server.tutorStore 与 tutor.Repo 两个接口。
type srvTutorStore struct {
	mu       sync.Mutex
	convs    map[int64]*tutorpkg.Conversation
	messages map[int64][]storedMessage
	prompts  map[int64]string
	skills   map[int64][]tutorpkg.Skill
	nextConv int64
	nextMsg  int64
}

func newSrvTutorStore() *srvTutorStore {
	return &srvTutorStore{
		convs:    map[int64]*tutorpkg.Conversation{},
		messages: map[int64][]storedMessage{},
		prompts: map[int64]string{
			1: "你是一位严格的数学助教。",
			2: "你是B班数学助教。",
		},
		skills: map[int64][]tutorpkg.Skill{
			1: {{ID: 50, Name: "解题引导", Description: "引导", Enabled: true}},
			2: {{ID: 60, Name: "解题引导", Description: "引导", Enabled: true}},
		},
	}
}

func (s *srvTutorStore) CreateConversation(_ context.Context, classID, userID int64, title string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextConv++
	s.convs[s.nextConv] = &tutorpkg.Conversation{
		ID: s.nextConv, ClassID: classID, UserID: userID, Title: title, CreatedAt: time.Now(),
	}
	s.messages[s.nextConv] = nil
	return s.nextConv, nil
}

func (s *srvTutorStore) ConversationsByUser(_ context.Context, userID int64) ([]tutorpkg.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []tutorpkg.Conversation
	for _, c := range s.convs {
		if c.UserID == userID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *srvTutorStore) ConversationForUser(_ context.Context, convID, userID int64) (*tutorpkg.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[convID]
	if !ok || c.UserID != userID {
		return nil, tutorpkg.ErrConversationNotFound
	}
	cp := *c
	return &cp, nil
}

func (s *srvTutorStore) MessagesByConversation(_ context.Context, convID int64) ([]tutorpkg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []tutorpkg.Message{}
	for _, sm := range s.messages[convID] {
		out = append(out, tutorpkg.Message{
			ID: sm.id, ConversationID: convID,
			Role: sm.m.Role, Content: sm.m.Content, Citations: sm.m.Citations,
			CreatedAt: time.Now(),
		})
	}
	return out, nil
}

func (s *srvTutorStore) AssistantConfig(_ context.Context, classID int64) (*tutorpkg.AssistantConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt, ok := s.prompts[classID]
	if !ok {
		return nil, tutorpkg.ErrAssistantNotFound
	}
	return &tutorpkg.AssistantConfig{
		AssistantID: classID * 10, Name: "解题助手",
		SystemPrompt: prompt, Skills: s.skills[classID],
	}, nil
}

func (s *srvTutorStore) SavePrompt(_ context.Context, classID int64, prompt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.prompts[classID]; !ok {
		return tutorpkg.ErrAssistantNotFound
	}
	s.prompts[classID] = prompt
	return nil
}

func (s *srvTutorStore) SetSkillEnabled(_ context.Context, classID, skillID int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i, sk := range s.skills[classID] {
		if sk.ID == skillID {
			s.skills[classID][i].Enabled = enabled
			found = true
		}
	}
	if !found {
		return tutorpkg.ErrSkillNotFound
	}
	return nil
}

func (s *srvTutorStore) RenameConversationIfEmpty(_ context.Context, convID int64, question string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages[convID]) > 0 {
		return nil
	}
	c, ok := s.convs[convID]
	if !ok {
		return tutorpkg.ErrConversationNotFound
	}
	c.Title = tutorpkg.TruncateTitle(question)
	return nil
}

func (s *srvTutorStore) AppendMessage(_ context.Context, convID int64, m tutorpkg.NewMessage) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.convs[convID]; !ok {
		return 0, tutorpkg.ErrConversationNotFound
	}
	s.nextMsg++
	s.messages[convID] = append(s.messages[convID], storedMessage{id: s.nextMsg, m: m})
	return s.nextMsg, nil
}

func (s *srvTutorStore) convMessages(convID int64) []storedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storedMessage(nil), s.messages[convID]...)
}

// ---------- 装配辅助 ----------

func srvHits() []vector.Hit {
	return []vector.Hit{{
		ID:    "p1",
		Score: 0.91,
		Payload: vector.Payload{
			ClassID: 1, DocumentID: 100,
			FileName: "讲义.txt", FileType: "txt",
			Text:    "集合是具有某种特定性质的事物的总体",
			Locator: model.Locator{Kind: model.LocatorLines, StartLine: 3, EndLine: 5},
		},
	}}
}

func newTutorTestAPI(store *srvTutorStore, stream *srvFakeStreamLLM,
	hits []vector.Hit, embErr error) (*API, *srvFakeVectors) {
	a := newTestAPI()
	vecs := &srvFakeVectors{hits: hits}
	svc := &tutorpkg.Service{
		Repo:     store,
		Embedder: &srvFakeEmbedder{vec: []float32{0.1, 0.2}, err: embErr},
		Vectors:  vecs,
		LLM:      stream,
		TopK:     5,
	}
	a.Tutor = svc
	a.TutorStore = store
	return a, vecs
}

func testUser(name string) *model.User {
	cp := *newFakeUsers().users[name]
	return &cp
}

// signedReq 构造带 Bearer token 的请求。
func signedReq(t *testing.T, a *API, u *model.User, method, path string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := a.JWT.Sign(u)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

// ---------- SSE body 解析 ----------

type parsedEvent struct {
	event string
	data  string
}

func parseSSEBody(t *testing.T, body []byte) []parsedEvent {
	t.Helper()
	var out []parsedEvent
	for _, block := range strings.Split(string(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var ev, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		out = append(out, parsedEvent{event: ev, data: data})
	}
	return out
}

// ---------- 会话创建/列表 ----------

func TestTutorCreateConversation(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	req := signedReq(t, api, testUser("studentA1"), http.MethodPost,
		"/api/tutor/conversations", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201，得到 %d: %s", rec.Code, rec.Body)
	}
	var got createdConversationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.Title != newConversationTitle {
		t.Fatalf("响应错误: %+v", got)
	}
	c := store.convs[got.ID]
	if c.ClassID != 1 || c.UserID != 2 {
		t.Fatalf("会话归属必须取自 claims: %+v", c)
	}
}

func TestTutorListConversations(t *testing.T) {
	store := newSrvTutorStore()
	id1, _ := store.CreateConversation(context.Background(), 1, 2, "会话一")
	id2, _ := store.CreateConversation(context.Background(), 1, 2, "会话二")
	_, _ = store.CreateConversation(context.Background(), 1, 3, "他人会话")

	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)
	req := signedReq(t, api, testUser("studentA1"), http.MethodGet,
		"/api/tutor/conversations", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", rec.Code)
	}
	var got []tutorpkg.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != id2 || got[1].ID != id1 {
		t.Fatalf("应只见本人 2 个会话（新→旧）: %+v", got)
	}
}

// ---------- 消息读取与属主 404 ----------

func TestTutorGetMessagesOwnerAnd404(t *testing.T) {
	store := newSrvTutorStore()
	convID, _ := store.CreateConversation(context.Background(), 1, 2, "会话")
	_, _ = store.AppendMessage(context.Background(), convID,
		tutorpkg.NewMessage{Role: tutorpkg.RoleUser, Content: "提问"})

	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	// 属主 200。
	req := signedReq(t, api, testUser("studentA1"), http.MethodGet,
		fmt.Sprintf("/api/tutor/conversations/%d/messages", convID), nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("属主期望 200，得到 %d", rec.Code)
	}
	var msgs []tutorpkg.Message
	json.Unmarshal(rec.Body.Bytes(), &msgs)
	if len(msgs) != 1 || msgs[0].Content != "提问" {
		t.Fatalf("消息错误: %+v", msgs)
	}

	// 他人（同班 studentA2 不存在，用 B 班学生）→ 404。
	req = signedReq(t, api, testUser("studentB1"), http.MethodGet,
		fmt.Sprintf("/api/tutor/conversations/%d/messages", convID), nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("他人会话期望 404，得到 %d", rec.Code)
	}

	// 非法 ID → 404。
	req = signedReq(t, api, testUser("studentA1"), http.MethodGet,
		"/api/tutor/conversations/abc/messages", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非法 ID 期望 404，得到 %d", rec.Code)
	}
}

// ---------- SSE 提问：事件顺序 + 响应头 + 持久化 ----------

func TestTutorAskSSESequenceAndPersistence(t *testing.T) {
	store := newSrvTutorStore()
	stream := &srvFakeStreamLLM{deltas: []string{"先回顾", "定义"}}
	api, _ := newTutorTestAPI(store, stream, srvHits(), nil)

	convID, _ := store.CreateConversation(context.Background(), 1, 2, newConversationTitle)
	body, _ := json.Marshal(map[string]string{"question": "什么是集合？"})
	req := signedReq(t, api, testUser("studentA1"), http.MethodPost,
		fmt.Sprintf("/api/tutor/conversations/%d/messages", convID), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)

	// 状态与响应头。
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type 错误: %s", h.Get("Content-Type"))
	}
	if h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("必须设置 X-Accel-Buffering: no，得到 %q", h.Get("X-Accel-Buffering"))
	}
	if h.Get("Cache-Control") != "no-store" {
		t.Fatalf("必须 Cache-Control: no-store，得到 %q", h.Get("Cache-Control"))
	}

	events := parseSSEBody(t, rec.Body.Bytes())
	if len(events) != 4 {
		t.Fatalf("应有 4 个事件，得到 %d: %+v", len(events), events)
	}

	// meta 在最前：引用 + skillEnabled。
	if events[0].event != "meta" {
		t.Fatal("首个事件必须是 meta")
	}
	var meta sseMetaPayload
	json.Unmarshal([]byte(events[0].data), &meta)
	if !meta.SkillEnabled || len(meta.Citations) != 1 {
		t.Fatalf("meta 载荷错误: %s", events[0].data)
	}
	c := meta.Citations[0]
	if c.DocumentID != 100 || c.FileName != "讲义.txt" || c.Snippet == "" ||
		c.Locator.Kind != model.LocatorLines || c.Score != 0.91 {
		t.Fatalf("meta 引用错误: %+v", c)
	}

	// delta 中段，按序。
	if events[1].event != "delta" || events[1].data != `{"text":"先回顾"}` {
		t.Fatalf("delta 1 错误: %+v", events[1])
	}
	if events[2].event != "delta" || events[2].data != `{"text":"定义"}` {
		t.Fatalf("delta 2 错误: %+v", events[2])
	}

	// done 最后，携带 messageId。
	if events[3].event != "done" {
		t.Fatal("末事件必须是 done")
	}
	var done sseDonePayload
	json.Unmarshal([]byte(events[3].data), &done)
	if done.MessageID <= 0 {
		t.Fatal("done 必须携带 messageId")
	}

	// 流式结束后消息已持久化：user + assistant；assistant 带引用/快照。
	persisted := store.convMessages(convID)
	if len(persisted) != 2 {
		t.Fatalf("应持久化 2 条消息，得到 %d", len(persisted))
	}
	if persisted[0].m.Role != tutorpkg.RoleUser || persisted[0].m.Content != "什么是集合？" {
		t.Fatalf("user 持久化错误: %+v", persisted[0])
	}
	a := persisted[1].m
	if a.Role != tutorpkg.RoleAssistant || a.Content != "先回顾定义" ||
		len(a.Citations) != 1 || a.PromptUsed != "你是一位严格的数学助教。" || !a.SkillEnabled {
		t.Fatalf("assistant 持久化错误: %+v", a)
	}

	// 首问生成标题。
	if store.convs[convID].Title != "什么是集合？" {
		t.Fatalf("标题应为首问，得到 %q", store.convs[convID].Title)
	}
}

// ---------- 提问入参校验 400 ----------

func TestTutorAskValidation400(t *testing.T) {
	store := newSrvTutorStore()
	convID, _ := store.CreateConversation(context.Background(), 1, 2, "会话")
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, srvHits(), nil)

	path := fmt.Sprintf("/api/tutor/conversations/%d/messages", convID)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"非法 JSON", "{bad"},
		{"空问题", `{"question":"   "}`},
		{"超长问题", `{"question":"` + strings.Repeat("长", 501) + `"}`},
	} {
		req := signedReq(t, api, testUser("studentA1"), http.MethodPost, path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，得到 %d", tc.name, rec.Code)
		}
	}
}

// 他人会话提问 → 404。
func TestTutorAskOtherConversation404(t *testing.T) {
	store := newSrvTutorStore()
	convID, _ := store.CreateConversation(context.Background(), 1, 2, "A1 会话")
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, srvHits(), nil)

	body, _ := json.Marshal(map[string]string{"question": "题目"})
	req := signedReq(t, api, testUser("studentB1"), http.MethodPost,
		fmt.Sprintf("/api/tutor/conversations/%d/messages", convID), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("他人会话期望 404，得到 %d", rec.Code)
	}
}

// ---------- 401：无 token ----------

func TestTutorRoutesRequireAuth(t *testing.T) {
	api, _ := newTutorTestAPI(newSrvTutorStore(), &srvFakeStreamLLM{}, nil, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/tutor/conversations"},
		{http.MethodGet, "/api/tutor/conversations"},
		{http.MethodGet, "/api/tutor/conversations/1/messages"},
		{http.MethodPost, "/api/tutor/conversations/1/messages"},
		{http.MethodGet, "/api/tutor/assistant"},
		{http.MethodPut, "/api/tutor/assistant/prompt"},
		{http.MethodPut, "/api/tutor/assistant/skills/1"},
	} {
		req, _ := http.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 期望 401，得到 %d", tc.method, tc.path, rec.Code)
		}
	}
}

// ---------- 学生 403 ----------

func TestTutorStudentManagementForbidden(t *testing.T) {
	api, _ := newTutorTestAPI(newSrvTutorStore(), &srvFakeStreamLLM{}, nil, nil)

	// GET 配置
	req := signedReq(t, api, testUser("studentA1"), http.MethodGet, "/api/tutor/assistant", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("学生读配置期望 403，得到 %d", rec.Code)
	}

	// PUT 提示词
	req = signedReq(t, api, testUser("studentA1"), http.MethodPut,
		"/api/tutor/assistant/prompt", strings.NewReader(`{"systemPrompt":"x"}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("学生改提示词期望 403，得到 %d", rec.Code)
	}

	// PUT 技能开关
	req = signedReq(t, api, testUser("studentA1"), http.MethodPut,
		"/api/tutor/assistant/skills/50", strings.NewReader(`{"enabled":false}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("学生开关技能期望 403，得到 %d", rec.Code)
	}
}

// ---------- 教师配置生命周期 ----------

func TestTutorTeacherConfigLifecycle(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	// 初始 GET。
	req := signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/assistant", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("教师 GET 期望 200，得到 %d", rec.Code)
	}
	var cfg assistantResponse
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.SystemPrompt != "你是一位严格的数学助教。" || len(cfg.Skills) != 1 ||
		!cfg.Skills[0].Enabled || cfg.Skills[0].ID != 50 {
		t.Fatalf("初始配置错误: %+v", cfg)
	}

	// 保存提示词。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut,
		"/api/tutor/assistant/prompt", strings.NewReader(`{"systemPrompt":"你是引导式助教"}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存提示词期望 200，得到 %d: %s", rec.Code, rec.Body)
	}

	// 空提示词 → 400。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut,
		"/api/tutor/assistant/prompt", strings.NewReader(`{"systemPrompt":"  "}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空提示词期望 400，得到 %d", rec.Code)
	}

	// 停用技能。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut,
		"/api/tutor/assistant/skills/50", strings.NewReader(`{"enabled":false}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("停用技能期望 200，得到 %d", rec.Code)
	}

	// 不存在的技能 → 404。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut,
		"/api/tutor/assistant/skills/999", strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在技能期望 404，得到 %d", rec.Code)
	}

	// GET 验证全部持久化。
	req = signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/assistant", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.SystemPrompt != "你是引导式助教" || cfg.Skills[0].Enabled {
		t.Fatalf("更新未持久化: %+v", cfg)
	}
}

// ---------- meta 之前失败：503 JSON ----------

func TestTutorPreMetaFailure503(t *testing.T) {
	store := newSrvTutorStore()
	convID, _ := store.CreateConversation(context.Background(), 1, 2, "会话")
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, srvHits(), fmt.Errorf("embedding 失败"))

	body, _ := json.Marshal(map[string]string{"question": "题目"})
	req := signedReq(t, api, testUser("studentA1"), http.MethodPost,
		fmt.Sprintf("/api/tutor/conversations/%d/messages", convID), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("meta 前失败期望 503，得到 %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("应返回 JSON: %s", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "解题助手暂不可用") {
		t.Fatalf("503 文案错误: %s", rec.Body)
	}
}

// ---------- 生成中途失败：error 事件 + 友好文案，不泄露内部地址 ----------

func TestTutorMidStreamErrorEvent(t *testing.T) {
	store := newSrvTutorStore()
	convID, _ := store.CreateConversation(context.Background(), 1, 2, "会话")
	stream := &srvFakeStreamLLM{deltas: []string{"半截"}, err: fmt.Errorf("upstream http://10.0.0.5:9999 超时")}
	api, _ := newTutorTestAPI(store, stream, srvHits(), nil)

	body, _ := json.Marshal(map[string]string{"question": "题目"})
	req := signedReq(t, api, testUser("studentA1"), http.MethodPost,
		fmt.Sprintf("/api/tutor/conversations/%d/messages", convID), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)

	events := parseSSEBody(t, rec.Body.Bytes())
	if len(events) != 3 {
		t.Fatalf("期望 meta,delta,error 三事件，得到 %+v", events)
	}
	if events[0].event != "meta" || events[1].event != "delta" || events[2].event != "error" {
		t.Fatalf("事件顺序错误: %+v", events)
	}
	var errPayload sseErrorPayload
	json.Unmarshal([]byte(events[2].data), &errPayload)
	if errPayload.Error != "助手暂不可用，请稍后再试" {
		t.Fatalf("错误文案不友好: %q", errPayload.Error)
	}
	// 内部地址绝不能出现在响应体。
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatal("响应不得泄露内部服务地址")
	}
	// 半截内容已持久化。
	persisted := store.convMessages(convID)
	if len(persisted) != 2 || persisted[1].m.Content != "半截" {
		t.Fatalf("半截回答应持久化: %+v", persisted)
	}
}

// ---------- 4.2 真实断连：部分持久化、无 error 事件 ----------

func TestTutorRealClientCancelPersistsPartial(t *testing.T) {
	store := newSrvTutorStore()
	stream := &srvFakeStreamLLM{
		deltas:          []string{"部分"},
		blockAfterFirst: true,
		firstDelta:      make(chan struct{}),
	}
	api, _ := newTutorTestAPI(store, stream, srvHits(), nil)
	srv := httptest.NewServer(api.NewHandler())
	defer srv.Close()

	client := loginJarClient(t, srv.URL, "studentA1", "a1-pw")
	resp, _ := client.Post(srv.URL+"/api/tutor/conversations",
		"application/json", strings.NewReader("{}"))
	var created createdConversationResponse
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	reqBody, _ := json.Marshal(map[string]string{"question": "题目"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/tutor/conversations/%d/messages", srv.URL, created.ID),
		bytes.NewReader(reqBody))
	req.Header.Set("Authorization",
		"Bearer "+client.Transport.(*bearerTransport).token)

	requestDone := make(chan struct{})
	go func() {
		res, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		close(requestDone)
	}()

	select {
	case <-stream.firstDelta:
		cancel() // 首个增量后中断
	case <-time.After(3 * time.Second):
		t.Fatal("未收到首个增量")
	}
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		t.Fatal("断连后请求未结束")
	}

	// 已积累文本非空 → assistant 消息持久化（含引用/快照）。
	// 客户端取消时请求先于服务端处理器结束，持久化可能略晚完成，故轮询等待。
	var persisted []storedMessage
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted = store.convMessages(created.ID)
		if len(persisted) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(persisted) != 2 {
		t.Fatalf("应持久化 user 与部分 assistant，得到 %+v", persisted)
	}
	a := persisted[1].m
	if a.Role != tutorpkg.RoleAssistant || a.Content != "部分" ||
		len(a.Citations) != 1 || a.PromptUsed == "" || !a.SkillEnabled {
		t.Fatalf("断连部分持久化错误: %+v", a)
	}
}
