// Package server 用标准库 net/http 组装全部 HTTP 路由与处理函数（不使用 Web 框架）。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"campusclaw/internal/model"
	qapkg "campusclaw/internal/qa"
	searchpkg "campusclaw/internal/search"
)

// SessionCookie 是服务端会话 Cookie 名称。
const SessionCookie = "cc_session"

// userAuthenticator 是最小的身份校验能力。
type userAuthenticator interface {
	Authenticate(ctx context.Context, username, password string) (*model.User, error)
}

// jwtVerifier 是 JWT 验签与签发的最小能力。
type jwtVerifier interface {
	Sign(user *model.User) (token string, expiresAt time.Time, err error)
	Verify(token string) (*model.User, error)
}

// 材料相关依赖（*store.MaterialRepository 满足）。
type materialService interface {
	ListByClass(ctx context.Context, classID int64) ([]model.Material, error)
	GetByID(ctx context.Context, id int64) (*model.Material, error)
	SaveAndCreate(ctx context.Context, classID, uploaderID int64, originalName string, data []byte) (*model.Material, error)
	Open(m *model.Material) (io.ReadCloser, error)
}

// 下载票据依赖（*store.TicketRepository 满足）。
type ticketService interface {
	Create(ctx context.Context, handoutID, userID, classID int64) (string, error)
	Consume(ctx context.Context, token string) (*model.Ticket, error)
	DeleteExpired(ctx context.Context) error
}

// pinger 是健康检查所需的最小数据库能力。
type pinger interface {
	PingContext(ctx context.Context) error
}

// indexQueue 是上传成功后异步入队的最小能力（*indexer.Queue 满足）。
type indexQueue interface {
	Enqueue(ctx context.Context, handoutID int64) error
}

// vectorPinger 是健康检查所需的向量库探测能力（*vector.Client 满足）。
type vectorPinger interface {
	Ping(ctx context.Context) error
}

// knowledgeSearcher 是班级内语义检索能力（*search.Service 满足）。
type knowledgeSearcher interface {
	Search(ctx context.Context, query string, classID int64) ([]searchpkg.Result, error)
}

// qaService 是知识问答编排能力（*qa.Service 满足）。
type qaService interface {
	Ask(ctx context.Context, question string, classID int64) (*qapkg.Result, error)
}

// API 持有全部处理函数共享的依赖。
type API struct {
	Users      userAuthenticator
	JWT        jwtVerifier
	Materials  materialService
	Tickets    ticketService
	DB         pinger
	Indexer    indexQueue
	Vectors    vectorPinger
	SearchSvc  knowledgeSearcher
	QASvc      qaService
	Tutor      tutorAsker
	TutorStore tutorStore
}

// NewHandler 构建路由树：鉴权在各路由上显式包装。
func (a *API) NewHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", a.health)

	mux.HandleFunc("POST /api/sessions", a.login)
	mux.HandleFunc("DELETE /api/sessions", a.requireAuth(a.logout))
	mux.HandleFunc("GET /api/me", a.requireAuth(a.me))

	mux.HandleFunc("GET /api/search", a.requireAuth(a.searchKnowledge))
	mux.HandleFunc("POST /api/qa", a.requireAuth(a.askQuestion))

	// 解题助手：会话/消息/SSE 提问（登录即可），提示词与技能管理（仅教师）。
	mux.HandleFunc("POST /api/tutor/conversations", a.requireAuth(a.createTutorConversation))
	mux.HandleFunc("GET /api/tutor/conversations", a.requireAuth(a.listTutorConversations))
	mux.HandleFunc("GET /api/tutor/conversations/{id}/messages", a.requireAuth(a.getTutorMessages))
	mux.HandleFunc("POST /api/tutor/conversations/{id}/messages", a.requireAuth(a.askTutor))
	mux.HandleFunc("GET /api/tutor/assistant", a.requireAuth(a.requireTeacher(a.getTutorAssistant)))
	mux.HandleFunc("PUT /api/tutor/assistant/prompt", a.requireAuth(a.requireTeacher(a.saveTutorPrompt)))
	mux.HandleFunc("PUT /api/tutor/assistant/skills/{id}", a.requireAuth(a.requireTeacher(a.putTutorSkill)))

	mux.HandleFunc("GET /api/materials", a.requireAuth(a.listMaterials))
	mux.HandleFunc("POST /api/materials", a.requireAuth(a.requireTeacher(a.uploadMaterial)))
	mux.HandleFunc("GET /api/materials/{id}/content", a.requireAuth(a.materialContent))
	mux.HandleFunc("POST /api/materials/{id}/download-ticket", a.requireAuth(a.createTicket))

	// 凭一次性票据下载，不依赖会话 Cookie。
	mux.HandleFunc("GET /api/download", a.download)

	return logRequests(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// meResponse 是 /api/me 与登录响应共享的用户视图。
type meResponse struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	ClassID     int64  `json:"classId"`
	ClassName   string `json:"className"`
}

func toMeResponse(u *model.User) meResponse {
	return meResponse{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		ClassID:     u.ClassID,
		ClassName:   u.ClassName,
	}
}
