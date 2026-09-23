package server

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"campusclaw/internal/auth"
	"campusclaw/internal/model"
	"campusclaw/internal/store"
)

// GET /api/materials —— 服务端按会话班级过滤。
func (a *API) listMaterials(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	items, err := a.Materials.ListByClass(r.Context(), u.ClassID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询材料失败")
		return
	}
	if items == nil {
		items = []model.Material{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"materials": items})
}

// POST /api/materials —— requireTeacher 已保证学生拿 403。
func (a *API) uploadMaterial(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())

	// 32MB 硬上限：超限 ParseMultipartForm 直接返回错误。
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxUploadSize)
	if err := r.ParseMultipartForm(store.MaxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "文件过大或表单格式错误（上限 32MB）")
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少上传文件字段 file")
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取上传文件失败")
		return
	}

	// class_id / uploader 一律取自服务端会话，忽略客户端的任何声明。
	m, err := a.Materials.SaveAndCreate(r.Context(), u.ClassID, u.ID, header.Filename, data)
	if err != nil {
		if errors.Is(err, store.ErrInvalidFile) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "保存材料失败")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// GET /api/materials/{id}/content
func (a *API) materialContent(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	m, ok := a.loadClassMaterial(w, r, id, u.ClassID)
	if !ok {
		return
	}

	rc, err := a.Materials.Open(m)
	if err != nil {
		writeError(w, http.StatusNotFound, "文件内容不存在")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", store.ContentType(m.FileType))
	w.Header().Set("X-File-Name", url.PathEscape(m.OriginalName))
	w.Header().Set("Content-Length", strconv.FormatInt(m.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// POST /api/materials/{id}/download-ticket
func (a *API) createTicket(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	m, ok := a.loadClassMaterial(w, r, id, u.ClassID)
	if !ok {
		return
	}
	ticket, err := a.Tickets.Create(r.Context(), m.ID, u.ID, u.ClassID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建下载凭证失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket, "fileName": m.OriginalName})
}

// loadClassMaterial 统一处理 404（不存在）与 403（跨班）判定。
func (a *API) loadClassMaterial(w http.ResponseWriter, r *http.Request, id, classID int64) (*model.Material, bool) {
	m, err := a.Materials.GetByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "材料不存在")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询材料失败")
		return nil, false
	}
	if m.ClassID != classID {
		writeError(w, http.StatusForbidden, "无权访问该班级的材料")
		return nil, false
	}
	return m, true
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "材料标识非法")
		return 0, false
	}
	return id, true
}
