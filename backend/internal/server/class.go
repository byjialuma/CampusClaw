package server

import (
	"net/http"

	"campusclaw/internal/auth"
)

// GET /api/class/students
// 返回当前教师所属班级的学生名单。class_id 取自 JWT claims（requireAuth 注入），
// 角色校验由 requireTeacher 完成；学生调用返回 403。
func (a *API) listClassStudents(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	if u == nil {
		// 理论上 requireAuth 已拦截，防御性处理。
		writeError(w, http.StatusUnauthorized, "未登录或会话已失效")
		return
	}
	if a.ClassStudents == nil {
		writeError(w, http.StatusServiceUnavailable, "学生名单服务未配置")
		return
	}
	students, err := a.ClassStudents.ListStudentsByClass(r.Context(), u.ClassID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "加载学生名单失败")
		return
	}
	if students == nil {
		students = []auth.StudentRow{}
	}
	writeJSON(w, http.StatusOK, students)
}
