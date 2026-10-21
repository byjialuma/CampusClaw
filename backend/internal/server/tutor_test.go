package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
// 智能体（agents）与技能库（skills）均按 class_id 隔离；绑定（agentSkills/agentHandouts）按 agent_id。
type srvTutorStore struct {
	mu            sync.Mutex
	convs         map[int64]*tutorpkg.Conversation
	messages      map[int64][]storedMessage
	skills        map[int64][]tutorpkg.Skill // keyed by classID
	agents        map[int64]*tutorpkg.Agent
	agentSkills   map[int64][]int64
	agentHandouts map[int64][]int64
	nextConv      int64
	nextMsg       int64
	nextSkill     int64
	nextAgent     int64
}

func newSrvTutorStore() *srvTutorStore {
	s := &srvTutorStore{
		convs:    map[int64]*tutorpkg.Conversation{},
		messages: map[int64][]storedMessage{},
		skills: map[int64][]tutorpkg.Skill{
			// 「适用条件」行使技能可被词法路由命中（问题含「集合」即命中）。
			1: {{ID: 50, Name: "解题引导", Description: "引导",
				Content: "学生正在解题，请给出解题思路与步骤引导。\n适用条件：集合，题目",
				Source:  "seed", Builtin: true, Enabled: true}},
			2: {{ID: 60, Name: "解题引导", Description: "引导",
				Content: "学生正在解题，请给出解题思路与步骤引导。\n适用条件：集合，题目",
				Source:  "seed", Builtin: true, Enabled: true}},
		},
		agents:        map[int64]*tutorpkg.Agent{},
		agentSkills:   map[int64][]int64{},
		agentHandouts: map[int64][]int64{},
		nextSkill:     60, // 预置技能 id 已达 60，避免新建技能与预置冲突
		nextAgent:     2,
	}
	now := time.Now()
	// agent 1 属 A 班：提示词 + 绑定技能 50 与材料 100（与 srvHits 的 documentId 对齐）。
	s.agents[1] = &tutorpkg.Agent{ID: 1, ClassID: 1, Name: "数学助教",
		SystemPrompt: "你是一位严格的数学助教。", CreatedAt: now, UpdatedAt: now}
	// agent 2 属 B 班：提示词 + 绑定技能 60。
	s.agents[2] = &tutorpkg.Agent{ID: 2, ClassID: 2, Name: "B班助教",
		SystemPrompt: "你是B班数学助教。", CreatedAt: now, UpdatedAt: now}
	s.agentSkills[1] = []int64{50}
	s.agentSkills[2] = []int64{60}
	s.agentHandouts[1] = []int64{100}
	s.agentHandouts[2] = []int64{}
	return s
}

func (s *srvTutorStore) CreateConversation(_ context.Context, classID, userID, agentID int64, title string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextConv++
	s.convs[s.nextConv] = &tutorpkg.Conversation{
		ID: s.nextConv, ClassID: classID, UserID: userID, AgentID: agentID, Title: title, CreatedAt: time.Now(),
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
	skills, ok := s.skills[classID]
	if !ok {
		return nil, tutorpkg.ErrAssistantNotFound
	}
	return &tutorpkg.AssistantConfig{
		AssistantID: classID * 10, Name: "解题助手", Skills: skills,
	}, nil
}

// ---------- 智能体与绑定（server.tutorStore + tutor.Repo） ----------

func (s *srvTutorStore) GetAgent(_ context.Context, classID, id int64) (*tutorpkg.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag, ok := s.agents[id]
	if !ok || ag.ClassID != classID {
		return nil, tutorpkg.ErrAgentNotFound
	}
	cp := *ag
	return &cp, nil
}

func (s *srvTutorStore) ListAgents(_ context.Context, classID int64) ([]tutorpkg.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []tutorpkg.Agent{}
	for _, id := range sortedAgentIDs(s.agents) {
		if ag := s.agents[id]; ag.ClassID == classID {
			out = append(out, *ag)
		}
	}
	return out, nil
}

func (s *srvTutorStore) CreateAgent(_ context.Context, classID int64, name, systemPrompt string) (*tutorpkg.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ag := range s.agents {
		if ag.ClassID == classID && ag.Name == name {
			return nil, tutorpkg.ErrAgentNameConflict
		}
	}
	s.nextAgent++
	now := time.Now()
	ag := &tutorpkg.Agent{ID: s.nextAgent, ClassID: classID, Name: name,
		SystemPrompt: systemPrompt, CreatedAt: now, UpdatedAt: now}
	s.agents[ag.ID] = ag
	s.agentSkills[ag.ID] = []int64{}
	s.agentHandouts[ag.ID] = []int64{}
	cp := *ag
	return &cp, nil
}

func (s *srvTutorStore) UpdateAgent(_ context.Context, classID, id int64, name, systemPrompt string) (*tutorpkg.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag, ok := s.agents[id]
	if !ok || ag.ClassID != classID {
		return nil, tutorpkg.ErrAgentNotFound
	}
	for _, other := range s.agents {
		if other.ID != id && other.ClassID == classID && other.Name == name {
			return nil, tutorpkg.ErrAgentNameConflict
		}
	}
	ag.Name = name
	ag.SystemPrompt = systemPrompt
	ag.UpdatedAt = time.Now()
	cp := *ag
	return &cp, nil
}

func (s *srvTutorStore) DeleteAgent(_ context.Context, classID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag, ok := s.agents[id]
	if !ok || ag.ClassID != classID {
		return tutorpkg.ErrAgentNotFound
	}
	for _, c := range s.convs {
		if c.AgentID == id {
			return tutorpkg.ErrAgentInUse
		}
	}
	delete(s.agents, id)
	delete(s.agentSkills, id)
	delete(s.agentHandouts, id)
	return nil
}

func (s *srvTutorStore) ReplaceAgentSkills(_ context.Context, classID, agentID int64, skillIDs []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag, ok := s.agents[agentID]
	if !ok || ag.ClassID != classID {
		return tutorpkg.ErrAgentNotFound
	}
	for _, id := range skillIDs {
		found := false
		for _, sk := range s.skills[classID] {
			if sk.ID == id {
				found = true
				break
			}
		}
		if !found {
			return tutorpkg.ErrSkillNotFound
		}
	}
	s.agentSkills[agentID] = dedupeInt64(skillIDs)
	return nil
}

func (s *srvTutorStore) ReplaceAgentMaterials(_ context.Context, classID, agentID int64, handoutIDs []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag, ok := s.agents[agentID]
	if !ok || ag.ClassID != classID {
		return tutorpkg.ErrAgentNotFound
	}
	for _, id := range handoutIDs {
		if !s.handoutInClass(classID, id) {
			return tutorpkg.ErrHandoutNotFound
		}
	}
	s.agentHandouts[agentID] = dedupeInt64(handoutIDs)
	return nil
}

func (s *srvTutorStore) AgentSkillIDs(_ context.Context, agentID int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64{}, s.agentSkills[agentID]...), nil
}

func (s *srvTutorStore) AgentHandoutIDs(_ context.Context, agentID int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64{}, s.agentHandouts[agentID]...), nil
}

// AgentConfig 返回一次提问所需的智能体配置：提示词 + 绑定且已启用的技能（id 升序）+ 绑定材料。
func (s *srvTutorStore) AgentConfig(_ context.Context, classID, agentID int64) (*tutorpkg.AgentConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ag, ok := s.agents[agentID]
	if !ok || ag.ClassID != classID {
		return nil, tutorpkg.ErrAgentNotFound
	}
	cfg := &tutorpkg.AgentConfig{
		AgentID: agentID, ClassID: classID, Name: ag.Name,
		SystemPrompt: ag.SystemPrompt, Skills: []tutorpkg.Skill{},
		HandoutIDs: append([]int64{}, s.agentHandouts[agentID]...),
	}
	bound := map[int64]struct{}{}
	for _, id := range s.agentSkills[agentID] {
		bound[id] = struct{}{}
	}
	for _, sk := range s.skills[classID] {
		if _, ok := bound[sk.ID]; ok && sk.Enabled {
			cfg.Skills = append(cfg.Skills, sk)
		}
	}
	return cfg, nil
}

// handoutInClass 判断讲义是否属于本班：测试中仅 A 班持有材料 100。
func (s *srvTutorStore) handoutInClass(classID, handoutID int64) bool {
	return classID == 1 && handoutID == 100
}

// sortedAgentIDs 返回稳定的 agent id 升序切片。
func sortedAgentIDs(m map[int64]*tutorpkg.Agent) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// dedupeInt64 按首次出现顺序去重。
func dedupeInt64(ids []int64) []int64 {
	seen := map[int64]struct{}{}
	out := []int64{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

func (s *srvTutorStore) CreateSkill(_ context.Context, classID int64, name, content, source string) (*tutorpkg.Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.skills[classID]; !ok {
		return nil, tutorpkg.ErrAssistantNotFound
	}
	s.nextSkill++
	sk := tutorpkg.Skill{ID: s.nextSkill, Name: name, Content: content, Source: source}
	s.skills[classID] = append(s.skills[classID], sk)
	return &sk, nil
}

func (s *srvTutorStore) DeleteSkill(_ context.Context, classID, skillID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sk := range s.skills[classID] {
		if sk.ID == skillID {
			if sk.Builtin {
				return tutorpkg.ErrSkillBuiltinProtected
			}
			s.skills[classID] = append(s.skills[classID][:i], s.skills[classID][i+1:]...)
			return nil
		}
	}
	return tutorpkg.ErrSkillNotFound
}

func (s *srvTutorStore) SlugExists(_ context.Context, classID int64, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sk := range s.skills[classID] {
		if sk.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

func (s *srvTutorStore) CreateSkillPackage(_ context.Context, classID int64, pkg *tutorpkg.SkillPackage, slug string) (*tutorpkg.Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.skills[classID]; !ok {
		return nil, tutorpkg.ErrAssistantNotFound
	}
	for _, sk := range s.skills[classID] {
		if sk.Slug == slug {
			return nil, tutorpkg.ErrSkillPackageConflict
		}
	}
	s.nextSkill++
	sk := tutorpkg.Skill{
		ID: s.nextSkill, Name: pkg.Name, Description: pkg.Description, Content: pkg.Body,
		Source: tutorpkg.SkillSourcePackage, Slug: slug,
		Resources: pkg.Resources, Conditions: pkg.Conditions, ToolRefs: pkg.ToolRefs,
	}
	s.skills[classID] = append(s.skills[classID], sk)
	return &sk, nil
}

func (s *srvTutorStore) SetSkillPackagePath(_ context.Context, classID, skillID int64, dirPath, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sk := range s.skills[classID] {
		if sk.ID == skillID {
			s.skills[classID][i].DirPath = dirPath
			return nil
		}
	}
	return tutorpkg.ErrSkillNotFound
}

func (s *srvTutorStore) GetSkillPackage(_ context.Context, classID, skillID int64) (*tutorpkg.Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sk := range s.skills[classID] {
		if sk.ID == skillID {
			cp := sk
			return &cp, nil
		}
	}
	return nil, tutorpkg.ErrSkillNotFound
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
		"/api/tutor/conversations", strings.NewReader(`{"agentId":1}`))
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
	id1, _ := store.CreateConversation(context.Background(), 1, 2, 1, "会话一")
	id2, _ := store.CreateConversation(context.Background(), 1, 2, 1, "会话二")
	_, _ = store.CreateConversation(context.Background(), 1, 3, 1, "他人会话")

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
	convID, _ := store.CreateConversation(context.Background(), 1, 2, 1, "会话")
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

	convID, _ := store.CreateConversation(context.Background(), 1, 2, 1, newConversationTitle)
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

	// meta 在最前：引用 + 启用技能快照。
	if events[0].event != "meta" {
		t.Fatal("首个事件必须是 meta")
	}
	var meta sseMetaPayload
	json.Unmarshal([]byte(events[0].data), &meta)
	if len(meta.Skills) != 1 || meta.Skills[0].ID != 50 || meta.Skills[0].Name != "解题引导" ||
		len(meta.Citations) != 1 {
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
		len(a.Citations) != 1 || a.PromptUsed != "你是一位严格的数学助教。" ||
		len(a.SkillsUsed) != 1 || a.SkillsUsed[0].ID != 50 {
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
	convID, _ := store.CreateConversation(context.Background(), 1, 2, 1, "会话")
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
	convID, _ := store.CreateConversation(context.Background(), 1, 2, 1, "A1 会话")
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
		{http.MethodPut, "/api/tutor/assistant/skills/1"},
		{http.MethodPost, "/api/tutor/assistant/skills"},
		{http.MethodPost, "/api/tutor/assistant/skills/upload"},
		{http.MethodDelete, "/api/tutor/assistant/skills/1"},
		{http.MethodGet, "/api/tutor/agents"},
		{http.MethodPost, "/api/tutor/agents"},
		{http.MethodPut, "/api/tutor/agents/1"},
		{http.MethodDelete, "/api/tutor/agents/1"},
		{http.MethodPut, "/api/tutor/agents/1/skills"},
		{http.MethodPut, "/api/tutor/agents/1/materials"},
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

	// 智能体写接口：学生一律 403（列表 GET 允许，仅返回 id/name/updatedAt）。
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tutor/agents", `{"name":"x","systemPrompt":"y"}`},
		{http.MethodPut, "/api/tutor/agents/1", `{"name":"x","systemPrompt":"y"}`},
		{http.MethodDelete, "/api/tutor/agents/1", ``},
		{http.MethodPut, "/api/tutor/agents/1/skills", `{"skillIds":[50]}`},
		{http.MethodPut, "/api/tutor/agents/1/materials", `{"handoutIds":[100]}`},
	} {
		req = signedReq(t, api, testUser("studentA1"), tc.method, tc.path, strings.NewReader(tc.body))
		rec = httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("学生 %s %s 期望 403，得到 %d", tc.method, tc.path, rec.Code)
		}
	}

	// PUT 技能开关
	req = signedReq(t, api, testUser("studentA1"), http.MethodPut,
		"/api/tutor/assistant/skills/50", strings.NewReader(`{"enabled":false}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("学生开关技能期望 403，得到 %d", rec.Code)
	}

	// POST 新建技能 / POST 上传技能 / DELETE 技能
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tutor/assistant/skills", `{"name":"x","content":"y"}`},
		{http.MethodPost, "/api/tutor/assistant/skills/upload", `{}`},
		{http.MethodPost, "/api/tutor/assistant/skills/package", `{}`},
		{http.MethodGet, "/api/tutor/assistant/skills/50", ``},
		{http.MethodGet, "/api/tutor/assistant/skills/50/resources?path=assets/a.md", ``},
		{http.MethodDelete, "/api/tutor/assistant/skills/50", ``},
	} {
		req = signedReq(t, api, testUser("studentA1"), tc.method, tc.path, strings.NewReader(tc.body))
		rec = httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("学生 %s %s 期望 403，得到 %d", tc.method, tc.path, rec.Code)
		}
	}
}

// ---------- 教师技能库读取与开关生命周期（提示词已随智能体配置，不再在本接口读写） ----------

func TestTutorTeacherConfigLifecycle(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	// 初始 GET：技能库视图。
	req := signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/assistant", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("教师 GET 期望 200，得到 %d", rec.Code)
	}
	var cfg assistantResponse
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if len(cfg.Skills) != 1 || !cfg.Skills[0].Enabled || cfg.Skills[0].ID != 50 {
		t.Fatalf("初始技能库错误: %+v", cfg)
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

	// GET 验证开关已持久化。
	req = signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/assistant", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.Skills[0].Enabled {
		t.Fatalf("更新未持久化: %+v", cfg)
	}
}

// ---------- 教师技能管理生命周期（新建/上传/开关/删除 + 内置保护 + 校验） ----------

func TestTutorTeacherSkillCRUDLifecycle(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)
	// 在线新建：201，默认停用，来源 online。
	req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/assistant/skills",
		strings.NewReader(`{"name":"英语写作","content":"用英文润色关键术语表达。"}`))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("在线新建技能期望 201，得到 %d: %s", rec.Code, rec.Body)
	}
	var created tutorpkg.Skill
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID <= 0 || created.Name != "英语写作" || created.Source != tutorpkg.SkillSourceOnline ||
		created.Enabled || created.Content != "用英文润色关键术语表达。" {
		t.Fatalf("新建技能字段错误: %+v", created)
	}

	// 上传 md 文件：文件名去扩展名作为默认名称，来源 upload。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "审题清单.md")
	fw.Write([]byte("# 审题清单\n先标注已知量与未知量。"))
	mw.Close()
	req = signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/assistant/skills/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传技能期望 201，得到 %d: %s", rec.Code, rec.Body)
	}
	var uploaded tutorpkg.Skill
	json.Unmarshal(rec.Body.Bytes(), &uploaded)
	if uploaded.Name != "审题清单" || uploaded.Source != tutorpkg.SkillSourceUpload ||
		!strings.Contains(uploaded.Content, "先标注已知量与未知量") {
		t.Fatalf("上传技能字段错误: %+v", uploaded)
	}

	// 启用新建技能 → 出现在 GET 列表；编排配置可见（AssistantConfig 含新技能）。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut,
		fmt.Sprintf("/api/tutor/assistant/skills/%d", created.ID), strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("启用技能期望 200，得到 %d", rec.Code)
	}
	req = signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/assistant", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	var cfg assistantResponse
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if len(cfg.Skills) != 3 || !cfg.Skills[1].Enabled {
		t.Fatalf("技能列表应含 3 条且新建技能已启用: %+v", cfg.Skills)
	}

	// 删除上传的自建技能 → 200，列表中消失。
	req = signedReq(t, api, testUser("teacherA"), http.MethodDelete,
		fmt.Sprintf("/api/tutor/assistant/skills/%d", uploaded.ID), nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除自建技能期望 200，得到 %d", rec.Code)
	}

	// 删除内置技能 → 409。
	req = signedReq(t, api, testUser("teacherA"), http.MethodDelete, "/api/tutor/assistant/skills/50", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("删除内置技能期望 409，得到 %d", rec.Code)
	}

	// 删除不存在的技能 → 404。
	req = signedReq(t, api, testUser("teacherA"), http.MethodDelete, "/api/tutor/assistant/skills/999", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删除不存在技能期望 404，得到 %d", rec.Code)
	}
}

// 技能入参校验：名称/内容必填与超长，上传扩展名/大小限制。
func TestTutorSkillValidation400(t *testing.T) {
	api, _ := newTutorTestAPI(newSrvTutorStore(), &srvFakeStreamLLM{}, nil, nil)
	longName := strings.Repeat("长", 121)
	longContent := strings.Repeat("字", 20001)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"空名称", `{"name":"  ","content":"c"}`},
		{"空内容", `{"name":"n","content":"  "}`},
		{"名称超长", fmt.Sprintf(`{"name":%q,"content":"c"}`, longName)},
		{"内容超长", fmt.Sprintf(`{"name":"n","content":%q}`, longContent)},
	} {
		req := signedReq(t, api, testUser("teacherA"), http.MethodPost,
			"/api/tutor/assistant/skills", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，得到 %d", tc.name, rec.Code)
		}
	}

	// 上传 .pdf → 400。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "笔记.pdf")
	fw.Write([]byte("not-a-skill"))
	mw.Close()
	req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/assistant/skills/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非 md/txt 上传期望 400，得到 %d", rec.Code)
	}
}

// ---------- meta 之前失败：503 JSON ----------

func TestTutorPreMetaFailure503(t *testing.T) {
	store := newSrvTutorStore()
	convID, _ := store.CreateConversation(context.Background(), 1, 2, 1, "会话")
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
	convID, _ := store.CreateConversation(context.Background(), 1, 2, 1, "会话")
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
		"application/json", strings.NewReader(`{"agentId":1}`))
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
		len(a.Citations) != 1 || a.PromptUsed == "" || len(a.SkillsUsed) != 1 {
		t.Fatalf("断连部分持久化错误: %+v", a)
	}
}

// ---------- 智能体：创建会话必须带 agentId 且属本班 ----------

func TestTutorCreateConversationAgentValidation(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)
	path := "/api/tutor/conversations"

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"缺少 agentId", `{}`, http.StatusBadRequest},
		{"agentId 非正", `{"agentId":0}`, http.StatusBadRequest},
		{"agentId 不存在", `{"agentId":999}`, http.StatusNotFound},
		{"agentId 跨班（B 班智能体）", `{"agentId":2}`, http.StatusNotFound},
	} {
		req := signedReq(t, api, testUser("studentA1"), http.MethodPost, path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s 期望 %d，得到 %d: %s", tc.name, tc.want, rec.Code, rec.Body)
		}
	}

	// 会话落库须携带 agentId。
	req := signedReq(t, api, testUser("studentA1"), http.MethodPost, path, strings.NewReader(`{"agentId":1}`))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	var got createdConversationResponse
	json.Unmarshal(rec.Body.Bytes(), &got)
	if c := store.convs[got.ID]; c == nil || c.AgentID != 1 {
		t.Fatalf("会话应绑定 agentId=1: %+v", c)
	}
}

// ---------- 智能体列表：教师含提示词与绑定，学生仅 id/name/updatedAt ----------

func TestTutorListAgentsRoleCrop(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	// 教师视图：class1 仅 agent 1；含 systemPrompt 与绑定清单。
	req := signedReq(t, api, testUser("teacherA"), http.MethodGet, "/api/tutor/agents", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("教师列表期望 200，得到 %d", rec.Code)
	}
	var teacher []teacherAgentView
	if err := json.Unmarshal(rec.Body.Bytes(), &teacher); err != nil {
		t.Fatal(err)
	}
	if len(teacher) != 1 || teacher[0].ID != 1 || teacher[0].Name != "数学助教" ||
		teacher[0].SystemPrompt != "你是一位严格的数学助教。" ||
		len(teacher[0].SkillIDs) != 1 || teacher[0].SkillIDs[0] != 50 ||
		len(teacher[0].HandoutIDs) != 1 || teacher[0].HandoutIDs[0] != 100 {
		t.Fatalf("教师视图错误: %+v", teacher)
	}

	// 学生视图：不含提示词字段（原始 JSON 里不得出现 systemPrompt）。
	req = signedReq(t, api, testUser("studentA1"), http.MethodGet, "/api/tutor/agents", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("学生列表期望 200，得到 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "systemPrompt") ||
		strings.Contains(rec.Body.String(), "skillIds") {
		t.Fatalf("学生视图不得泄露提示词或绑定: %s", rec.Body)
	}
	var student []studentAgentView
	json.Unmarshal(rec.Body.Bytes(), &student)
	if len(student) != 1 || student[0].ID != 1 || student[0].Name != "数学助教" {
		t.Fatalf("学生视图错误: %+v", student)
	}
}

// ---------- 智能体增删改：201/200/400/404/409 ----------

func TestTutorAgentCRUDLifecycle(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	// 新建 201。
	req := signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/agents",
		strings.NewReader(`{"name":"几何助教","systemPrompt":"专注几何。"}`))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("新建智能体期望 201，得到 %d: %s", rec.Code, rec.Body)
	}
	var created teacherAgentView
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID <= 0 || created.Name != "几何助教" || created.SystemPrompt != "专注几何。" {
		t.Fatalf("新建字段错误: %+v", created)
	}

	// 名称/提示词校验 400。
	for _, tc := range []struct{ name, body string }{
		{"空名称", `{"name":"  ","systemPrompt":"x"}`},
		{"名称超长", `{"name":"` + strings.Repeat("长", 121) + `","systemPrompt":"x"}`},
		{"提示词超长", `{"name":"n","systemPrompt":"` + strings.Repeat("字", 2001) + `"}`},
	} {
		req = signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/agents",
			strings.NewReader(tc.body))
		rec = httptest.NewRecorder()
		api.NewHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，得到 %d", tc.name, rec.Code)
		}
	}

	// 同班重名 400。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPost, "/api/tutor/agents",
		strings.NewReader(`{"name":"几何助教","systemPrompt":"重复"}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重名期望 400，得到 %d", rec.Code)
	}

	// 更新 200。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut,
		fmt.Sprintf("/api/tutor/agents/%d", created.ID),
		strings.NewReader(`{"name":"几何助教2","systemPrompt":"更新提示词"}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新智能体期望 200，得到 %d: %s", rec.Code, rec.Body)
	}
	var updated teacherAgentView
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Name != "几何助教2" || updated.SystemPrompt != "更新提示词" {
		t.Fatalf("更新未生效: %+v", updated)
	}

	// 更新不存在 → 404。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut, "/api/tutor/agents/999",
		strings.NewReader(`{"name":"x","systemPrompt":"y"}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("更新不存在期望 404，得到 %d", rec.Code)
	}

	// 删除 200。
	req = signedReq(t, api, testUser("teacherA"), http.MethodDelete,
		fmt.Sprintf("/api/tutor/agents/%d", created.ID), nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除智能体期望 200，得到 %d", rec.Code)
	}

	// 删除不存在 → 404。
	req = signedReq(t, api, testUser("teacherA"), http.MethodDelete, "/api/tutor/agents/999", nil)
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删除不存在期望 404，得到 %d", rec.Code)
	}
}

// 仍被会话引用的智能体删除 → 409。
func TestTutorDeleteAgentInUse409(t *testing.T) {
	store := newSrvTutorStore()
	_, _ = store.CreateConversation(context.Background(), 1, 2, 1, "占用会话")
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	req := signedReq(t, api, testUser("teacherA"), http.MethodDelete, "/api/tutor/agents/1", nil)
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("被引用智能体删除期望 409，得到 %d", rec.Code)
	}
}

// ---------- 智能体绑定：技能/材料全量替换 + 越界 404 且不变更 ----------

func TestTutorAgentBindings(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)

	// 绑定技能 200。
	req := signedReq(t, api, testUser("teacherA"), http.MethodPut, "/api/tutor/agents/1/skills",
		strings.NewReader(`{"skillIds":[50]}`))
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("绑定技能期望 200，得到 %d: %s", rec.Code, rec.Body)
	}

	// 绑定不属本班的技能 → 404，且原绑定不变。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut, "/api/tutor/agents/1/skills",
		strings.NewReader(`{"skillIds":[60]}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨班技能期望 404，得到 %d", rec.Code)
	}
	ids, _ := store.AgentSkillIDs(context.Background(), 1)
	if len(ids) != 1 || ids[0] != 50 {
		t.Fatalf("失败绑定不应改变既有绑定: %+v", ids)
	}

	// 绑定材料 200。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut, "/api/tutor/agents/1/materials",
		strings.NewReader(`{"handoutIds":[100]}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("绑定材料期望 200，得到 %d", rec.Code)
	}

	// 绑定不属本班的材料 → 404。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut, "/api/tutor/agents/1/materials",
		strings.NewReader(`{"handoutIds":[999]}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("越界材料期望 404，得到 %d", rec.Code)
	}

	// 绑定到不存在的智能体 → 404。
	req = signedReq(t, api, testUser("teacherA"), http.MethodPut, "/api/tutor/agents/999/skills",
		strings.NewReader(`{"skillIds":[]}`))
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在智能体绑定期望 404，得到 %d", rec.Code)
	}
}
