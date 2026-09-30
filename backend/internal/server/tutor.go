package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"campusclaw/internal/auth"
	tutorpkg "campusclaw/internal/tutor"
)

// tutorAsker 是流式提问编排能力（*tutor.Service 满足）。
type tutorAsker interface {
	AskStream(ctx context.Context, in tutorpkg.AskInput) (*tutorpkg.AskOutput, error)
}

// tutorStore 是会话/助手配置的直接 MySQL 读写（*tutor.TutorRepository 满足）。
type tutorStore interface {
	CreateConversation(ctx context.Context, classID, userID int64, title string) (int64, error)
	ConversationsByUser(ctx context.Context, userID int64) ([]tutorpkg.Conversation, error)
	ConversationForUser(ctx context.Context, convID, userID int64) (*tutorpkg.Conversation, error)
	MessagesByConversation(ctx context.Context, convID int64) ([]tutorpkg.Message, error)
	AssistantConfig(ctx context.Context, classID int64) (*tutorpkg.AssistantConfig, error)
	SavePrompt(ctx context.Context, classID int64, prompt string) error
	SetSkillEnabled(ctx context.Context, classID, skillID int64, enabled bool) error
}

// 新会话的初始标题（首问提交后由首问前 30 字覆盖）。
const newConversationTitle = "新的解题会话"

// promptMaxRunes 是教师提示词长度上限。
const promptMaxRunes = 2000

// ---------- 请求/响应载体 ----------

type createdConversationResponse struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type assistantResponse struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	SystemPrompt string           `json:"systemPrompt"`
	Skills       []tutorpkg.Skill `json:"skills"`
}

type savePromptRequest struct {
	SystemPrompt string `json:"systemPrompt"`
}

type setSkillRequest struct {
	Enabled bool `json:"enabled"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

// SSE 各事件的 data 载荷。
type sseMetaPayload struct {
	Citations    []tutorpkg.Citation `json:"citations"`
	SkillEnabled bool                `json:"skillEnabled"`
}
type sseDeltaPayload struct {
	Text string `json:"text"`
}
type sseDonePayload struct {
	MessageID int64 `json:"messageId"`
}
type sseErrorPayload struct {
	Error string `json:"error"`
}

// ---------- 会话 CRUD ----------

// createTutorConversation 创建会话；class_id/user_id 全部取自 JWT claims。
func (a *API) createTutorConversation(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, err := a.TutorStore.CreateConversation(r.Context(), u.ClassID, u.ID, newConversationTitle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	writeJSON(w, http.StatusCreated, createdConversationResponse{ID: id, Title: newConversationTitle})
}

// listTutorConversations 返回本人会话列表（新→旧）。
func (a *API) listTutorConversations(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	convs, err := a.TutorStore.ConversationsByUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取会话列表失败")
		return
	}
	writeJSON(w, http.StatusOK, convs)
}

// getTutorMessages 读取会话消息；属主不匹配（含他人/其他班）一律 404。
func (a *API) getTutorMessages(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	convID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	if _, err := a.TutorStore.ConversationForUser(r.Context(), convID, u.ID); err != nil {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	msgs, err := a.TutorStore.MessagesByConversation(r.Context(), convID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取消息失败")
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// ---------- SSE 流式提问 ----------

// askTutor 以 SSE 推送一次提问：meta（引用先行）→ delta（逐段）→ done/error。
func (a *API) askTutor(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	convID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}

	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeError(w, http.StatusBadRequest, "问题不能为空")
		return
	}
	if len([]rune(question)) > 500 {
		writeError(w, http.StatusBadRequest, "问题长度不能超过 500 字")
		return
	}

	// 属主校验必须在打开 SSE 之前完成，他人会话按 404 返回。
	if _, err := a.TutorStore.ConversationForUser(r.Context(), convID, u.ID); err != nil {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	sse := &sseWriter{w: w, flusher: flusher}

	_, err := a.Tutor.AskStream(r.Context(), tutorpkg.AskInput{
		ConversationID: convID,
		UserID:         u.ID,
		ClassID:        u.ClassID,
		Question:       question,
		OnEvent:        sse.write,
	})
	// 首个事件写出前失败（embedding/检索/配置）：响应头尚未发送，可直接 503。
	if err != nil && !sse.started {
		writeError(w, http.StatusServiceUnavailable, "解题助手暂不可用")
	}
}

// sseWriter 负责 SSE 响应头与逐事件写入；首个事件写出时才提交 200 与响应头。
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func (s *sseWriter) begin() {
	if s.started {
		return
	}
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 通知 Nginx 关闭本响应缓冲
	s.w.WriteHeader(http.StatusOK)
	s.started = true
}

// write 序列化并推送一个事件，随后立即 Flush。
func (s *sseWriter) write(e tutorpkg.Event) {
	s.begin()
	var data any
	switch e.Type {
	case tutorpkg.EventMeta:
		data = sseMetaPayload{Citations: e.Citations, SkillEnabled: e.SkillEnabled}
	case tutorpkg.EventDelta:
		data = sseDeltaPayload{Text: e.Text}
	case tutorpkg.EventDone:
		data = sseDonePayload{MessageID: e.MessageID}
	case tutorpkg.EventError:
		data = sseErrorPayload{Error: e.ErrorMessage}
	default:
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", e.Type, raw)
	s.flusher.Flush()
}

// ---------- 教师：提示词/技能管理 ----------

// getTutorAssistant 读取本班助手配置（仅教师）。
func (a *API) getTutorAssistant(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	cfg, err := a.TutorStore.AssistantConfig(r.Context(), u.ClassID)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrAssistantNotFound) {
			writeError(w, http.StatusNotFound, "本班助手不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "读取助手配置失败")
		return
	}
	writeJSON(w, http.StatusOK, assistantResponse{
		ID:           cfg.AssistantID,
		Name:         cfg.Name,
		SystemPrompt: cfg.SystemPrompt,
		Skills:       cfg.Skills,
	})
}

// saveTutorPrompt 保存本班助手提示词（仅教师）；下一轮提问起生效。
func (a *API) saveTutorPrompt(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	var req savePromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	prompt := strings.TrimSpace(req.SystemPrompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "提示词不能为空")
		return
	}
	if len([]rune(prompt)) > promptMaxRunes {
		writeError(w, http.StatusBadRequest, "提示词长度不能超过 2000 字")
		return
	}
	if err := a.TutorStore.SavePrompt(r.Context(), u.ClassID, prompt); err != nil {
		if errors.Is(err, tutorpkg.ErrAssistantNotFound) {
			writeError(w, http.StatusNotFound, "本班助手不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "保存提示词失败")
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// putTutorSkill 启用/停用技能（仅教师）；技能必须属于本班助手，否则 404。
func (a *API) putTutorSkill(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	skillID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "技能不存在")
		return
	}
	var req setSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.TutorStore.SetSkillEnabled(r.Context(), u.ClassID, skillID, req.Enabled); err != nil {
		if errors.Is(err, tutorpkg.ErrSkillNotFound) {
			writeError(w, http.StatusNotFound, "技能不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "更新技能失败")
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// parsePathID 解析正整数路径参数；非法/缺失返回 false（按资源不存在处理）。
func parsePathID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
