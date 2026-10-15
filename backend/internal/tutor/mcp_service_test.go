package tutor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"campusclaw/internal/mcp"
)

// ---------- 6.x 工具阶段测试替身 ----------

// fakeMCPRepo 是 MCPRepo 的内存实现。
type fakeMCPRepo struct {
	mu         sync.Mutex
	servers    map[string]*MCPServer
	getErr     error
	calls      []MCPCallInput
	gotClassID int64
}

func newFakeMCPRepo(servers ...*MCPServer) *fakeMCPRepo {
	m := &fakeMCPRepo{servers: map[string]*MCPServer{}}
	for _, s := range servers {
		m.servers[s.Name] = s
	}
	return m
}

func (r *fakeMCPRepo) GetMCPServerByName(_ context.Context, classID int64, name string) (*MCPServer, error) {
	r.mu.Lock()
	r.gotClassID = classID
	r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	s, ok := r.servers[name]
	if !ok {
		return nil, ErrMCPServerNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *fakeMCPRepo) RecordMCPCall(_ context.Context, in MCPCallInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, in)
	return nil
}

func (r *fakeMCPRepo) recorded() []MCPCallInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]MCPCallInput(nil), r.calls...)
}

// callArg 记录一次远端调用入参。
type callArg struct {
	name string
	args map[string]any
}

// fakeMCPCaller 是 MCPCaller 的替身：可返回文本、返回错误或阻塞到超时。
type fakeMCPCaller struct {
	mu    sync.Mutex
	text  string
	err   error
	block bool
	calls []callArg
}

func (c *fakeMCPCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	c.mu.Lock()
	c.calls = append(c.calls, callArg{name: name, args: args})
	c.mu.Unlock()
	if c.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if c.err != nil {
		return "", c.err
	}
	return c.text, nil
}

func (c *fakeMCPCaller) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// connectedServer 构造一台已连接且清单含指定工具的服务器。
func connectedServer(tools ...string) *MCPServer {
	ts := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		ts = append(ts, mcp.Tool{Name: t, Description: t + " 描述"})
	}
	return &MCPServer{
		ID: 42, Name: "campus-tools", BaseURL: "http://mcp-demo:8090",
		AuthToken: "secret-token", HasAuth: true, Connected: true, Tools: ts,
	}
}

// toolSkill 构造一条带「工具」声明的已启用技能；内容须自带「适用条件」才会被词法路由命中。
func toolSkill(id int64, name, content string) Skill {
	return Skill{ID: id, Name: name, Content: content, Source: SkillSourceOnline, Enabled: true}
}

// routedToolSkill 构造一条「工具」声明 + 指定适用条件的技能（问题含该条件即命中）。
func routedToolSkill(id int64, name, ref, condition string) Skill {
	return toolSkill(id, name, "工具："+ref+"\n适用条件："+condition)
}

// newToolService 装配带 MCP 能力的服务；MCPDial 恒返回 caller。
func newToolService(skills []Skill, mrepo *fakeMCPRepo, caller MCPCaller, timeout time.Duration) (*Service, *fakeRepo, *fakeStreamLLM) {
	cfg := baseAgentConfig()
	cfg.Skills = skills
	repo := &fakeRepo{conv: &Conversation{ID: 1}, agentCfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"回答"}}
	svc, _ := newHarness(repo, sampleHits(), stream)
	svc.MCPRepo = mrepo
	svc.MCPDial = func(baseURL, token string, timeout time.Duration) MCPCaller { return caller }
	svc.MCPTimeout = timeout
	return svc, repo, stream
}

// ---------- 6.1 引用收集：同技能内去重、路由命中决定是否调用 ----------

// 同一技能内声明重复引用时只调用一次。
func TestToolPhaseDedupSingleCall(t *testing.T) {
	skills := []Skill{toolSkill(1, "s1",
		"工具：formula_hint@campus-tools, formula_hint@campus-tools\n适用条件：题目")}
	caller := &fakeMCPCaller{text: "公式提示"}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, _, _ := newToolService(skills, mrepo, caller, time.Second)

	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("AskStream 失败: %v", err)
	}
	meta := (*eventsPtr)[0]
	if len(meta.ToolCalls) != 1 {
		t.Fatalf("去重后应只有 1 条工具状态，得到 %+v", meta.ToolCalls)
	}
	if caller.count() != 1 {
		t.Fatalf("远端应只调用一次，实际 %d", caller.count())
	}
	if mrepo.gotClassID != 10 {
		t.Fatalf("读取服务器必须用本班 classID，得到 %d", mrepo.gotClassID)
	}
}

// 路由命中决定是否进入工具阶段：未声明条件/条件未命中即零命中，不解析、不调用、不审计。
func TestToolPhaseConditionGate(t *testing.T) {
	t.Run("未声明条件即零命中不调用", func(t *testing.T) {
		caller := &fakeMCPCaller{text: "数据"}
		mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
		svc, _, _ := newToolService([]Skill{toolSkill(1, "s", "工具：formula_hint@campus-tools")}, mrepo, caller, time.Second)
		eventsPtr, onEvent := collectEvents()
		if _, err := svc.AskStream(context.Background(), defaultAskInput("任意问题", onEvent)); err != nil {
			t.Fatalf("失败: %v", err)
		}
		if caller.count() != 0 {
			t.Fatalf("零命中不得调用，实际 %d", caller.count())
		}
		if len((*eventsPtr)[0].ToolCalls) != 0 {
			t.Fatalf("零命中不应有工具状态: %+v", (*eventsPtr)[0].ToolCalls)
		}
		if got := mrepo.recorded(); len(got) != 0 {
			t.Fatalf("零命中不得写审计，得到 %+v", got)
		}
	})

	t.Run("条件未命中不调用", func(t *testing.T) {
		caller := &fakeMCPCaller{text: "数据"}
		mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
		svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "函数")},
			mrepo, caller, time.Second)
		eventsPtr, onEvent := collectEvents()
		if _, err := svc.AskStream(context.Background(), defaultAskInput("什么是集合", onEvent)); err != nil {
			t.Fatalf("失败: %v", err)
		}
		if caller.count() != 0 {
			t.Fatalf("条件未命中不得调用，实际 %d", caller.count())
		}
		if len((*eventsPtr)[0].ToolCalls) != 0 {
			t.Fatalf("条件未命中的引用不应出现在 meta: %+v", (*eventsPtr)[0].ToolCalls)
		}
		if got := mrepo.recorded(); len(got) != 0 {
			t.Fatalf("条件未命中不得写审计，得到 %+v", got)
		}
	})

	t.Run("条件命中调用并写审计", func(t *testing.T) {
		caller := &fakeMCPCaller{text: "数据"}
		mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
		svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "函数")},
			mrepo, caller, time.Second)
		eventsPtr, onEvent := collectEvents()
		if _, err := svc.AskStream(context.Background(), defaultAskInput("求函数的定义域", onEvent)); err != nil {
			t.Fatalf("失败: %v", err)
		}
		if caller.count() != 1 {
			t.Fatalf("条件命中应调用一次，实际 %d", caller.count())
		}
		if len((*eventsPtr)[0].ToolCalls) != 1 {
			t.Fatalf("meta 应含 1 条工具状态: %+v", (*eventsPtr)[0].ToolCalls)
		}
		if got := mrepo.recorded(); len(got) != 1 {
			t.Fatalf("命中应写 1 条审计，得到 %+v", got)
		}
	})
}

// ---------- 6.2 数据块注入（系统提示词不变、落库仍为原始问题） ----------

func TestToolDataInjectedButPersistOriginal(t *testing.T) {
	question := "求集合 {1,2} 的子集个数"
	caller := &fakeMCPCaller{text: "公式提示：子集个数 = 2^n"}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, repo, stream := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "集合")}, mrepo, caller, time.Second)
	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput(question, onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}

	// 发给模型的 user 消息含定界数据块与工具引用标注。
	var lastUser string
	for _, m := range stream.gotMessages {
		if m.Role == RoleUser {
			lastUser = m.Content
		}
	}
	if !strings.Contains(lastUser, toolDataNote) {
		t.Fatalf("user 消息必须含数据块定界注记: %q", lastUser)
	}
	if !strings.Contains(lastUser, "【工具引用：formula_hint@campus-tools】") ||
		!strings.Contains(lastUser, "子集个数 = 2^n") {
		t.Fatalf("user 消息必须含工具引用与结果: %q", lastUser)
	}
	if !strings.HasPrefix(lastUser, question) {
		t.Fatalf("user 消息必须以原始问题开头: %q", lastUser)
	}

	// system 消息不得注入工具数据。
	sys := stream.gotMessages[0]
	if strings.Contains(sys.Content, toolDataNote) || strings.Contains(sys.Content, "子集个数 = 2^n") {
		t.Fatalf("工具数据不得进入 system: %q", sys.Content)
	}

	// 落库的 user 消息仍为原始问题。
	if got := repo.appended[0].m.Content; got != question {
		t.Fatalf("落库 user 消息应为原始问题，得到 %q", got)
	}
}

// ---------- 6.3 meta toolCalls ----------

func TestToolCallsInMeta(t *testing.T) {
	caller := &fakeMCPCaller{text: "数据"}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)

	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	tc := (*eventsPtr)[0].ToolCalls
	if len(tc) != 1 {
		t.Fatalf("meta 应含 1 条工具状态: %+v", tc)
	}
	if tc[0].ToolRef != "formula_hint@campus-tools" || tc[0].ServerName != "campus-tools" ||
		tc[0].Status != MCPStatusSuccess || tc[0].Error != "" {
		t.Fatalf("工具状态字段错误: %+v", tc[0])
	}

	// 不得泄露令牌与内部地址。
	raw, _ := json.Marshal((*eventsPtr)[0])
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "mcp-demo:8090") {
		t.Fatalf("meta 不得泄露令牌或内部地址: %s", raw)
	}
}

// ---------- 6.4 审计三态 + 已删除服务器不写审计 ----------

func TestAuditSuccess(t *testing.T) {
	caller := &fakeMCPCaller{text: "数据"}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	got := mrepo.recorded()
	if len(got) != 1 {
		t.Fatalf("成功应写 1 条审计，得到 %d", len(got))
	}
	c := got[0]
	if c.Status != MCPStatusSuccess || c.ClassID != 10 || c.UserID != 2 ||
		c.ConversationID != 1 || c.ServerID != 42 || c.ServerName != "campus-tools" ||
		c.ToolRef != "formula_hint@campus-tools" || c.Error != "" {
		t.Fatalf("审计字段错误: %+v", c)
	}
}

func TestAuditDeniedNotConnected(t *testing.T) {
	srv := connectedServer("formula_hint")
	srv.Connected = false
	caller := &fakeMCPCaller{text: "数据"}
	mrepo := newFakeMCPRepo(srv)
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	got := mrepo.recorded()
	if len(got) != 1 || got[0].Status != MCPStatusDenied || got[0].Error != "服务器未连接" {
		t.Fatalf("未连接应记 denied，得到 %+v", got)
	}
	if caller.count() != 0 {
		t.Fatal("未连接不得发起远端调用")
	}
	if (*eventsPtr)[0].ToolCalls[0].Status != MCPStatusDenied {
		t.Fatalf("meta 状态应为 denied: %+v", (*eventsPtr)[0].ToolCalls)
	}
}

func TestAuditDeniedToolMissing(t *testing.T) {
	caller := &fakeMCPCaller{text: "数据"}
	mrepo := newFakeMCPRepo(connectedServer("unit_convert")) // 清单不含 formula_hint
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	got := mrepo.recorded()
	if len(got) != 1 || got[0].Status != MCPStatusDenied || got[0].Error != "工具不在服务器清单中" {
		t.Fatalf("工具缺失应记 denied，得到 %+v", got)
	}
}

// 引用服务器已在本班删除：记为 denied 且不写审计。
func TestAuditDeletedServerNoRecord(t *testing.T) {
	caller := &fakeMCPCaller{text: "数据"}
	mrepo := newFakeMCPRepo() // 无任何服务器 → ErrMCPServerNotFound
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if got := mrepo.recorded(); len(got) != 0 {
		t.Fatalf("已删除服务器不得写审计，得到 %+v", got)
	}
	tc := (*eventsPtr)[0].ToolCalls
	if len(tc) != 1 || tc[0].Status != MCPStatusDenied || tc[0].Error != "服务器不存在" {
		t.Fatalf("应为 denied 且原因服务器不存在: %+v", tc)
	}
}

func TestAuditErrorRemoteCall(t *testing.T) {
	caller := &fakeMCPCaller{err: errors.New("远端 500")}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("远端失败不应中断回答: %v", err)
	}
	got := mrepo.recorded()
	if len(got) != 1 || got[0].Status != MCPStatusError || got[0].ServerID != 42 {
		t.Fatalf("远端失败应记 error，得到 %+v", got)
	}
	if (*eventsPtr)[0].ToolCalls[0].Status != MCPStatusError {
		t.Fatalf("meta 状态应为 error: %+v", (*eventsPtr)[0].ToolCalls)
	}
}

func TestAuditErrorReadServer(t *testing.T) {
	caller := &fakeMCPCaller{text: "x"}
	mrepo := newFakeMCPRepo()
	mrepo.getErr = errors.New("数据库故障")
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	_, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	got := mrepo.recorded()
	if len(got) != 1 || got[0].Status != MCPStatusError || got[0].Error != "读取服务器失败" {
		t.Fatalf("读取失败应记 error，得到 %+v", got)
	}
}

// ---------- 6.5 失败与超时不阻断 ----------

func TestToolFailureDoesNotBlockAnswer(t *testing.T) {
	caller := &fakeMCPCaller{err: errors.New("远端 500")}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, _, stream := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, time.Second)
	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent))
	if err != nil || out == nil {
		t.Fatalf("工具失败不应中断：err=%v out=%v", err, out)
	}
	types := eventTypes(*eventsPtr)
	if types[len(types)-1] != EventDone {
		t.Fatalf("应仍收到 done，事件序列: %v", types)
	}
	if len(stream.gotMessages) == 0 {
		t.Fatal("仍应调用 LLM")
	}
}

func TestToolTimeoutDoesNotBlockAnswer(t *testing.T) {
	caller := &fakeMCPCaller{block: true} // 阻塞直到超时
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, _, _ := newToolService([]Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}, mrepo, caller, 20*time.Millisecond)
	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent))
	if err != nil || out == nil {
		t.Fatalf("超时不应中断：err=%v out=%v", err, out)
	}
	types := eventTypes(*eventsPtr)
	if types[len(types)-1] != EventDone {
		t.Fatalf("超时应仍收到 done，事件序列: %v", types)
	}
	if got := mrepo.recorded(); len(got) != 1 || got[0].Status != MCPStatusError {
		t.Fatalf("超时应记 error 审计，得到 %+v", got)
	}
}

// ---------- 内置引用与未启用能力 ----------

// knowledge_search 为内置引用：记成功且不发起远端调用、不注入数据块。
func TestBuiltinKnowledgeSearchRef(t *testing.T) {
	caller := &fakeMCPCaller{text: "不应被调用"}
	mrepo := newFakeMCPRepo(connectedServer("formula_hint"))
	svc, repo, stream := newToolService([]Skill{routedToolSkill(1, "s", "knowledge_search", "题目")}, mrepo, caller, time.Second)
	eventsPtr, onEvent := collectEvents()
	if _, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent)); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if caller.count() != 0 {
		t.Fatal("内置引用不得发起远端调用")
	}
	if got := mrepo.recorded(); len(got) != 0 {
		t.Fatalf("内置引用不写审计，得到 %+v", got)
	}
	tc := (*eventsPtr)[0].ToolCalls
	if len(tc) != 1 || tc[0].Status != MCPStatusSuccess || tc[0].ServerName != "" {
		t.Fatalf("内置引用状态错误: %+v", tc)
	}
	// 无数据块注入：落库仍为原始问题，发给模型的 user 无定界注记。
	if repo.appended[0].m.Content != "题目" {
		t.Fatalf("落库应为原始问题: %q", repo.appended[0].m.Content)
	}
	var lastUser string
	for _, m := range stream.gotMessages {
		if m.Role == RoleUser {
			lastUser = m.Content
		}
	}
	if strings.Contains(lastUser, toolDataNote) {
		t.Fatalf("内置引用不应注入数据块: %q", lastUser)
	}
}

// MCP 能力未启用（无 dial）：引用记 denied，但回答继续。
func TestMCPDisabledDenied(t *testing.T) {
	cfg := baseAgentConfig()
	cfg.Skills = []Skill{routedToolSkill(1, "s", "formula_hint@campus-tools", "题目")}
	repo := &fakeRepo{conv: &Conversation{ID: 1}, agentCfg: cfg}
	stream := &fakeStreamLLM{deltas: []string{"回答"}}
	svc, _ := newHarness(repo, sampleHits(), stream) // 未设置 MCPRepo/MCPDial
	eventsPtr, onEvent := collectEvents()
	out, err := svc.AskStream(context.Background(), defaultAskInput("题目", onEvent))
	if err != nil || out == nil {
		t.Fatalf("能力未启用不应中断: err=%v", err)
	}
	tc := (*eventsPtr)[0].ToolCalls
	if len(tc) != 1 || tc[0].Status != MCPStatusDenied || tc[0].Error != "MCP 能力未启用" {
		t.Fatalf("未启用能力应记 denied: %+v", tc)
	}
	if eventTypes(*eventsPtr)[len(*eventsPtr)-1] != EventDone {
		t.Fatal("应仍收到 done")
	}
}

// eventTypes 提取事件类型序列。
func eventTypes(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
