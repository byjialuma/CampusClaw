// Package server 用标准库 net/http 组装全部 HTTP 路由与处理函数（不使用 Web 框架）。
package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"campusclaw/internal/auth"
)

// askRequest 是知识问答请求体。
type askRequest struct {
	Question string `json:"question"`
}

// askQuestion 处理知识问答请求。
func (a *API) askQuestion(w http.ResponseWriter, r *http.Request) {
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
	if len(question) > 500 {
		writeError(w, http.StatusBadRequest, "问题长度不能超过 500 字")
		return
	}

	u := auth.CurrentUser(r.Context())
	result, err := a.QASvc.Ask(r.Context(), question, u.ClassID)
	if err != nil {
		// 统一返回 503，不区分 Embed/Search/LLM 哪一步失败，不泄露内部细节。
		writeError(w, http.StatusServiceUnavailable, "知识库问答暂不可用")
		return
	}

	writeJSON(w, http.StatusOK, result)
}
