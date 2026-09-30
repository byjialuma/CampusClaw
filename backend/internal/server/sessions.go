package server

import (
	"encoding/json"
	"net/http"
	"time"

	"campusclaw/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponseBody 是登录成功后的响应体。
type loginResponseBody struct {
	User        meResponse `json:"user"`
	AccessToken string     `json:"accessToken"`
	ExpiresAt   time.Time  `json:"expiresAt"`
}

// POST /api/sessions
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "请输入用户名和密码")
		return
	}

	u, err := a.Users.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		// 用户不存在与密码错误响应完全一致。
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	token, expiresAt, err := a.JWT.Sign(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成访问令牌失败")
		return
	}
	writeJSON(w, http.StatusOK, loginResponseBody{
		User:        toMeResponse(u),
		AccessToken: token,
		ExpiresAt:   expiresAt,
	})
}

// DELETE /api/sessions
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	// JWT 无状态，客户端直接丢弃 token 即可；服务端不维护黑名单。
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/me
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	if u == nil {
		// 理论上 requireAuth 已拦截，防御性处理。
		writeError(w, http.StatusUnauthorized, "未登录或会话已失效")
		return
	}
	writeJSON(w, http.StatusOK, toMeResponse(u))
}
