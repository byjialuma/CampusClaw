package server

import (
	"log"
	"net/http"
	"strings"
	"time"

	"campusclaw/internal/auth"
	"campusclaw/internal/model"
)

// requireAuth 强制 JWT access token 有效，并把当前用户注入上下文。
func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, http.StatusUnauthorized, "未登录或会话已失效")
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
			writeError(w, http.StatusUnauthorized, "未登录或会话已失效")
			return
		}
		u, err := a.JWT.Verify(parts[1])
		if err != nil {
			log.Printf("JWT 验签失败: %v", err)
			writeError(w, http.StatusUnauthorized, "未登录或会话已失效")
			return
		}
		ctx := auth.WithUser(r.Context(), u)
		next(w, r.WithContext(ctx))
	}
}

// requireTeacher 强制教师角色；学生一律 403（即使绕过前端直连接口）。
func (a *API) requireTeacher(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.CurrentUser(r.Context())
		if u == nil || u.Role != model.RoleTeacher {
			writeError(w, http.StatusForbidden, "学生无权限上传文件")
			return
		}
		next(w, r)
	}
}

// statusRecorder 记录响应状态码用于访问日志。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logRequests 是最外层访问日志中间件。
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Millisecond))
	})
}
