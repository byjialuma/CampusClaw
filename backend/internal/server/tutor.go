package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

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
	CreateSkill(ctx context.Context, classID int64, name, content, source string) (*tutorpkg.Skill, error)
	DeleteSkill(ctx context.Context, classID, skillID int64) error
}

// 新会话的初始标题（首问提交后由首问前 30 字覆盖）。
const newConversationTitle = "新的解题会话"

// promptMaxRunes 是教师提示词长度上限。
const promptMaxRunes = 2000

// 技能入参校验口径（设计 §6）。
const (
	skillNameMaxRunes    = 120
	skillContentMaxRunes = 20000
	skillUploadMaxBytes  = 64 * 1024 // 64KB
)

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

type createSkillRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

// SSE 各事件的 data 载荷。
type sseMetaPayload struct {
	Citations []tutorpkg.Citation `json:"citations"`
	Skills    []tutorpkg.SkillRef `json:"skills"`
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
		data = sseMetaPayload{Citations: e.Citations, Skills: e.Skills}
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

// createTutorSkill 在线新建技能文档（仅教师）；默认停用，由教师显式启用。
func (a *API) createTutorSkill(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	var req createSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name, content, errMsg := validateSkillInput(req.Name, req.Content)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	sk, err := a.TutorStore.CreateSkill(r.Context(), u.ClassID, name, content, tutorpkg.SkillSourceOnline)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrAssistantNotFound) {
			writeError(w, http.StatusNotFound, "本班助手不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "新建技能失败")
		return
	}
	writeJSON(w, http.StatusCreated, sk)
}

// uploadTutorSkill 上传 md/txt 文件创建技能（仅教师）；文件名去扩展名作为默认名称（可用 name 字段覆盖）。
func (a *API) uploadTutorSkill(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	// 先限制整体体积再解析，防止超大表单占用内存。
	r.Body = http.MaxBytesReader(w, r.Body, skillUploadMaxBytes+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少技能文件（表单字段 file）")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".md" && ext != ".txt" {
		writeError(w, http.StatusBadRequest, "技能文件仅支持 .md 或 .txt")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, skillUploadMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取技能文件失败")
		return
	}
	if len(data) > skillUploadMaxBytes {
		writeError(w, http.StatusBadRequest, "技能文件大小不能超过 64KB")
		return
	}
	if !utf8.Valid(data) {
		writeError(w, http.StatusBadRequest, "技能文件必须是 UTF-8 文本")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(header.Filename, ext)
	}
	name, content, errMsg := validateSkillInput(name, string(data))
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	sk, err := a.TutorStore.CreateSkill(r.Context(), u.ClassID, name, content, tutorpkg.SkillSourceUpload)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrAssistantNotFound) {
			writeError(w, http.StatusNotFound, "本班助手不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "上传技能失败")
		return
	}
	writeJSON(w, http.StatusCreated, sk)
}

// deleteTutorSkill 删除本班自建技能（仅教师）；内置解题引导技能返回 409。
func (a *API) deleteTutorSkill(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	skillID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "技能不存在")
		return
	}
	if err := a.TutorStore.DeleteSkill(r.Context(), u.ClassID, skillID); err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrSkillBuiltinProtected):
			writeError(w, http.StatusConflict, "内置技能不可删除")
		case errors.Is(err, tutorpkg.ErrSkillNotFound):
			writeError(w, http.StatusNotFound, "技能不存在")
		default:
			writeError(w, http.StatusInternalServerError, "删除技能失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// validateSkillInput 统一技能名称/内容校验；返回修剪后的值与错误文案（空串表示通过）。
func validateSkillInput(name, content string) (string, string, string) {
	name = strings.TrimSpace(name)
	content = strings.TrimSpace(content)
	if name == "" {
		return "", "", "技能名称不能为空"
	}
	if len([]rune(name)) > skillNameMaxRunes {
		return "", "", "技能名称长度不能超过 120 字"
	}
	if content == "" {
		return "", "", "技能内容不能为空"
	}
	if len([]rune(content)) > skillContentMaxRunes {
		return "", "", "技能内容长度不能超过 20000 字"
	}
	return name, content, ""
}

// parsePathID 解析正整数路径参数；非法/缺失返回 false（按资源不存在处理）。
func parsePathID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
