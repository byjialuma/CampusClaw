package server

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"campusclaw/internal/auth"
	searchpkg "campusclaw/internal/search"
)

// maxQueryRunes 是检索查询的最大长度（按 rune 计）。
const maxQueryRunes = 500

// searchUnavailableMessage 是下游故障时的固定对外文案，不包含任何内部地址。
const searchUnavailableMessage = "知识库检索暂不可用"

// GET /api/search?q=... —— 教师学生均可用；班级只取服务端会话。
func (a *API) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	// 注意：刻意不读取 class_id / classId 等任何请求参数，
	// 班级范围只能来自服务端会话。
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > maxQueryRunes {
		writeError(w, http.StatusBadRequest, "查询内容不能为空且不超过 500 字")
		return
	}

	u := auth.CurrentUser(r.Context())
	results, err := a.SearchSvc.Search(r.Context(), q, u.ClassID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, searchUnavailableMessage)
		return
	}
	if results == nil {
		results = []searchResultView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// searchResultView 用别名输出，保持字段名与 search.Result 的 JSON tag 一致。
type searchResultView = searchpkg.Result
