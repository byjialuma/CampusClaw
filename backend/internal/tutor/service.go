package tutor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"campusclaw/internal/embedding"
	"campusclaw/internal/llm"
	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// SSE 事件类型（协议见设计 §6）。
const (
	EventMeta  = "meta"  // 生成开始前推送引用列表与技能状态
	EventDelta = "delta" // 回答文本逐段增量
	EventDone  = "done"  // 持久化完成
	EventError = "error" // 中途失败的友好错误
)

const (
	// guidingSkillName 是内置技能名（与 seed.tutorSkillName 一致）。
	guidingSkillName = "解题引导"
	// fallbackSystemPrompt 提示词列为空时的内置兜底（与 seed.defaultSystemPrompt 一致）。
	fallbackSystemPrompt = "你是本班解题助教，结合班级材料引导学生自主解题，用中文，简洁准确。"
	// skillInstruction 技能开启时附加到 system 的引导指令：给思路、引材料、不给最终答案。
	skillInstruction = "学生正在解题，请给出解题思路与步骤引导，引用材料中的相关内容（含材料标题与位置），不要直接给出该题的最终答案。"
	// noHitNote 无命中时注入 system 的防编造注记。
	noHitNote = "知识库中未找到与本题相关的内容，回答时必须明确说明\"资料中未找到\"，不得引用或编造任何材料出处。"
)

// friendlyErrorText 是 error 事件对外文案：不泄露内部服务地址。
const friendlyErrorText = "助手暂不可用，请稍后再试"

// persistTimeout 是客户端中断后兜底持久化的独立超时（不随请求 context 取消）。
const persistTimeout = 10 * time.Second

// Event 是流式提问过程中推给前端的一个事件。
type Event struct {
	Type         string
	Citations    []Citation
	SkillEnabled bool
	Text         string
	MessageID    int64
	ErrorMessage string
}

// AskInput 是 AskStream 的入参；ClassID/UserID 必须由 handler 从 JWT claims 填入。
type AskInput struct {
	ConversationID int64
	UserID         int64
	ClassID        int64
	Question       string
	OnEvent        func(Event)
}

// AskOutput 是流式提问正常结束后的完整结果。
type AskOutput struct {
	Answer       string
	Citations    []Citation
	SkillEnabled bool
	MessageID    int64
}

// Repo 是编排服务所需的持久化能力（*TutorRepository 满足）。
type Repo interface {
	ConversationForUser(ctx context.Context, convID, userID int64) (*Conversation, error)
	AssistantConfig(ctx context.Context, classID int64) (*AssistantConfig, error)
	MessagesByConversation(ctx context.Context, convID int64) ([]Message, error)
	RenameConversationIfEmpty(ctx context.Context, convID int64, question string) error
	AppendMessage(ctx context.Context, convID int64, m NewMessage) (int64, error)
}

// vectorSearcher 是班级内向量召回能力（*vector.Client 满足）。
type vectorSearcher interface {
	Search(ctx context.Context, queryVec []float32, classID int64, topK int) ([]vector.Hit, error)
}

// Service 编排解题助手的一次完整提问：
// 属主校验 → 本班检索 → 提示词/技能加载 → 历史组装 → 流式生成 → 持久化。
type Service struct {
	Repo     Repo
	Embedder embedding.Embedder
	Vectors  vectorSearcher
	LLM      llm.StreamLLM
	TopK     int
}

// AskStream 执行流式提问。embedding/检索失败发生在 meta 之前，返回错误由 handler 映射 503；
// 生成开始后失败则推送 error 事件；客户端中断时停止推送，已积累文本非空仍持久化。
func (s *Service) AskStream(ctx context.Context, in AskInput) (*AskOutput, error) {
	emit := func(e Event) {
		if in.OnEvent != nil {
			in.OnEvent(e)
		}
	}

	// 0. 属主校验：他人/不存在会话在 SQL 层即按不存在处理（404）。
	if _, err := s.Repo.ConversationForUser(ctx, in.ConversationID, in.UserID); err != nil {
		return nil, err
	}

	// 1. 本班检索（问题向量化 → 强制 class_id 召回 → 过滤零相关度）。
	hits, err := s.searchClass(ctx, in.Question, in.ClassID)
	if err != nil {
		return nil, err
	}

	// 2. 加载提示词（空则内置兜底）；3. 加载解题引导技能状态。
	cfg, err := s.Repo.AssistantConfig(ctx, in.ClassID)
	if err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(cfg.SystemPrompt)
	if prompt == "" {
		prompt = fallbackSystemPrompt
	}
	skillEnabled := cfg.guidingEnabled()

	// 4. 读取历史（必须在写入本轮 user 消息之前），按时间升序。
	history, err := s.Repo.MessagesByConversation(ctx, in.ConversationID)
	if err != nil {
		return nil, err
	}

	// 5. 会话首问以问题前 30 字生成标题（此时尚无消息）。
	if err := s.Repo.RenameConversationIfEmpty(ctx, in.ConversationID, in.Question); err != nil {
		return nil, err
	}

	// 6. 持久化本轮用户提问。
	if _, err := s.Repo.AppendMessage(ctx, in.ConversationID, NewMessage{
		Role: RoleUser, Content: in.Question,
	}); err != nil {
		return nil, err
	}

	citations := mapCitations(hits)

	// 7. 组装 system（提示词 + 技能指令 + 材料上下文/无命中注记）与完整消息序列。
	systemContent := buildSystem(prompt, skillEnabled, hits)
	messages := make([]llm.Message, 0, len(history)+2)
	messages = append(messages, llm.Message{Role: "system", Content: systemContent})
	for _, h := range history {
		messages = append(messages, llm.Message{Role: h.Role, Content: h.Content})
	}
	messages = append(messages, llm.Message{Role: RoleUser, Content: in.Question})

	// 8. meta 先于回答文本推送（引用卡片可先行渲染）。
	emit(Event{Type: EventMeta, Citations: citations, SkillEnabled: skillEnabled})

	// 9. 流式生成，delta 透传。
	answer, streamErr := s.LLM.StreamAsk(ctx, messages, func(delta string) error {
		emit(Event{Type: EventDelta, Text: delta})
		return nil
	})

	if streamErr != nil {
		return s.finishWithError(ctx, in, emit, answer, citations, prompt, skillEnabled, streamErr)
	}

	// 10. 持久化完整回答 + 引用 + 提示词快照 + 技能状态。
	messageID, err := s.Repo.AppendMessage(ctx, in.ConversationID, NewMessage{
		Role:         RoleAssistant,
		Content:      answer,
		Citations:    citations,
		PromptUsed:   prompt,
		SkillEnabled: skillEnabled,
	})
	if err != nil {
		emit(Event{Type: EventError, ErrorMessage: friendlyErrorText})
		return nil, err
	}

	emit(Event{Type: EventDone, MessageID: messageID})
	return &AskOutput{
		Answer:       answer,
		Citations:    citations,
		SkillEnabled: skillEnabled,
		MessageID:    messageID,
	}, nil
}

// finishWithError 处理流式生成失败/中断：
// 已积累文本非空则持久化（引用与快照一并落库）；客户端中断不推送 error 事件。
func (s *Service) finishWithError(reqCtx context.Context, in AskInput, emit func(Event),
	answer string, citations []Citation, prompt string, skillEnabled bool, streamErr error) (*AskOutput, error) {
	// 客户端中断：请求 context 已取消。使用独立 context 兜底持久化，随后静默返回。
	if reqCtx.Err() != nil {
		if strings.TrimSpace(answer) != "" {
			_ = s.persistPartial(in.ConversationID, answer, citations, prompt, skillEnabled)
		}
		return nil, streamErr
	}

	// 生成中途失败：保留实际已生成的完整内容（若有，刷新后可回看），
	// 但不发送 done（避免前端误判成功），随后推送友好错误事件。
	if strings.TrimSpace(answer) != "" {
		_, _ = s.Repo.AppendMessage(reqCtx, in.ConversationID, NewMessage{
			Role:         RoleAssistant,
			Content:      answer,
			Citations:    citations,
			PromptUsed:   prompt,
			SkillEnabled: skillEnabled,
		})
	}
	emit(Event{Type: EventError, ErrorMessage: friendlyErrorText})
	return nil, streamErr
}

// persistPartial 以独立 context 持久化中断时已积累的回答（不随请求取消而失败）。
func (s *Service) persistPartial(convID int64, answer string, citations []Citation,
	prompt string, skillEnabled bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	_, err := s.Repo.AppendMessage(ctx, convID, NewMessage{
		Role:         RoleAssistant,
		Content:      answer,
		Citations:    citations,
		PromptUsed:   prompt,
		SkillEnabled: skillEnabled,
	})
	return err
}

// searchClass 向量化问题并在本班强制过滤召回，过滤 score <= 0 的无效命中。
func (s *Service) searchClass(ctx context.Context, question string, classID int64) ([]vector.Hit, error) {
	vecs, err := s.Embedder.Embed(ctx, []string{question})
	if err != nil {
		return nil, fmt.Errorf("问题向量化失败: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("向量化返回条数异常: %d", len(vecs))
	}
	hits, err := s.Vectors.Search(ctx, vecs[0], classID, s.TopK)
	if err != nil {
		return nil, fmt.Errorf("向量召回失败: %w", err)
	}
	filtered := make([]vector.Hit, 0, len(hits))
	for _, h := range hits {
		if h.Score <= 0 {
			continue
		}
		filtered = append(filtered, h)
	}
	return filtered, nil
}

// mapCitations 将命中分片映射为引用（含 snippet/locator/score），与检索结果严格一致。
func mapCitations(hits []vector.Hit) []Citation {
	citations := make([]Citation, 0, len(hits))
	for _, h := range hits {
		citations = append(citations, Citation{
			DocumentID: h.Payload.DocumentID,
			FileName:   h.Payload.FileName,
			FileType:   h.Payload.FileType,
			Snippet:    h.Payload.Text,
			Locator:    h.Payload.Locator,
			Score:      h.Score,
		})
	}
	return citations
}

// buildSystem 组装 system 消息：提示词 → 技能指令（开启时）→ 材料上下文或无命中注记。
func buildSystem(prompt string, skillEnabled bool, hits []vector.Hit) string {
	parts := []string{prompt}
	if skillEnabled {
		parts = append(parts, skillInstruction)
	}
	if len(hits) == 0 {
		parts = append(parts, noHitNote)
	} else {
		parts = append(parts, "以下是从本班知识库检索到的相关材料片段：\n"+formatHits(hits))
	}
	return strings.Join(parts, "\n\n")
}

// formatHits 按 `[文件名 定位] 片段` 列出材料上下文。
func formatHits(hits []vector.Hit) string {
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("[%s %s] %s",
			h.Payload.FileName, formatLocator(h.Payload.Locator), h.Payload.Text))
	}
	return strings.Join(lines, "\n")
}

// formatLocator 将分片定位信息格式化为可读文本（PDF 页码/MD 章节/TXT 行号）。
func formatLocator(l model.Locator) string {
	switch l.Kind {
	case model.LocatorPage:
		return fmt.Sprintf("第%d页", l.Page)
	case model.LocatorHeading:
		return fmt.Sprintf("章节: %s", l.Path)
	case model.LocatorLines:
		return fmt.Sprintf("第%d-%d行", l.StartLine, l.EndLine)
	default:
		return "未知位置"
	}
}

// guidingEnabled 读取内置「解题引导」技能状态；找不到同名技能时回退到首个技能。
func (c *AssistantConfig) guidingEnabled() bool {
	for _, sk := range c.Skills {
		if sk.Name == guidingSkillName {
			return sk.Enabled
		}
	}
	if len(c.Skills) > 0 {
		return c.Skills[0].Enabled
	}
	return false
}
