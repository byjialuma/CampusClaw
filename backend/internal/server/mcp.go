package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"campusclaw/internal/auth"
	"campusclaw/internal/mcp"
	tutorpkg "campusclaw/internal/tutor"
)

// MCPProbe 是连接服务器时握手与工具发现所需的能力（*mcp.Client 满足）。
type MCPProbe interface {
	Initialize(ctx context.Context) (*mcp.ServerInfo, error)
	ListTools(ctx context.Context) ([]mcp.Tool, error)
}

// MCPDialer 按 base_url 与令牌构造连接探测客户端（生产环境传入包装 mcp.NewClient 的函数）。
type MCPDialer func(baseURL, token string, timeout time.Duration) MCPProbe

// mcpStore 是 MCP 服务器与审计的 MySQL 读写（*tutor.TutorRepository 满足）。
type mcpStore interface {
	ListMCPServers(ctx context.Context, classID int64) ([]tutorpkg.MCPServer, error)
	GetMCPServer(ctx context.Context, classID, id int64) (*tutorpkg.MCPServer, error)
	GetMCPServerByName(ctx context.Context, classID int64, name string) (*tutorpkg.MCPServer, error)
	CreateMCPServer(ctx context.Context, classID int64, name, baseURL, token string) (*tutorpkg.MCPServer, error)
	UpdateMCPServer(ctx context.Context, classID, id int64, name, baseURL string, token *string) (*tutorpkg.MCPServer, error)
	DeleteMCPServer(ctx context.Context, classID, id int64) error
	MarkMCPServerConnected(ctx context.Context, classID, id int64, tools []mcp.Tool) error
	MarkMCPServerConnectFailed(ctx context.Context, classID, id int64, lastError string) error
	DisconnectMCPServer(ctx context.Context, classID, id int64) error
	RecentMCPCalls(ctx context.Context, classID int64, limit int) ([]tutorpkg.MCPCall, error)
}

// 服务器名称与 Base URL 长度上限；最近调用记录条数上限。
const (
	mcpServerNameMaxRunes = 120
	mcpBaseURLMaxRunes    = 512
	mcpCallListLimit      = 50
)

// ---------- 请求/响应载体 ----------

type mcpServerResponse struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	BaseURL   string     `json:"baseUrl"`
	HasAuth   bool       `json:"hasAuth"`
	Connected bool       `json:"connected"`
	Tools     []mcp.Tool `json:"tools"`
	LastError string     `json:"lastError,omitempty"`
}

type createMCPServerRequest struct {
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	AuthToken string `json:"authToken"`
}

// updateMCPServerRequest 的 AuthToken 为指针：缺省/null 表示保留原令牌，给定值则覆盖。
type updateMCPServerRequest struct {
	Name      string  `json:"name"`
	BaseURL   string  `json:"baseUrl"`
	AuthToken *string `json:"authToken"`
}

// ---------- 处理函数 ----------

// listMCPServers 返回本班全部 MCP 服务器；响应只回 hasAuth 布尔，绝不含令牌。
func (a *API) listMCPServers(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	servers, err := a.MCPStore.ListMCPServers(r.Context(), u.ClassID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 MCP 服务器列表失败")
		return
	}
	out := make([]mcpServerResponse, 0, len(servers))
	for _, s := range servers {
		out = append(out, toMCPServerResponse(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// createMCPServer 登记一台本班 MCP 服务器；班级取自 JWT claims，同班重名返回 400。
func (a *API) createMCPServer(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	var req createMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name, baseURL, errMsg := validateMCPInput(req.Name, req.BaseURL)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	srv, err := a.MCPStore.CreateMCPServer(r.Context(), u.ClassID, name, baseURL, strings.TrimSpace(req.AuthToken))
	if err != nil {
		if errors.Is(err, tutorpkg.ErrMCPServerNameConflict) {
			writeError(w, http.StatusBadRequest, "本班已存在同名 MCP 服务器")
			return
		}
		writeError(w, http.StatusInternalServerError, "登记 MCP 服务器失败")
		return
	}
	writeJSON(w, http.StatusCreated, toMCPServerResponse(*srv))
}

// updateMCPServer 修改本班服务器；跨班/不存在返回 404，重名返回 400。
func (a *API) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		return
	}
	var req updateMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name, baseURL, errMsg := validateMCPInput(req.Name, req.BaseURL)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	token := req.AuthToken
	if token != nil {
		trimmed := strings.TrimSpace(*token)
		token = &trimmed
	}
	srv, err := a.MCPStore.UpdateMCPServer(r.Context(), u.ClassID, id, name, baseURL, token)
	if err != nil {
		switch {
		case errors.Is(err, tutorpkg.ErrMCPServerNotFound):
			writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		case errors.Is(err, tutorpkg.ErrMCPServerNameConflict):
			writeError(w, http.StatusBadRequest, "本班已存在同名 MCP 服务器")
		default:
			writeError(w, http.StatusInternalServerError, "修改 MCP 服务器失败")
		}
		return
	}
	writeJSON(w, http.StatusOK, toMCPServerResponse(*srv))
}

// deleteMCPServer 删除本班服务器；跨班/不存在返回 404。
func (a *API) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		return
	}
	if err := a.MCPStore.DeleteMCPServer(r.Context(), u.ClassID, id); err != nil {
		if errors.Is(err, tutorpkg.ErrMCPServerNotFound) {
			writeError(w, http.StatusNotFound, "MCP 服务器不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "删除 MCP 服务器失败")
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// connectMCPServer 完成握手与工具发现并缓存清单；任一步失败返回 502 且保持未连接。
func (a *API) connectMCPServer(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		return
	}
	srv, err := a.MCPStore.GetMCPServer(r.Context(), u.ClassID, id)
	if err != nil {
		if errors.Is(err, tutorpkg.ErrMCPServerNotFound) {
			writeError(w, http.StatusNotFound, "MCP 服务器不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "读取 MCP 服务器失败")
		return
	}
	if a.MCPDial == nil {
		writeError(w, http.StatusBadGateway, "MCP 连接能力未配置")
		return
	}

	timeout := a.MCPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probe := a.MCPDial(srv.BaseURL, srv.AuthToken, timeout)

	if _, err := probe.Initialize(r.Context()); err != nil {
		_ = a.MCPStore.MarkMCPServerConnectFailed(r.Context(), u.ClassID, id, "握手失败")
		writeError(w, http.StatusBadGateway, "连接 MCP 服务器失败，请检查 Base URL 与令牌")
		return
	}
	tools, err := probe.ListTools(r.Context())
	if err != nil {
		_ = a.MCPStore.MarkMCPServerConnectFailed(r.Context(), u.ClassID, id, "工具发现失败")
		writeError(w, http.StatusBadGateway, "连接 MCP 服务器失败，请检查 Base URL 与令牌")
		return
	}
	if err := a.MCPStore.MarkMCPServerConnected(r.Context(), u.ClassID, id, tools); err != nil {
		if errors.Is(err, tutorpkg.ErrMCPServerNotFound) {
			writeError(w, http.StatusNotFound, "MCP 服务器不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "缓存 MCP 工具清单失败")
		return
	}

	if len(tools) > tutorpkg.MCPToolCacheLimit {
		tools = tools[:tutorpkg.MCPToolCacheLimit]
	}
	srv.Connected = true
	srv.Tools = tools
	srv.LastError = ""
	writeJSON(w, http.StatusOK, toMCPServerResponse(*srv))
}

// disconnectMCPServer 断开服务器并清空工具清单。
func (a *API) disconnectMCPServer(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, ok := parsePathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "MCP 服务器不存在")
		return
	}
	if err := a.MCPStore.DisconnectMCPServer(r.Context(), u.ClassID, id); err != nil {
		if errors.Is(err, tutorpkg.ErrMCPServerNotFound) {
			writeError(w, http.StatusNotFound, "MCP 服务器不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "断开 MCP 服务器失败")
		return
	}
	srv, err := a.MCPStore.GetMCPServer(r.Context(), u.ClassID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 MCP 服务器失败")
		return
	}
	writeJSON(w, http.StatusOK, toMCPServerResponse(*srv))
}

// listMCPCalls 返回本班最近调用记录；班级取自 JWT claims，跨班记录不会被返回。
func (a *API) listMCPCalls(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	calls, err := a.MCPStore.RecentMCPCalls(r.Context(), u.ClassID, mcpCallListLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取调用记录失败")
		return
	}
	writeJSON(w, http.StatusOK, calls)
}

// toMCPServerResponse 把仓储视图映射为响应；不含令牌，只回 hasAuth 布尔。
func toMCPServerResponse(s tutorpkg.MCPServer) mcpServerResponse {
	tools := s.Tools
	if tools == nil {
		tools = []mcp.Tool{}
	}
	return mcpServerResponse{
		ID:        s.ID,
		Name:      s.Name,
		BaseURL:   s.BaseURL,
		HasAuth:   s.HasAuth,
		Connected: s.Connected,
		Tools:     tools,
		LastError: s.LastError,
	}
}

// validateMCPInput 校验服务器名称与 Base URL；返回修剪后的值与错误文案（空串表示通过）。
func validateMCPInput(name, baseURL string) (string, string, string) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimSpace(baseURL)
	if name == "" {
		return "", "", "服务器名称不能为空"
	}
	if len([]rune(name)) > mcpServerNameMaxRunes {
		return "", "", "服务器名称长度不能超过 120 字"
	}
	if !tutorpkg.ValidMCPServerName(name) {
		return "", "", "服务器名称需以小写字母开头，仅含小写字母、数字、下划线与连字符"
	}
	if baseURL == "" {
		return "", "", "Base URL 不能为空"
	}
	if len([]rune(baseURL)) > mcpBaseURLMaxRunes {
		return "", "", "Base URL 长度不能超过 512 字"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", "", "Base URL 必须是合法的 http(s) 地址"
	}
	return name, baseURL, ""
}

// validateSkillToolRefs 校验技能正文中的 MCP 工具引用：服务器存在 → 已连接 → 工具在缓存清单。
// 内置引用 knowledge_search 无需校验；返回空串表示通过，否则返回指明具体引用的可读原因。
func (a *API) validateSkillToolRefs(ctx context.Context, classID int64, content string) string {
	parsed, err := tutorpkg.ParseSkillTools(content)
	if err != nil {
		return err.Error()
	}
	return a.validateSkillToolRefList(ctx, classID, parsed.Refs)
}

// validateSkillToolRefList 校验已解析的工具引用（技能包合并 frontmatter 后使用），规则与正文校验一致。
func (a *API) validateSkillToolRefList(ctx context.Context, classID int64, refs []tutorpkg.ToolRef) string {
	for _, ref := range refs {
		if ref.Builtin {
			continue
		}
		srv, err := a.MCPStore.GetMCPServerByName(ctx, classID, ref.Server)
		if err != nil {
			if errors.Is(err, tutorpkg.ErrMCPServerNotFound) {
				return "工具引用 " + ref.Raw + " 的服务器在本班不存在"
			}
			return "校验工具引用 " + ref.Raw + " 失败"
		}
		if !srv.Connected {
			return "工具引用 " + ref.Raw + " 的服务器尚未连接"
		}
		if !mcpToolExists(srv.Tools, ref.Name) {
			return "工具 " + ref.Name + " 不在服务器 " + ref.Server + " 的工具清单中"
		}
	}
	return ""
}

// mcpToolExists 判断工具名是否出现在清单中。
func mcpToolExists(tools []mcp.Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
