package tutor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"campusclaw/internal/llm"
	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// ---------- fakes ----------

// fakeEmbedder 返回固定向量；记录调用次数以断言「未绑定材料时不检索」。
type fakeEmbedder struct {
	vec     []float32
	calls   int
	embedOn []string
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.embedOn = append(f.embedOn, texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}
func (f *fakeEmbedder) Dim() int { return len(f.vec) }

// fakeVectors 记录召回使用的 classID，返回预置命中。
type fakeVectors struct {
	hits       []vector.Hit
	err        error
	calls      int
	gotClassID int64
	gotTopK    int
}

func (f *fakeVectors) Search(_ context.Context, _ []float32, classID int64, topK int) ([]vector.Hit, error) {
	f.calls++
	f.gotClassID = classID
	f.gotTopK = topK
	return f.hits, f.err
}

// fakeStreamLLM 按配置发出增量；可在发完后等待取消，或返回错误；记录消息序列。
type fakeStreamLLM struct {
	deltas        []string
	failErr       error
	blockToCancel bool
	gotMessages   []llm.Message
}

func (f *fakeStreamLLM) StreamAsk(ctx context.Context, msgs []llm.Message, onDelta func(string) error) (string, error) {
	f.gotMessages = append(f.gotMessages, msgs...)
	var sb strings.Builder
	for _, d := range f.deltas {
		if err := ctx.Err(); err != nil {
			return sb.String(), err
		}
		sb.WriteString(d)
		if err := onDelta(d); err != nil {
			return sb.String(), err
		}
	}
	if f.blockToCancel {
		<-ctx.Done()
		return sb.String(), ctx.Err()
	}
	return sb.String(), f.failErr
}

// appendedMsg 记录一次 AppendMessage 调用。
type appendedMsg struct {
	convID int64
	m      NewMessage
}

// fakeRepo 记录全部持久化调用。
type fakeRepo struct {
	conv      *Conversation
	convErr   error
	agentCfg  *AgentConfig
	cfgErr    error
	gotAgent  int64
	gotClass  int64
	history   []Message
	renameErr error
	renamed   []string
	appended  []appendedMsg
	appendErr error
}

func (r *fakeRepo) ConversationForUser(_ context.Context, _, _ int64) (*Conversation, error) {
	return r.conv, r.convErr
}
func (r *fakeRepo) AgentConfig(_ context.Context, classID, agentID int64) (*AgentConfig, error) {
	r.gotClass, r.gotAgent = classID, agentID
	return r.agentCfg, r.cfgErr
}
func (r *fakeRepo) MessagesByConversation(_ context.Context, _ int64) ([]Message, error) {
	return r.history, nil
}
func (r *fakeRepo) RenameConversationIfEmpty(_ context.Context, _ int64, question string) error {
	r.renamed = append(r.renamed, question)
	return r.renameErr
}
func (r *fakeRepo) AppendMessage(_ context.Context, convID int64, m NewMessage) (int64, error) {
	if r.appendErr != nil {
		return 0, r.appendErr
	}
	r.appended = append(r.appended, appendedMsg{convID, m})
	return int64(len(r.appended)), nil
}

// ---------- fixtures ----------

func sampleHits() []vector.Hit {
	return []vector.Hit{{
		ID:    "p1",
		Score: 0.91,
		Payload: vector.Payload{
			ClassID:    10,
			DocumentID: 100,
			FileName:   "讲义.txt",
			FileType:   "txt",
			Text:       "集合是具有某种特定性质的事物的总体",
			Locator:    model.Locator{Kind: model.LocatorLines, StartLine: 3, EndLine: 5},
		},
	}}
}

// guidingSkill 返回一条「适用条件：集合」的内置技能（问题含「集合」即命中）。
func guidingSkill() Skill {
	return Skill{
		ID: 7, Name: "解题引导",
		Content: skillInstruction + "\n适用条件：集合",
		Source: "seed", Builtin: true, Enabled: true,
	}
}

func baseAgentConfig() *AgentConfig {
	return &AgentConfig{
		AgentID:      5,
		Name:         "解题助手",
		SystemPrompt: "你是一位严格的数学助教。",
		Skills:       []Skill{guidingSkill()},
		HandoutIDs:   []int64{100},
	}
}

func newHarness(repo Repo, hits []vector.Hit, stream *fakeStreamLLM) (*Service, *fakeVectors) {
	vecs := &fakeVectors{hits: hits}
	return &Service{
		Repo:     repo,
		Embedder: &fakeEmbedder{vec: []float32{0.1, 0.2}},
		Vectors:  vecs,
		LLM:      stream,
		TopK:     5,
	}, vecs
}

func collectEvents() (*[]Event, func(Event)) {
	var events []Event
	return &events, func(e Event) { events = append(events, e) }
}

func defaultAskInput(question string, onEvent func(Event)) AskInput {
	return AskInput{
		ConversationID: 1,
		UserID:         2,
		ClassID:        10,
		Question:       question,
		OnEvent:        onEvent,
	}
}

// ---------- 3.5 完整编排 ----------

func TestAskStreamFullOrchestration(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: []string{"思路一", "思路二"}}
	svc, vecs := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合？", onEvent))
	if err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	events := *eventsPtr

	if out.Answer != "思路一思路二" || out.MessageID != 2 {
		t.Fatalf("输出错误: %+v", out)
	}

	gotTypes := make([]string, len(events))
	for i, e := range events {
		gotTypes[i] = e.Type
	}
	if strings.Join(gotTypes, ",") != "meta,delta,delta,done" {
		t.Fatalf("事件顺序错误: %v", gotTypes)
	}

	// meta：单条命中技能 + 带 index 的引用。
	meta := events[0]
	if len(meta.Skills) != 1 || meta.Skills[0].ID != 7 || meta.Skills[0].Name != "解题引导" ||
		len(meta.Citations) != 1 || meta.Citations[0].Index != 1 {
		t.Fatalf("meta 错误: %+v", meta)
	}
	c := meta.Citations[0]
	if c.DocumentID != 100 || c.FileName != "讲义.txt" || c.FileType != "txt" ||
		c.Snippet != "集合是具有某种特定性质的事物的总体" || c.Score != 0.91 ||
		c.Locator.Kind != model.LocatorLines || c.Locator.StartLine != 3 || c.Locator.EndLine != 5 {
		t.Fatalf("引用映射错误: %+v", c)
	}

	if events[1].Text != "思路一" || events[2].Text != "思路二" {
		t.Fatal("delta 文本错误")
	}
	if events[3].MessageID != 2 {
		t.Fatal("done messageId 错误")
	}

	// 会话智能体与班级取自 claims：AgentConfig 收到 classID=10 agentID=5。
	if repo.gotClass != 10 || repo.gotAgent != 5 {
		t.Fatalf("AgentConfig 参数错误: class=%d agent=%d", repo.gotClass, repo.gotAgent)
	}
	// 检索强制使用本班 classID。
	if vecs.gotClassID != 10 || vecs.gotTopK != 5 {
		t.Fatalf("检索参数错误: class=%d topK=%d", vecs.gotClassID, vecs.gotTopK)
	}

	// system：提示词 + 命中技能内容 + 材料上下文（含 [1] 标号指令与片段）。
	if len(stream.gotMessages) != 2 {
		t.Fatalf("消息数错误: %d", len(stream.gotMessages))
	}
	sys := stream.gotMessages[0]
	if sys.Role != "system" || !strings.Contains(sys.Content, "严格的数学助教") ||
		!strings.Contains(sys.Content, "不要直接给出该题的最终答案") ||
		!strings.Contains(sys.Content, "讲义.txt") || !strings.Contains(sys.Content, "[1]") {
		t.Fatalf("system 组装错误: %q", sys.Content)
	}
	last := stream.gotMessages[1]
	if last.Role != RoleUser || last.Content != "什么是集合？" {
		t.Fatalf("末条消息错误: %+v", last)
	}

	// 持久化：先 user 后 assistant；assistant 带引用、提示词快照与命中技能快照。
	if len(repo.appended) != 2 {
		t.Fatalf("持久化次数错误: %d", len(repo.appended))
	}
	u := repo.appended[0].m
	if u.Role != RoleUser || u.Content != "什么是集合？" || u.PromptUsed != "" {
		t.Fatalf("user 消息错误: %+v", u)
	}
	a := repo.appended[1].m
	if a.Role != RoleAssistant || a.Content != "思路一思路二" ||
		len(a.Citations) != 1 || a.PromptUsed != "你是一位严格的数学助教。" ||
		len(a.SkillsUsed) != 1 || a.SkillsUsed[0].Name != "解题引导" {
		t.Fatalf("assistant 消息错误: %+v", a)
	}

	if len(repo.renamed) != 1 || repo.renamed[0] != "什么是集合？" {
		t.Fatalf("标题生成错误: %v", repo.renamed)
	}
}

// ---------- 3.5 命中一条技能 / 零命中 ----------

// 候选集两条技能：高分者命中，低分者不参与本轮回答。
func TestAskStreamRoutesHighestScoreSkill(t *testing.T) {
	cfg := baseAgentConfig()
	cfg.Skills = []Skill{
		{ID: 7, Name: "解题引导", Content: skillInstruction + "\n适用条件：集合", Source: "seed", Enabled: true},
		{ID: 8, Name: "英语写作", Content: "用英文润色。\n适用条件：集合, 子集", Source: "online", Enabled: true},
	}
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"答"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合的子集", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	// 「集合」「子集」均命中 → 技能 8 得 2 分，技能 7 得 1 分 → 命中 8。
	meta := (*eventsPtr)[0]
	if len(meta.Skills) != 1 || meta.Skills[0].ID != 8 {
		t.Fatalf("应命中最高分技能 8: %+v", meta.Skills)
	}
	sys := stream.gotMessages[0].Content
	if !strings.Contains(sys, "用英文润色。") {
		t.Fatalf("命中技能内容必须并入 system: %q", sys)
	}
}

// 零命中：不选技能，仅提示词 + 护栏。
func TestAskStreamZeroHitNoSkill(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: []string{"按角色作答"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("完全无关的问题", onEvent))
	if err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	meta := (*eventsPtr)[0]
	if len(meta.Skills) != 0 {
		t.Fatalf("零命中时 meta 技能列表必须为空: %+v", meta.Skills)
	}
	// 零命中不得并入技能指令；引用仍按检索结果给出（本用例检索有命中）。
	sys := stream.gotMessages[0].Content
	if strings.Contains(sys, "不要直接给出该题的最终答案") {
		t.Fatalf("零命中不得并入技能内容: %q", sys)
	}
	if len(out.Skills) != 0 || len(repo.appended[1].m.SkillsUsed) != 0 {
		t.Fatal("零命中时技能快照应为空")
	}
}

// 检索落空：引用为空、注入反编造护栏，仍调用 LLM 作答。
func TestAskStreamSearchMissStillAnswers(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: []string{"仅按提示词回答"}}
	svc, _ := newHarness(repo, nil, stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合？", onEvent))
	if err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	events := *eventsPtr
	if len(events[0].Citations) != 0 || len(out.Citations) != 0 {
		t.Fatal("检索落空时引用必须为空")
	}
	sys := stream.gotMessages[0].Content
	if !strings.Contains(sys, noMaterialGuard) {
		t.Fatalf("检索落空必须注入反编造护栏: %q", sys)
	}
	if strings.Contains(sys, "根据已有材料无法回答该问题") {
		t.Fatalf("熔断固定文案不得出现: %q", sys)
	}
	if len(stream.gotMessages) != 2 {
		t.Fatalf("检索落空仍必须调用 LLM，消息数=%d", len(stream.gotMessages))
	}
}

// 智能体未绑定材料：不检索、引用为空、仍按提示词作答。
func TestAskStreamNoBoundMaterialsStillAnswers(t *testing.T) {
	cfg := baseAgentConfig()
	cfg.HandoutIDs = nil
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"按提示词回答"}}
	svc, vecs := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合？", onEvent))
	if err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	if vecs.calls != 0 {
		t.Fatalf("未绑定材料时不得调用向量检索，实际 %d 次", vecs.calls)
	}
	if len((*eventsPtr)[0].Citations) != 0 || len(out.Citations) != 0 {
		t.Fatal("未绑定材料时引用必须为空")
	}
	if !strings.Contains(stream.gotMessages[0].Content, noMaterialGuard) {
		t.Fatal("未绑定材料时必须注入反编造护栏")
	}
	if len(stream.gotMessages) != 2 {
		t.Fatal("未绑定材料时仍必须调用 LLM")
	}
}

// ---------- 2.5 searchClass：绑定白名单 + 阈值 ----------

func TestSearchClassFiltersByBindingAndThreshold(t *testing.T) {
	embedder := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	vecs := &fakeVectors{hits: []vector.Hit{
		{ID: "bound-hi", Score: 0.9, Payload: vector.Payload{DocumentID: 100, FileName: "讲义.txt"}},
		{ID: "unbound", Score: 0.95, Payload: vector.Payload{DocumentID: 999, FileName: "他材料.txt"}},
		{ID: "bound-low", Score: 0.2, Payload: vector.Payload{DocumentID: 100, FileName: "讲义.txt"}},
	}}
	svc := &Service{Embedder: embedder, Vectors: vecs, TopK: 5}

	hits, err := svc.searchClass(context.Background(), "q", 10, []int64{100}, 0.5)
	if err != nil {
		t.Fatalf("searchClass 失败: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "bound-hi" {
		t.Fatalf("应仅保留绑定材料且高于阈值的分片: %+v", hits)
	}

	// 空绑定集合：不检索，返回空。
	hits, err = svc.searchClass(context.Background(), "q", 10, nil, 0.5)
	if err != nil {
		t.Fatalf("searchClass 失败: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("空绑定集合必须返回空命中: %+v", hits)
	}
}

// 阈值过滤：全部分片低于下限 → 空命中。
func TestSearchClassThresholdRemovesAll(t *testing.T) {
	svc := &Service{
		Embedder: &fakeEmbedder{vec: []float32{0.1}},
		Vectors: &fakeVectors{hits: []vector.Hit{
			{ID: "a", Score: 0.3, Payload: vector.Payload{DocumentID: 100}},
			{ID: "b", Score: 0.4, Payload: vector.Payload{DocumentID: 100}},
		}},
		TopK: 5,
	}
	hits, err := svc.searchClass(context.Background(), "q", 10, []int64{100}, 0.5)
	if err != nil {
		t.Fatalf("searchClass 失败: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("低于下限的分片必须全部丢弃: %+v", hits)
	}
}

// ---------- 3.4 mapCitations 稳定排序与去重 ----------

func TestMapCitationsStableSortAndDedupe(t *testing.T) {
	hits := []vector.Hit{
		{ID: "d2-low", Score: 0.5, Payload: vector.Payload{DocumentID: 2, FileName: "b.txt"}},
		{ID: "d1-low", Score: 0.5, Payload: vector.Payload{DocumentID: 1, FileName: "a.txt"}},
		{ID: "d1-hi", Score: 0.9, Payload: vector.Payload{DocumentID: 1, FileName: "a.txt"}},
	}
	first := mapCitations(hits)
	second := mapCitations(hits)

	if len(first) != 2 {
		t.Fatalf("按 DocumentID 去重后应剩 2 条: %+v", first)
	}
	// 排序：d1(0.9) → d2(0.5)；index 自 1 递增。
	if first[0].DocumentID != 1 || first[0].Index != 1 || first[0].Score != 0.9 {
		t.Fatalf("首条应为 d1 最高分且 index=1: %+v", first[0])
	}
	if first[1].DocumentID != 2 || first[1].Index != 2 {
		t.Fatalf("次条应为 d2 且 index=2: %+v", first[1])
	}
	// 同一输入多次调用得到同一顺序与 index。
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("mapCitations 不稳定: %+v vs %+v", first, second)
		}
	}
}

// ---------- 3.3 buildSystem 两分支 ----------

func TestBuildSystemWithAndWithoutCitations(t *testing.T) {
	hit := guidingSkill()
	withMat := buildSystem("提示词", &hit, []Citation{{Index: 1, FileName: "a.txt", Snippet: "片段"}})
	if !strings.Contains(withMat, "提示词") || !strings.Contains(withMat, skillInstruction) ||
		!strings.Contains(withMat, materialContextNote) || !strings.Contains(withMat, "[1]") {
		t.Fatalf("有材料分支错误: %q", withMat)
	}
	if strings.Contains(withMat, noMaterialGuard) {
		t.Fatalf("有材料时不应注入护栏: %q", withMat)
	}

	noMat := buildSystem("提示词", &hit, nil)
	if !strings.Contains(noMat, noMaterialGuard) || strings.Contains(noMat, materialContextNote) {
		t.Fatalf("落空分支错误: %q", noMat)
	}
	// 无命中技能（零命中）时不并入技能内容。
	noSkill := buildSystem("提示词", nil, nil)
	if strings.Contains(noSkill, skillInstruction) {
		t.Fatalf("零命中不得并入技能内容: %q", noSkill)
	}
}

// ---------- 命中技能为空内容的兜底 ----------

// 内置「解题引导」正文缺失（存量库未播种）时，buildSystem 用默认指令兜底。
// 注：词法路由要求技能带「适用条件」才可能命中，故该兜底经 buildSystem 直接验证。
func TestBuildSystemGuidingSkillEmptyContentFallback(t *testing.T) {
	hit := Skill{ID: 7, Name: guidingSkillName, Enabled: true} // Content 为空
	sys := buildSystem("提示词", &hit, nil)
	if !strings.Contains(sys, skillInstruction) {
		t.Fatalf("内置技能正文为空时必须用默认指令兜底: %q", sys)
	}
}

// ---------- 历史按序带入 ----------

func TestAskStreamHistoryBroughtInOrder(t *testing.T) {
	repo := &fakeRepo{
		conv:     &Conversation{ID: 1, AgentID: 5},
		agentCfg: baseAgentConfig(),
		history: []Message{
			{ID: 10, Role: RoleUser, Content: "第一问"},
			{ID: 11, Role: RoleAssistant, Content: "第一答"},
		},
	}
	stream := &fakeStreamLLM{deltas: []string{"承接上文"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合第三步", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}

	if len(stream.gotMessages) != 4 {
		t.Fatalf("消息数错误: %d", len(stream.gotMessages))
	}
	wantRoles := []string{"system", "user", "assistant", "user"}
	wantContents := []string{"", "第一问", "第一答", "什么是集合第三步"}
	for i := range wantRoles {
		if stream.gotMessages[i].Role != wantRoles[i] {
			t.Fatalf("第 %d 条角色错误: %+v", i, stream.gotMessages[i])
		}
		if i > 0 && stream.gotMessages[i].Content != wantContents[i] {
			t.Fatalf("第 %d 条内容错误: %+v", i, stream.gotMessages[i])
		}
	}
}

// ---------- 提示词为空时兜底 ----------

func TestAskStreamFallbackPrompt(t *testing.T) {
	cfg := baseAgentConfig()
	cfg.SystemPrompt = "   "
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"回答"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	sys := stream.gotMessages[0].Content
	if !strings.Contains(sys, fallbackSystemPrompt) {
		t.Fatalf("空提示词必须兜底: %q", sys)
	}
	if repo.appended[1].m.PromptUsed != fallbackSystemPrompt {
		t.Fatalf("快照应为兜底提示词，得到 %q", repo.appended[1].m.PromptUsed)
	}
}

// 智能体不存在（会话绑定的智能体被删或被跨班）：直接失败，不推送事件。
func TestAskStreamAgentNotFound(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, cfgErr: ErrAgentNotFound}
	stream := &fakeStreamLLM{deltas: []string{"x"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	_, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent))
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("应返回 ErrAgentNotFound，得到 %v", err)
	}
	if len(*eventsPtr) != 0 {
		t.Fatalf("配置失败不应推送任何事件: %v", *eventsPtr)
	}
	if len(stream.gotMessages) != 0 {
		t.Fatal("配置失败不应调用 LLM")
	}
}

// ---------- 客户端中断：部分持久化，静默，不发 error ----------

func TestAskStreamClientInterruptPersistsPartial(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: []string{"已写部分"}, blockToCancel: true}
	svc, _ := newHarness(repo, sampleHits(), stream)

	ctx, cancel := context.WithCancel(context.Background())
	eventsPtr, onEvent := collectEvents()

	wrappedOnEvent := func(e Event) {
		onEvent(e)
		if e.Type == EventDelta {
			cancel()
		}
	}

	out, err := svc.AskStream(ctx, defaultAskInput("什么是集合", wrappedOnEvent))
	if err == nil {
		t.Fatal("中断必须返回错误")
	}
	if out != nil {
		t.Fatal("中断不应返回成功输出")
	}

	events := *eventsPtr
	gotTypes := make([]string, len(events))
	for i, e := range events {
		gotTypes[i] = e.Type
	}
	if strings.Join(gotTypes, ",") != "meta,delta" {
		t.Fatalf("中断事件序列错误: %v", gotTypes)
	}

	if len(repo.appended) != 2 {
		t.Fatalf("应持久化 user 与部分 assistant，实际 %d", len(repo.appended))
	}
	a := repo.appended[1].m
	if a.Role != RoleAssistant || a.Content != "已写部分" ||
		len(a.Citations) != 1 || a.PromptUsed == "" || len(a.SkillsUsed) != 1 {
		t.Fatalf("中断部分持久化错误: %+v", a)
	}
}

func TestAskStreamClientInterruptEmptyNoMessage(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: nil, blockToCancel: true}
	svc, _ := newHarness(repo, sampleHits(), stream)

	ctx, cancel := context.WithCancel(context.Background())
	eventsPtr, onEvent := collectEvents()
	wrapped := func(e Event) {
		onEvent(e)
		cancel()
	}

	if _, err := svc.AskStream(ctx, defaultAskInput("什么是集合", wrapped)); err == nil {
		t.Fatal("中断必须返回错误")
	}
	events := *eventsPtr
	if len(events) != 1 || events[0].Type != EventMeta {
		t.Fatalf("应仅推送 meta，得到 %v", events)
	}
	if len(repo.appended) != 1 || repo.appended[0].m.Role != RoleUser {
		t.Fatalf("空文本中断不落 assistant，实际 %+v", repo.appended)
	}
}

// ---------- 生成中途失败：error 事件 + 友好文案 ----------

func TestAskStreamLLMFailureEmptyFriendlyError(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{failErr: errors.New("上游 500")}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合", onEvent))
	if err == nil || out != nil {
		t.Fatal("生成失败必须返回错误且无输出")
	}
	events := *eventsPtr
	if len(events) != 2 || events[0].Type != EventMeta || events[1].Type != EventError {
		t.Fatalf("事件序列应为 meta,error: %v", events)
	}
	if events[1].ErrorMessage != friendlyErrorText {
		t.Fatalf("错误文案不友好: %q", events[1].ErrorMessage)
	}
	if len(repo.appended) != 1 {
		t.Fatalf("空文本失败不应持久化 assistant: %+v", repo.appended)
	}
}

func TestAskStreamLLMFailureWithPartial(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1, AgentID: 5}, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: []string{"半截回答"}, failErr: errors.New("断流")}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合", onEvent)); err == nil {
		t.Fatal("必须返回错误")
	}
	events := *eventsPtr
	gotTypes := make([]string, len(events))
	for i, e := range events {
		gotTypes[i] = e.Type
	}
	if strings.Join(gotTypes, ",") != "meta,delta,error" {
		t.Fatalf("事件序列错误: %v", gotTypes)
	}
	if len(repo.appended) != 2 || repo.appended[1].m.Content != "半截回答" {
		t.Fatalf("部分回答应持久化: %+v", repo.appended)
	}
}

// ---------- 他人/不存在会话：直接失败 ----------

func TestAskStreamConversationNotFound(t *testing.T) {
	repo := &fakeRepo{convErr: ErrConversationNotFound, agentCfg: baseAgentConfig()}
	stream := &fakeStreamLLM{deltas: []string{"x"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	_, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent))
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("应返回 ErrConversationNotFound，得到 %v", err)
	}
	if len(*eventsPtr) != 0 {
		t.Fatalf("属主失败不应推送任何事件: %v", *eventsPtr)
	}
	if len(stream.gotMessages) != 0 {
		t.Fatal("属主失败不应调用 LLM")
	}
}
