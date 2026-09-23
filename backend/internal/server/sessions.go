package server

import (
	"encoding/json"
	"net/http"

	"campusclaw/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
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

	token, err := a.Users.CreateSession(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	a.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, toMeResponse(u))
}

// DELETE /api/sessions
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		_ = a.Users.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
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

func (a *API) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.SessionTTL.Seconds()),
	})
}
