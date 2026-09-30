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

// fakeEmbedder 返回固定向量。
type fakeEmbedder struct{ vec []float32 }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
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
	gotClassID int64
	gotTopK    int
}

func (f *fakeVectors) Search(_ context.Context, _ []float32, classID int64, topK int) ([]vector.Hit, error) {
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
	cfg       *AssistantConfig
	cfgErr    error
	history   []Message
	renameErr error
	renamed   []string
	appended  []appendedMsg
	appendErr error
}

func (r *fakeRepo) ConversationForUser(_ context.Context, _, _ int64) (*Conversation, error) {
	return r.conv, r.convErr
}
func (r *fakeRepo) AssistantConfig(_ context.Context, _ int64) (*AssistantConfig, error) {
	return r.cfg, r.cfgErr
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

func baseConfig() *AssistantConfig {
	return &AssistantConfig{
		AssistantID:  5,
		Name:         "解题助手",
		SystemPrompt: "你是一位严格的数学助教。",
		Skills:       []Skill{{ID: 7, Name: "解题引导", Enabled: true}},
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

// ---------- 3.1/3.2 完整编排 ----------

func TestAskStreamFullOrchestration(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	stream := &fakeStreamLLM{deltas: []string{"思路一", "思路二"}}
	svc, vecs := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合？", onEvent))
	if err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	events := *eventsPtr

	// 输出：完整文本 + messageId（user=id1，assistant=id2）。
	if out.Answer != "思路一思路二" || out.MessageID != 2 {
		t.Fatalf("输出错误: %+v", out)
	}

	// 事件顺序：meta → delta ×2 → done。
	gotTypes := make([]string, len(events))
	for i, e := range events {
		gotTypes[i] = e.Type
	}
	if strings.Join(gotTypes, ",") != "meta,delta,delta,done" {
		t.Fatalf("事件顺序错误: %v", gotTypes)
	}

	// meta：引用完整映射（snippet/locator/score）+ 技能状态。
	meta := events[0]
	if !meta.SkillEnabled || len(meta.Citations) != 1 {
		t.Fatalf("meta 错误: %+v", meta)
	}
	c := meta.Citations[0]
	if c.DocumentID != 100 || c.FileName != "讲义.txt" || c.FileType != "txt" ||
		c.Snippet != "集合是具有某种特定性质的事物的总体" || c.Score != 0.91 ||
		c.Locator.Kind != model.LocatorLines || c.Locator.StartLine != 3 || c.Locator.EndLine != 5 {
		t.Fatalf("引用映射错误: %+v", c)
	}

	// delta 增量文本；done 的 messageId。
	if events[1].Text != "思路一" || events[2].Text != "思路二" {
		t.Fatal("delta 文本错误")
	}
	if events[3].MessageID != 2 {
		t.Fatal("done messageId 错误")
	}

	// 检索强制使用本班 classID（来自入参/claims）。
	if vecs.gotClassID != 10 || vecs.gotTopK != 5 {
		t.Fatalf("检索参数错误: class=%d topK=%d", vecs.gotClassID, vecs.gotTopK)
	}

	// 发给 LLM 的消息：system 首条含提示词、技能指令与材料；问题最后。
	if len(stream.gotMessages) != 2 {
		t.Fatalf("消息数错误: %d", len(stream.gotMessages))
	}
	sys := stream.gotMessages[0]
	if sys.Role != "system" || !strings.Contains(sys.Content, "严格的数学助教") ||
		!strings.Contains(sys.Content, "不要直接给出该题的最终答案") ||
		!strings.Contains(sys.Content, "讲义.txt") {
		t.Fatalf("system 组装错误: %q", sys.Content)
	}
	last := stream.gotMessages[1]
	if last.Role != RoleUser || last.Content != "什么是集合？" {
		t.Fatalf("末条消息错误: %+v", last)
	}

	// 持久化：先 user 后 assistant；assistant 带引用、提示词快照与技能状态。
	if len(repo.appended) != 2 {
		t.Fatalf("持久化次数错误: %d", len(repo.appended))
	}
	u := repo.appended[0].m
	if u.Role != RoleUser || u.Content != "什么是集合？" || u.PromptUsed != "" {
		t.Fatalf("user 消息错误: %+v", u)
	}
	a := repo.appended[1].m
	if a.Role != RoleAssistant || a.Content != "思路一思路二" ||
		len(a.Citations) != 1 || a.PromptUsed != "你是一位严格的数学助教。" || !a.SkillEnabled {
		t.Fatalf("assistant 消息错误: %+v", a)
	}

	// 首问生成标题。
	if len(repo.renamed) != 1 || repo.renamed[0] != "什么是集合？" {
		t.Fatalf("标题生成错误: %v", repo.renamed)
	}
}

// ---------- 技能开/关 system 差异 ----------

func TestAskStreamSkillDisabledDifference(t *testing.T) {
	cfg := baseConfig()
	cfg.Skills[0].Enabled = false
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"直接回答"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	events := *eventsPtr

	// meta 中技能状态为 false。
	if events[0].SkillEnabled {
		t.Fatal("技能应标记为停用")
	}
	// system 不含引导指令/不给最终答案约束。
	sys := stream.gotMessages[0].Content
	if strings.Contains(sys, "不要直接给出该题的最终答案") ||
		strings.Contains(sys, "解题思路与步骤引导") {
		t.Fatalf("停用时不应附加技能指令: %q", sys)
	}
	// 持久化快照为 false。
	if repo.appended[1].m.SkillEnabled {
		t.Fatal("持久化技能状态应为 false")
	}
}

func TestAskStreamSkillEnabledHasInstruction(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	stream := &fakeStreamLLM{deltas: []string{"引导"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	sys := stream.gotMessages[0].Content
	if !strings.Contains(sys, "解题思路与步骤引导") ||
		!strings.Contains(sys, "不要直接给出该题的最终答案") {
		t.Fatalf("开启时必须附加技能指令: %q", sys)
	}
}

// ---------- 无命中：注记 + 空引用 + 仍调 LLM ----------

func TestAskStreamNoHitsStillCallsLLM(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	stream := &fakeStreamLLM{deltas: []string{"资料中未找到相关内容"}}
	svc, _ := newHarness(repo, nil, stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("超纲题", onEvent))
	if err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	events := *eventsPtr

	// 引用为空。
	if len(events[0].Citations) != 0 || len(out.Citations) != 0 {
		t.Fatal("无命中时引用必须为空")
	}
	// system 含防编造注记。
	sys := stream.gotMessages[0].Content
	if !strings.Contains(sys, "资料中未找到") {
		t.Fatalf("无命中必须注入注记: %q", sys)
	}
	// LLM 仍被调用（system + user 两条消息）。
	if len(stream.gotMessages) != 2 {
		t.Fatalf("无命中也必须调用 LLM，消息数=%d", len(stream.gotMessages))
	}
	// assistant 消息持久化，引用为空数组。
	persisted := repo.appended[1].m
	if persisted.Role != RoleAssistant || len(persisted.Citations) != 0 {
		t.Fatalf("无命中 assistant 持久化错误: %+v", persisted)
	}
}

// ---------- 历史按序带入 ----------

func TestAskStreamHistoryBroughtInOrder(t *testing.T) {
	repo := &fakeRepo{
		conv: &Conversation{ID: 1},
		cfg:  baseConfig(),
		history: []Message{
			{ID: 10, Role: RoleUser, Content: "第一问"},
			{ID: 11, Role: RoleAssistant, Content: "第一答"},
		},
	}
	stream := &fakeStreamLLM{deltas: []string{"承接上文"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("第三步为什么", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}

	if len(stream.gotMessages) != 4 {
		t.Fatalf("消息数错误: %d", len(stream.gotMessages))
	}
	wantRoles := []string{"system", "user", "assistant", "user"}
	wantContents := []string{"", "第一问", "第一答", "第三步为什么"}
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
	cfg := baseConfig()
	cfg.SystemPrompt = "   "
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"回答"}}
	svc, _ := newHarness(repo, sampleHits(), stream)

	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
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

// ---------- 4.2 客户端中断：部分持久化，静默，不发 error ----------

func TestAskStreamClientInterruptPersistsPartial(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	stream := &fakeStreamLLM{deltas: []string{"已写部分"}, blockToCancel: true}
	svc, _ := newHarness(repo, sampleHits(), stream)

	ctx, cancel := context.WithCancel(context.Background())
	eventsPtr, onEvent := collectEvents()

	wrappedOnEvent := func(e Event) {
		onEvent(e)
		if e.Type == EventDelta {
			cancel() // 首个增量后客户端中断
		}
	}

	out, err := svc.AskStream(ctx, defaultAskInput("题目", wrappedOnEvent))
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
	// 只有 meta/delta：不推 error（已断开无法送达），也不推 done。
	if strings.Join(gotTypes, ",") != "meta,delta" {
		t.Fatalf("中断事件序列错误: %v", gotTypes)
	}

	// 已积累文本非空：持久化 assistant 消息（含引用与快照）。
	if len(repo.appended) != 2 {
		t.Fatalf("应持久化 user 与部分 assistant，实际 %d", len(repo.appended))
	}
	a := repo.appended[1].m
	if a.Role != RoleAssistant || a.Content != "已写部分" ||
		len(a.Citations) != 1 || a.PromptUsed == "" || !a.SkillEnabled {
		t.Fatalf("中断部分持久化错误: %+v", a)
	}
}

// 中断时无任何文本：不落 assistant 消息。
func TestAskStreamClientInterruptEmptyNoMessage(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	// 无增量、直接等待取消。
	stream := &fakeStreamLLM{deltas: nil, blockToCancel: true}
	svc, _ := newHarness(repo, sampleHits(), stream)

	ctx, cancel := context.WithCancel(context.Background())
	eventsPtr, onEvent := collectEvents()
	wrapped := func(e Event) {
		onEvent(e)
		cancel() // meta 后立即中断
	}

	if _, err := svc.AskStream(ctx, defaultAskInput("题目", wrapped)); err == nil {
		t.Fatal("中断必须返回错误")
	}
	events := *eventsPtr
	if len(events) != 1 || events[0].Type != EventMeta {
		t.Fatalf("应仅推送 meta，得到 %v", events)
	}
	// 只有 user 消息，无 assistant。
	if len(repo.appended) != 1 || repo.appended[0].m.Role != RoleUser {
		t.Fatalf("空文本中断不落 assistant，实际 %+v", repo.appended)
	}
}

// ---------- 生成中途失败：error 事件 + 友好文案 ----------

func TestAskStreamLLMFailureEmptyFriendlyError(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	stream := &fakeStreamLLM{failErr: errors.New("上游 500")}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent))
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
	// 无文本：不落 assistant。
	if len(repo.appended) != 1 {
		t.Fatalf("空文本失败不应持久化 assistant: %+v", repo.appended)
	}
}

// 中途失败但已有部分文本：保留内容并发 error（不发 done）。
func TestAskStreamLLMFailureWithPartial(t *testing.T) {
	repo := &fakeRepo{conv: &Conversation{ID: 1}, cfg: baseConfig()}
	stream := &fakeStreamLLM{deltas: []string{"半截回答"}, failErr: errors.New("断流")}
	svc, _ := newHarness(repo, sampleHits(), stream)

	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err == nil {
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
	repo := &fakeRepo{convErr: ErrConversationNotFound, cfg: baseConfig()}
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
