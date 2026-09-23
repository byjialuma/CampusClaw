package server

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"campusclaw/internal/store"
)

// GET /api/download?ticket=...
//
// 凭一次性票据下载：不依赖会话 Cookie。票据缺失/伪造 → 401；
// 已使用（如下载页刷新）或过期 → 410。下载页脚本对两类失败均跳登录。
func (a *API) download(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("ticket")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "下载凭证缺失")
		return
	}

	t, err := a.Tickets.Consume(r.Context(), token)
	if err != nil {
		if errors.Is(err, store.ErrTicketInvalid) {
			writeError(w, http.StatusGone, "下载凭证已失效，请重新登录")
			return
		}
		writeError(w, http.StatusInternalServerError, "校验下载凭证失败")
		return
	}

	m, err := a.Materials.GetByID(r.Context(), t.HandoutID)
	if err != nil || m.ClassID != t.ClassID {
		writeError(w, http.StatusNotFound, "材料不存在")
		return
	}
	rc, err := a.Materials.Open(m)
	if err != nil {
		writeError(w, http.StatusNotFound, "文件内容不存在")
		return
	}
	defer rc.Close()

	// 一次性票据：禁止任何中间层/浏览器缓存，杜绝刷新时用缓存响应掩盖票据失效。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		`attachment; filename="download"; filename*=UTF-8''`+url.PathEscape(m.OriginalName))
	w.Header().Set("Content-Length", strconv.FormatInt(m.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}
