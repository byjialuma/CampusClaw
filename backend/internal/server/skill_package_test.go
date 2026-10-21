package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"campusclaw/internal/mcp"
	tutorpkg "campusclaw/internal/tutor"
)

const testSkillMD = `---
name: 勾股定理解题引导
description: 面向直角三角形边长提供分步引导
适用条件: [勾股定理, 直角三角形, 斜边]
allowed-tools:
  - formula_hint@campus-tools
resources:
  - references/a.md
---

# 正文

不写出最终数值结果。
`

// packageZip 打包一个最小技能包（SKILL.md + 声明资源）。
func packageZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"SKILL.md":         testSkillMD,
		"references/a.md":  "# 参考资料",
		"scripts/check.py": "print(1)",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

func multipartRequest(t *testing.T, a *API, method, path string, file []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "skill.zip")
	if err != nil {
		t.Fatalf("创建表单失败: %v", err)
	}
	if _, err := fw.Write(file); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	req := signedReq(t, a, testUser("teacherA"), method, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestTutorSkillPackageUploadAndRead(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)
	st, err := tutorpkg.NewSkillStorage(t.TempDir())
	if err != nil {
		t.Fatalf("初始化技能包存储失败: %v", err)
	}
	api.SkillStorage = st

	// campus-tools 已连接且提供 formula_hint，满足上传时的工具引用校验。
	mcpStore := newFakeMCPStore()
	srv, err := mcpStore.CreateMCPServer(context.Background(), 1, "campus-tools", "http://mcp-demo:8090", "token")
	if err != nil {
		t.Fatalf("登记 MCP 服务器失败: %v", err)
	}
	mcpStore.entries[srv.ID].server.Connected = true
	mcpStore.entries[srv.ID].server.Tools = []mcp.Tool{{Name: "formula_hint"}}
	api.MCPStore = mcpStore

	// 上传合法技能包：201，返回声明资源与包内文件清单。
	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, multipartRequest(t, api, http.MethodPost, "/api/tutor/assistant/skills/package", packageZip(t)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传期望 201，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var detail skillPackageView
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("解析上传响应失败: %v", err)
	}
	if detail.ID == 0 || detail.Name != "勾股定理解题引导" || len(detail.Resources) != 1 {
		t.Fatalf("上传响应字段不符: %+v", detail)
	}
	kindSeen := map[string]bool{}
	for _, f := range detail.Files {
		kindSeen[f.Kind] = true
	}
	if !kindSeen["references"] || !kindSeen["scripts"] {
		t.Fatalf("资源清单应覆盖 references 与 scripts: %+v", detail.Files)
	}
	if len(detail.Tools) != 1 || detail.Tools[0].Server != "campus-tools" {
		t.Fatalf("工具引用不符: %+v", detail.Tools)
	}
	skillPath := "/api/tutor/assistant/skills/" + strconv.FormatInt(detail.ID, 10)

	// 详情：200。
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, signedReq(t, api, testUser("teacherA"), http.MethodGet, skillPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("详情期望 200，得到 %d", rec.Code)
	}

	// 资源读取：200 + no-store。
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, signedReq(t, api, testUser("teacherA"), http.MethodGet,
		skillPath+"/resources?path=references/a.md", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "# 参考资料" {
		t.Fatalf("资源读取不符: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("资源响应必须禁止缓存: %v", rec.Header())
	}

	// 越界路径：400。
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, signedReq(t, api, testUser("teacherA"), http.MethodGet,
		skillPath+"/resources?path=../SKILL.md", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("越界路径期望 400，得到 %d", rec.Code)
	}

	// 跨班：B 班教师读 A 班技能 → 404。
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, signedReq(t, api, testUser("teacherB"), http.MethodGet, skillPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨班读取期望 404，得到 %d", rec.Code)
	}

	// 同班同名再次上传：409。
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, multipartRequest(t, api, http.MethodPost, "/api/tutor/assistant/skills/package", packageZip(t)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("同名技能包期望 409，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 删除后目录清理，再次读取 404。
	rec = httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, signedReq(t, api, testUser("teacherA"), http.MethodDelete, skillPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("删除期望 200，得到 %d", rec.Code)
	}
	dirPath := "skills/1/" + strconv.FormatInt(detail.ID, 10)
	if _, _, err := st.ReadResource(dirPath, "references/a.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除后技能包目录应已清理，得到 %v", err)
	}
}

// 缺少 SKILL.md 的包在解析阶段返回 400。
func TestTutorSkillPackageMissingManifest(t *testing.T) {
	store := newSrvTutorStore()
	api, _ := newTutorTestAPI(store, &srvFakeStreamLLM{}, nil, nil)
	st, err := tutorpkg.NewSkillStorage(t.TempDir())
	if err != nil {
		t.Fatalf("初始化技能包存储失败: %v", err)
	}
	api.SkillStorage = st

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("references/a.md")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()

	rec := httptest.NewRecorder()
	api.NewHandler().ServeHTTP(rec, multipartRequest(t, api, http.MethodPost, "/api/tutor/assistant/skills/package", buf.Bytes()))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SKILL.md") {
		t.Fatalf("缺 SKILL.md 期望 400，得到 %d: %s", rec.Code, rec.Body.String())
	}
}
