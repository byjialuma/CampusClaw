package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"campusclaw/internal/auth"
	"campusclaw/internal/model"
	tutorpkg "campusclaw/internal/tutor"
)

// tutorAsker 是流式提问编排能力（*tutor.Service 满足）。
type tutorAsker interface {
	AskStream(ctx context.Context, in tutorpkg.AskInput) (*tutorpkg.AskOutput, error)
}

// tutorStore 是会话/助手配置的直接 MySQL 读写（*tutor.TutorRepository 满足）。
type tutorStore interface {
	CreateConversation(ctx context.Context, classID, userID, agentID int64, title string) (int64, error)
	ConversationsByUser(ctx context.Context, userID int64) ([]tutorpkg.Conversation, error)
	ConversationForUser(ctx context.Context, convID, userID int64) (*tutorpkg.Conversation, error)
	MessagesByConversation(ctx context.Context, convID int64) ([]tutorpkg.Message, error)
	AssistantConfig(ctx context.Context, classID int64) (*tutorpkg.AssistantConfig, error)
	SetSkillEnabled(ctx context.Context, classID, skillID int64, enabled bool) error
	CreateSkill(ctx context.Context, classID int64, name, content, source string) (*tutorpkg.Skill, error)
	DeleteSkill(ctx context.Context, classID, skillID int64) error
	SlugExists(ctx context.Context, classID int64, slug string) (bool, error)
	CreateSkillPackage(ctx context.Context, classID int64, pkg *tutorpkg.SkillPackage, slug string) (*tutorpkg.Skill, error)
	SetSkillPackagePath(ctx context.Context, classID, skillID int64, dirPath, contentHash string) error
	GetSkillPackage(ctx context.Context, classID, skillID int64) (*tutorpkg.Skill, error)

	// 智能体 CRUD 与技能/材料绑定（班级一律取参数，来自 JWT claims）。
	ListAgents(ctx context.Context, classID int64) ([]tutorpkg.Agent, error)
	GetAgent(ctx context.Context, classID, id int64) (*tutorpkg.Agent, error)
	CreateAgent(ctx context.Context, classID int64, name, systemPrompt string) (*tutorpkg.Agent, error)
	UpdateAgent(ctx context.Context, classID, id int64, name, systemPrompt string) (*tutorpkg.Agent, error)
	DeleteAgent(ctx context.Context, classID, id int64) error
	ReplaceAgentSkills(ctx context.Context, classID, agentID int64, skillIDs []int64) error
	ReplaceAgentMaterials(ctx context.Context, classID, agentID int64, handoutIDs []int64) error
	AgentSkillIDs(ctx context.Context, agentID int64) ([]int64, error)
	AgentHandoutIDs(ctx context.Context, agentID int64) ([]int64, error)
}

// 新会话的初始标题（首问提交后由首问前 30 字覆盖）。
const newConversationTitle = "新的解题会话"

// promptMaxRunes 是智能体提示词长度上限。
const promptMaxRunes = 2000

// agentNameMaxRunes 是智能体名称长度上限。
const agentNameMaxRunes = 120

// 技能入参校验口径（设计 §6）。
const (
	skillNameMaxRunes    = 120
	skillDescMaxRunes    = 255
	skillContentMaxRunes = 20000
	skillUploadMaxBytes  = 64 * 1024 // 64KB
)

// ---------- 请求/响应载体 ----------

type createdConversationResponse struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// assistantResponse 是本班技能库视图（仅教师）：提示词随智能体配置，不再在此返回。
type assistantResponse struct {
	ID     int64                `json:"id"`
	Name   string               `json:"name"`
	Skills []assistantSkillView `json:"skills"`
}

// assistantSkillView 在技能信息基础上附带解析出的工具引用（供技能管理界面展示）。
type assistantSkillView struct {
	tutorpkg.Skill
	Tools []tutorpkg.ToolRef `json:"tools"`
}

// skillPackageView 是技能包详情：技能字段 + 工具引用 + 包内资源清单。
type skillPackageView struct {
	tutorpkg.Skill
	Tools []tutorpkg.ToolRef       `json:"tools"`
	Files []tutorpkg.SkillResource `json:"files"`
}

// teacherAgentView 是教师视图的智能体：含提示词与绑定的技能/材料清单。
type teacherAgentView struct {
	tutorpkg.Agent
	SkillIDs   []int64 `json:"skillIds"`
	HandoutIDs []int64 `json:"handoutIds"`
}

// studentAgentView 是学生视图的智能体：仅暴露身份与更新时间，不含提示词与绑定。
type studentAgentView struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type createConversationRequest struct {
	AgentID int64 `json:"agentId"`
}

type saveAgentRequest struct {
	Name         string `json:"name"`
	SystemPrompt string `json:"systemPrompt"`
}

type setAgentSkillsRequest struct {
	SkillIDs []int64 `json:"skillIds"`
}

type setAgentMaterialsRequest struct {
	HandoutIDs []int64 `json:"handoutIds"`
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
	Citations      []tutorpkg.Citation            `json:"citations"`
	Skills         []tutorpkg.SkillRef            `json:"skills"`
	ToolCalls      []tutorpkg.ToolCallRef         `json:"toolCalls"`
	SkillResources []tutorpkg.SkillResourceStatus `json:"skillResources"`
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

// createTutorConversation 创建会话：agentId 必填并须属本班；class_id/user_id 全部取自 JWT claims。
func (a *API) createTutorConversation(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	var req createConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.AgentID <= 0 {
		writeError(w, http.StatusBadRequest, "缺少智能体 agentId")
		return
	}
	if _, err := a.TutorStore.GetAgent(r.Context(), u.ClassID, req.AgentID); err != nil {
		if errors.Is(err, tutorpkg.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "智能体不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	id, err := a.TutorStore.CreateConversation(r.Context(), u.ClassID, u.ID, req.AgentID, newConversationTitle)
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
		data = sseMetaPayload{
			Citations:      e.Citations,
			Skills:         e.Skills,
			ToolCalls:      e.ToolCalls,
			SkillResources: e.SkillResources,
		}
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
		ID:     cfg.AssistantID,
		Name:   cfg.Name,
		Skills: toAssistantSkillViews(cfg.Skills),
	})
}

// toAssistantSkillViews 为每条技能解析工具引用供界面展示；包技能优先用 frontmatter 声明，
// 纯文本技能回退正文解析。非法引用按空处理，不影响配置读取。
func toAssistantSkillViews(skills []tutorpkg.Skill) []assistantSkillView {
	out := make([]assistantSkillView, 0, len(skills))
	for _, sk := range skills {
		refs := sk.ToolRefs
		if len(refs) == 0 {
			if parsed, err := tutorpkg.ParseSkillTools(sk.Content); err == nil {
				refs = parsed.Refs
			}
		}
		if refs == nil {
			refs = []tutorpkg.ToolRef{}
		}
		out = append(out, assistantSkillView{Skill: sk, Tools: refs})
	}
	return out
}

// ---------- 智能体管理 ----------

// listTutorAgents 返回本班智能体列表：教师含提示词与绑定清单，学生仅含 id/name/updatedAt。
func (a *API) listTutorAgents(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	agents, err := a.TutorStore.ListAgents(r.Context(), u.ClassID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取智能体列表失败")
		return
	}
	if u.Role == model.RoleTeacher {
		views := make([]teacherAgentView, 0, len(agents))
		for _, ag := range agents {
			skillIDs, err := a.TutorStore.AgentSkillIDs(r.Context(), ag.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "读取智能体绑定失败")
				return
			}
			handoutIDs, err := a.TutorStore.AgentHandoutIDs(r.Context(), ag.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "读取智能体绑定失败")
				return
			}
			views = append(views, teacherAgentView{Agent: ag, SkillIDs: skillIDs, HandoutIDs: handoutIDs})
		}
		writeJSON(w, http.StatusOK, views)
		return
	}
	views := make([]studentAgentView, 0, len(agents))
	for _, ag := range agents {
		views = append(views, studentAgentView{ID: ag.ID, Name: ag.Name, UpdatedAt: ag.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, views)
}

// createTutorAgent 新建本班智能体（仅教师）；同班重名 400。
func (a *API) createTutorAgent(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	name, prompt, errMsg := decodeAgentInput(r)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	ag, err := a.TutorStore.CreateAgent(r.Context(), u.ClassID, name, prompt)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrAgentNameConflict) {
			writeError(w, http.StatusBadRequest, "本班已存在同名智能体")
			return
		}
		writeError(w, http.StatusInternalServerError, "新建智能体失败")
		return
	}
	writeJSON(w, http.StatusCreated, teacherAgentView{Agent: *ag, SkillIDs: []int64{}, HandoutIDs: []int64{}})
}

// updateTutorAgent 修改本班智能体名称与提示词（仅教师）；同班重名 400，跨班或不存在 404。
func (a *API) updateTutorAgent(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	agentID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "智能体不存在")
		return
	}
	name, prompt, errMsg := decodeAgentInput(r)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	ag, err := a.TutorStore.UpdateAgent(r.Context(), u.ClassID, agentID, name, prompt)
	if err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrAgentNameConflict):
			writeError(w, http.StatusBadRequest, "本班已存在同名智能体")
		case errors.Is(err, tutorpkg.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "智能体不存在")
		default:
			writeError(w, http.StatusInternalServerError, "更新智能体失败")
		}
		return
	}
	skillIDs, err := a.TutorStore.AgentSkillIDs(r.Context(), ag.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取智能体绑定失败")
		return
	}
	handoutIDs, err := a.TutorStore.AgentHandoutIDs(r.Context(), ag.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取智能体绑定失败")
		return
	}
	writeJSON(w, http.StatusOK, teacherAgentView{Agent: *ag, SkillIDs: skillIDs, HandoutIDs: handoutIDs})
}

// deleteTutorAgent 删除本班智能体（仅教师）；仍被会话引用时 409。
func (a *API) deleteTutorAgent(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	agentID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "智能体不存在")
		return
	}
	if err := a.TutorStore.DeleteAgent(r.Context(), u.ClassID, agentID); err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrAgentInUse):
			writeError(w, http.StatusConflict, "智能体仍被会话引用，无法删除")
		case errors.Is(err, tutorpkg.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "智能体不存在")
		default:
			writeError(w, http.StatusInternalServerError, "删除智能体失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// putAgentSkills 全量替换智能体绑定的技能（仅教师）；任一技能不属本班时 404 且绑定不变。
func (a *API) putAgentSkills(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	agentID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "智能体不存在")
		return
	}
	var req setAgentSkillsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.TutorStore.ReplaceAgentSkills(r.Context(), u.ClassID, agentID, req.SkillIDs); err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "智能体不存在")
		case errors.Is(err, tutorpkg.ErrSkillNotFound):
			writeError(w, http.StatusNotFound, "技能不存在")
		default:
			writeError(w, http.StatusInternalServerError, "保存技能绑定失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// putAgentMaterials 全量替换智能体绑定的材料（仅教师）；任一材料不属本班时 404 且绑定不变。
func (a *API) putAgentMaterials(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	agentID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "智能体不存在")
		return
	}
	var req setAgentMaterialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.TutorStore.ReplaceAgentMaterials(r.Context(), u.ClassID, agentID, req.HandoutIDs); err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "智能体不存在")
		case errors.Is(err, tutorpkg.ErrHandoutNotFound):
			writeError(w, http.StatusNotFound, "材料不存在")
		default:
			writeError(w, http.StatusInternalServerError, "保存材料绑定失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// decodeAgentInput 解析并校验智能体名称/提示词；提示词允许为空。返回错误文案（空串表示通过）。
func decodeAgentInput(r *http.Request) (string, string, string) {
	var req saveAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", "", "请求格式错误"
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", "", "智能体名称不能为空"
	}
	if len([]rune(name)) > agentNameMaxRunes {
		return "", "", "智能体名称长度不能超过 120 字"
	}
	prompt := strings.TrimSpace(req.SystemPrompt)
	if len([]rune(prompt)) > promptMaxRunes {
		return "", "", "提示词长度不能超过 2000 字"
	}
	return name, prompt, ""
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
	if errMsg := a.validateSkillToolRefs(r.Context(), u.ClassID, content); errMsg != "" {
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
	if errMsg := a.validateSkillToolRefs(r.Context(), u.ClassID, content); errMsg != "" {
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

// uploadTutorSkillPackage 上传 zip 技能包（仅教师）：解析校验 → 入库 → 落盘 → 回填目录。
// 落盘或回填失败时删除已写入的技能行与目录，保证不留下半成品。
func (a *API) uploadTutorSkillPackage(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	if a.SkillStorage == nil {
		writeError(w, http.StatusServiceUnavailable, "技能包存储未启用")
		return
	}
	// 压缩包上限 + multipart 开销。
	r.Body = http.MaxBytesReader(w, r.Body, tutorpkg.MaxSkillPackageBytes+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少技能包文件（表单字段 file）")
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		writeError(w, http.StatusBadRequest, "技能包必须是 .zip 文件")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, tutorpkg.MaxSkillPackageBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取技能包失败")
		return
	}
	if len(data) > tutorpkg.MaxSkillPackageBytes {
		writeError(w, http.StatusBadRequest, "技能包大小不能超过 512KB")
		return
	}
	decoded, err := tutorpkg.DecodeSkillPackageZip(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pkg := decoded.Package
	if len([]rune(pkg.Name)) > skillNameMaxRunes {
		writeError(w, http.StatusBadRequest, "技能名称长度不能超过 120 字")
		return
	}
	if len([]rune(pkg.Description)) > skillDescMaxRunes {
		writeError(w, http.StatusBadRequest, "技能描述长度不能超过 255 字")
		return
	}
	if len([]rune(pkg.Body)) > skillContentMaxRunes {
		writeError(w, http.StatusBadRequest, "技能内容长度不能超过 20000 字")
		return
	}
	if errMsg := a.validateSkillToolRefList(r.Context(), u.ClassID, pkg.ToolRefs); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	slug := tutorpkg.SkillSlug(pkg.Name)
	exists, err := a.TutorStore.SlugExists(r.Context(), u.ClassID, slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "上传技能包失败")
		return
	}
	if exists {
		writeError(w, http.StatusConflict, "本班已存在同名技能包")
		return
	}

	sk, err := a.TutorStore.CreateSkillPackage(r.Context(), u.ClassID, pkg, slug)
	if err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrSkillPackageConflict):
			writeError(w, http.StatusConflict, "本班已存在同名技能包")
		case errors.Is(err, tutorpkg.ErrAssistantNotFound):
			writeError(w, http.StatusNotFound, "本班助手不存在")
		default:
			writeError(w, http.StatusInternalServerError, "上传技能包失败")
		}
		return
	}

	dirPath, err := a.SkillStorage.WritePackage(u.ClassID, sk.ID, decoded.Files)
	if err != nil {
		_ = a.TutorStore.DeleteSkill(r.Context(), u.ClassID, sk.ID)
		writeError(w, http.StatusInternalServerError, "技能包落盘失败")
		return
	}
	if err := a.TutorStore.SetSkillPackagePath(r.Context(), u.ClassID, sk.ID, dirPath, tutorpkg.ContentHash(pkg.Raw)); err != nil {
		_ = a.SkillStorage.RemovePackage(dirPath)
		_ = a.TutorStore.DeleteSkill(r.Context(), u.ClassID, sk.ID)
		writeError(w, http.StatusInternalServerError, "技能包落盘失败")
		return
	}
	sk.DirPath = dirPath
	writeJSON(w, http.StatusCreated, a.skillPackageView(sk))
}

// getTutorSkillPackage 返回本班技能包详情与资源清单（仅教师）；跨班或不存在 404。
func (a *API) getTutorSkillPackage(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	skillID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "技能不存在")
		return
	}
	sk, err := a.TutorStore.GetSkillPackage(r.Context(), u.ClassID, skillID)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrSkillNotFound) {
			writeError(w, http.StatusNotFound, "技能不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "读取技能详情失败")
		return
	}
	writeJSON(w, http.StatusOK, a.skillPackageView(sk))
}

// getTutorSkillResource 读取技能包内的单个资源（仅教师）；路径限定在 references/assets/scripts 内。
// 非法路径 400，跨班、无包目录或文件不存在 404。
func (a *API) getTutorSkillResource(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	if a.SkillStorage == nil {
		writeError(w, http.StatusServiceUnavailable, "技能包存储未启用")
		return
	}
	skillID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "技能不存在")
		return
	}
	sk, err := a.TutorStore.GetSkillPackage(r.Context(), u.ClassID, skillID)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrSkillNotFound) {
			writeError(w, http.StatusNotFound, "技能不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "读取技能失败")
		return
	}
	if sk.DirPath == "" {
		writeError(w, http.StatusNotFound, "技能资源不存在")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "缺少资源路径参数 path")
		return
	}
	data, isText, err := a.SkillStorage.ReadResource(sk.DirPath, relPath)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrInvalidSkillResourcePath) {
			writeError(w, http.StatusBadRequest, "非法的资源路径")
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "技能资源不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "读取技能资源失败")
		return
	}
	contentType := "application/octet-stream"
	if isText {
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// skillPackageView 组装技能包详情：解析工具引用与包内资源清单；清单读取失败按空列表返回。
func (a *API) skillPackageView(sk *tutorpkg.Skill) skillPackageView {
	view := skillPackageView{Skill: *sk, Tools: sk.ToolRefs, Files: []tutorpkg.SkillResource{}}
	if view.Tools == nil {
		view.Tools = []tutorpkg.ToolRef{}
	}
	if view.Resources == nil {
		view.Resources = []string{}
	}
	if a.SkillStorage != nil && sk.DirPath != "" {
		if files, err := a.SkillStorage.ResourceList(sk.DirPath); err == nil {
			view.Files = files
		}
	}
	return view
}

// deleteTutorSkill 删除本班自建技能（仅教师）；内置解题引导技能返回 409。
// 技能包目录随技能行一起清理；删除失败不影响错误语义。
func (a *API) deleteTutorSkill(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	skillID, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "技能不存在")
		return
	}
	var dirPath string
	if sk, err := a.TutorStore.GetSkillPackage(r.Context(), u.ClassID, skillID); err == nil {
		dirPath = sk.DirPath
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
	if a.SkillStorage != nil && dirPath != "" {
		_ = a.SkillStorage.RemovePackage(dirPath)
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
