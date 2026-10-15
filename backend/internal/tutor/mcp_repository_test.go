package tutor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"campusclaw/internal/mcp"
)

// newTestRepoErr 与 newTestRepo 类似，但可注入 Exec 错误（模拟唯一键冲突）。
func newTestRepoErr(execErr error, affected int64, queryResults ...driver.Rows) (*TutorRepository, *fakeConn) {
	conn := &fakeConn{queryResults: queryResults, affected: affected, execErr: execErr}
	db := sql.OpenDB(fakeConnector{conn})
	return &TutorRepository{DB: db}, conn
}

// serverRow 组装一行 mcp_servers 查询结果。
func serverRow(id int64, name, baseURL string, token any, connected bool, cache any, lastError any) []driver.Value {
	return []driver.Value{id, name, baseURL, token, connected, cache, lastError}
}

// ---------- 4.2 列表与按 id/名读取 ----------

// 列表必须按 class_id 隔离，并还原工具清单 JSON 与 hasAuth 布尔。
func TestListMCPServersClassScoped(t *testing.T) {
	raw, _ := json.Marshal([]mcp.Tool{{Name: "formula_hint", Description: "公式提示"}})
	rows := &fakeRows{
		cols: []string{"id", "name", "base_url", "auth_token", "connected", "tool_cache", "last_error"},
		rows: [][]driver.Value{
			serverRow(3, "campus-tools", "http://mcp-demo:8090", "secret", true, raw, nil),
			serverRow(4, "no-auth", "http://x:1", nil, false, nil, nil),
		},
	}
	repo, conn := newTestRepo(0, rows)

	out, err := repo.ListMCPServers(context.Background(), 7)
	if err != nil {
		t.Fatalf("列出服务器失败: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("应返回 2 台，得到 %d", len(out))
	}
	if !out[0].HasAuth || out[0].AuthToken != "secret" || !out[0].Connected {
		t.Fatalf("第 1 台字段错误: %+v", out[0])
	}
	if len(out[0].Tools) != 1 || out[0].Tools[0].Name != "formula_hint" {
		t.Fatalf("工具清单还原错误: %+v", out[0].Tools)
	}
	if out[1].HasAuth || out[1].Connected || len(out[1].Tools) != 0 || out[1].Tools == nil {
		t.Fatalf("第 2 台字段错误: %+v", out[1])
	}

	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "WHERE class_id = ?") {
		t.Fatalf("列表必须按 class_id 过滤，语句为: %s", log.sql)
	}
	if len(log.args) != 1 || log.args[0].Value != int64(7) {
		t.Fatalf("参数必须是 class_id，实际: %v", log.args)
	}
}

// 按 id 读取：他人班级（无行）按不存在处理。
func TestGetMCPServerCrossClassNotFound(t *testing.T) {
	repo, conn := newTestRepo(0) // 查询无行 → sql.ErrNoRows
	if _, err := repo.GetMCPServer(context.Background(), 2, 9); !errors.Is(err, ErrMCPServerNotFound) {
		t.Fatalf("跨班读取期望 ErrMCPServerNotFound，得到 %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "WHERE id = ? AND class_id = ?") {
		t.Fatalf("按 id 读取必须同时限定 id 与 class_id，语句为: %s", log.sql)
	}
	if len(log.args) != 2 || log.args[0].Value != int64(9) || log.args[1].Value != int64(2) {
		t.Fatalf("参数必须是 (id, class_id)，实际: %v", log.args)
	}
}

// 按名读取同样限定本班，用于技能引用校验与编排调用。
func TestGetMCPServerByNameScoped(t *testing.T) {
	cache, _ := json.Marshal([]mcp.Tool{{Name: "unit_convert"}})
	rows := &fakeRows{
		cols: []string{"id", "name", "base_url", "auth_token", "connected", "tool_cache", "last_error"},
		rows: [][]driver.Value{serverRow(5, "campus-tools", "http://mcp-demo:8090", "t", true, cache, nil)},
	}
	repo, conn := newTestRepo(0, rows)

	s, err := repo.GetMCPServerByName(context.Background(), 1, "campus-tools")
	if err != nil {
		t.Fatalf("按名读取失败: %v", err)
	}
	if s.ID != 5 || s.Name != "campus-tools" || !s.Connected || len(s.Tools) != 1 {
		t.Fatalf("字段错误: %+v", s)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "WHERE name = ? AND class_id = ?") {
		t.Fatalf("按名读取必须限定本班，语句为: %s", log.sql)
	}
	if len(log.args) != 2 || log.args[0].Value != "campus-tools" || log.args[1].Value != int64(1) {
		t.Fatalf("参数必须是 (name, class_id)，实际: %v", log.args)
	}
}

// ---------- 4.2 登记：重名冲突 ----------

func TestCreateMCPServerNameConflict(t *testing.T) {
	repo, _ := newTestRepoErr(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, 0)
	_, err := repo.CreateMCPServer(context.Background(), 1, "campus-tools", "http://mcp-demo:8090", "tok")
	if !errors.Is(err, ErrMCPServerNameConflict) {
		t.Fatalf("重名期望 ErrMCPServerNameConflict，得到 %v", err)
	}
}

func TestCreateMCPServerSuccess(t *testing.T) {
	repo, conn := newTestRepo(1)

	s, err := repo.CreateMCPServer(context.Background(), 1, "campus-tools", "http://mcp-demo:8090", "tok")
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if s.ID != 101 || !s.HasAuth || s.AuthToken != "tok" || s.Connected || s.Tools == nil {
		t.Fatalf("登记返回字段错误: %+v", s)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "INSERT INTO mcp_servers") {
		t.Fatalf("语句错误: %s", log.sql)
	}
	if len(log.args) != 4 || log.args[0].Value != int64(1) || log.args[3].Value != "tok" {
		t.Fatalf("参数错误（班级须取自 claims），实际: %v", log.args)
	}

	// 空令牌映射为 NULL。
	repo2, conn2 := newTestRepo(1)
	if _, err := repo2.CreateMCPServer(context.Background(), 1, "n", "http://x", ""); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if lastLog(t, conn2).args[3].Value != nil {
		t.Fatalf("空令牌必须写 NULL，实际: %v", lastLog(t, conn2).args[3].Value)
	}
}

// ---------- 4.2 修改：连接状态重置 ----------

// token 为 nil 表示保留原令牌：语句不得更新 auth_token 列。
func TestUpdateMCPServerKeepTokenResetsConnection(t *testing.T) {
	after := &fakeRows{
		cols: []string{"id", "name", "base_url", "auth_token", "connected", "tool_cache", "last_error"},
		rows: [][]driver.Value{serverRow(5, "renamed", "http://new:1", "keep", false, nil, nil)},
	}
	repo, conn := newTestRepo(1, after)

	s, err := repo.UpdateMCPServer(context.Background(), 1, 5, "renamed", "http://new:1", nil)
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if s.Name != "renamed" || !s.HasAuth || s.AuthToken != "keep" {
		t.Fatalf("保留令牌语义错误: %+v", s)
	}
	exec := conn.logs[0]
	if strings.Contains(exec.sql, "auth_token = ?") {
		t.Fatalf("token 为 nil 时不得更新 auth_token，语句为: %s", exec.sql)
	}
	if !strings.Contains(exec.sql, "connected = 0") || !strings.Contains(exec.sql, "tool_cache = NULL") {
		t.Fatalf("修改必须重置连接与清单，语句为: %s", exec.sql)
	}
	if !strings.Contains(exec.sql, "WHERE id = ? AND class_id = ?") {
		t.Fatalf("修改必须限定本班，语句为: %s", exec.sql)
	}
	if len(exec.args) != 4 || exec.args[3].Value != int64(1) {
		t.Fatalf("参数必须含 class_id，实际: %v", exec.args)
	}
}

// token 非 nil：以给定值覆盖 auth_token。
func TestUpdateMCPServerReplaceToken(t *testing.T) {
	after := &fakeRows{
		cols: []string{"id", "name", "base_url", "auth_token", "connected", "tool_cache", "last_error"},
		rows: [][]driver.Value{serverRow(5, "campus-tools", "http://mcp-demo:8090", "newtok", false, nil, nil)},
	}
	repo, conn := newTestRepo(1, after)
	newTok := "newtok"

	s, err := repo.UpdateMCPServer(context.Background(), 1, 5, "campus-tools", "http://mcp-demo:8090", &newTok)
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if s.AuthToken != "newtok" || !s.HasAuth {
		t.Fatalf("覆盖令牌失败: %+v", s)
	}
	exec := conn.logs[0]
	if !strings.Contains(exec.sql, "auth_token = ?") || exec.args[2].Value != "newtok" {
		t.Fatalf("token 非 nil 时应覆盖 auth_token，实际: %v", exec.args)
	}
}

func TestUpdateMCPServerNameConflict(t *testing.T) {
	repo, _ := newTestRepoErr(&mysql.MySQLError{Number: 1062}, 0)
	tok := "t"
	_, err := repo.UpdateMCPServer(context.Background(), 1, 5, "dup", "http://x", &tok)
	if !errors.Is(err, ErrMCPServerNameConflict) {
		t.Fatalf("重名期望 ErrMCPServerNameConflict，得到 %v", err)
	}
}

// ---------- 4.2 删除 ----------

func TestDeleteMCPServerNotFound(t *testing.T) {
	repo, conn := newTestRepo(0)
	if err := repo.DeleteMCPServer(context.Background(), 1, 9); !errors.Is(err, ErrMCPServerNotFound) {
		t.Fatalf("期望 ErrMCPServerNotFound，得到 %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "WHERE id = ? AND class_id = ?") {
		t.Fatalf("删除必须限定本班，语句为: %s", log.sql)
	}
}

// ---------- 4.2 连接/断开 ----------

// 连接成功：置 connected=1 并缓存清单，最长 50 条。
func TestMarkMCPServerConnectedCachesAndTruncates(t *testing.T) {
	repo, conn := newTestRepo(1)
	tools := make([]mcp.Tool, 0, 55)
	for i := 0; i < 55; i++ {
		tools = append(tools, mcp.Tool{Name: "t" + string(rune('a'+i%26))})
	}
	if err := repo.MarkMCPServerConnected(context.Background(), 1, 5, tools); err != nil {
		t.Fatalf("标记连接失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "connected = 1") || !strings.Contains(log.sql, "tool_cache = ?") {
		t.Fatalf("语句错误: %s", log.sql)
	}
	var cached []mcp.Tool
	if err := json.Unmarshal([]byte(log.args[0].Value.(string)), &cached); err != nil {
		t.Fatalf("缓存必须是合法 JSON: %v", err)
	}
	if len(cached) != MCPToolCacheLimit {
		t.Fatalf("清单应截断为 %d，得到 %d", MCPToolCacheLimit, len(cached))
	}
}

// 连接失败：保持未连接、清空清单，并截断超长错误文案。
func TestMarkMCPServerConnectFailedTruncatesError(t *testing.T) {
	repo, conn := newTestRepo(1)
	longErr := strings.Repeat("错", 600)
	if err := repo.MarkMCPServerConnectFailed(context.Background(), 1, 5, longErr); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "connected = 0") || !strings.Contains(log.sql, "tool_cache = NULL") {
		t.Fatalf("语句错误: %s", log.sql)
	}
	if got := len([]rune(log.args[0].Value.(string))); got != 512 {
		t.Fatalf("错误文案应截断为 512 字，得到 %d", got)
	}
}

// 断开：置 connected=0 并清空清单。
func TestDisconnectMCPServerClearsCache(t *testing.T) {
	repo, conn := newTestRepo(1)
	if err := repo.DisconnectMCPServer(context.Background(), 1, 5); err != nil {
		t.Fatalf("断开失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "connected = 0") || !strings.Contains(log.sql, "tool_cache = NULL") {
		t.Fatalf("断开必须清空清单，语句为: %s", log.sql)
	}
}

// 影响行数为 0 且行不存在 → 不存在哨兵。
func TestMarkMCPServerConnectedNotFound(t *testing.T) {
	repo, _ := newTestRepo(0) // UPDATE 0 行；随后存在性查询无行
	err := repo.MarkMCPServerConnected(context.Background(), 1, 9, nil)
	if !errors.Is(err, ErrMCPServerNotFound) {
		t.Fatalf("期望 ErrMCPServerNotFound，得到 %v", err)
	}
}

// ---------- 4.2 审计 ----------

// 审计写入：0 值列映射为 NULL，class_id/user_id 取自入参。
func TestRecordMCPCallNullMapping(t *testing.T) {
	repo, conn := newTestRepo(1)
	if err := repo.RecordMCPCall(context.Background(), MCPCallInput{
		ClassID: 1, UserID: 2, ServerID: 0, ServerName: "campus-tools",
		ConversationID: 0, ToolRef: "formula_hint@campus-tools",
		Status: MCPStatusDenied, Error: "", DurationMS: 0,
	}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "INSERT INTO mcp_calls") {
		t.Fatalf("语句错误: %s", log.sql)
	}
	if len(log.args) != 9 {
		t.Fatalf("参数应为 9 个，得到 %d: %v", len(log.args), log.args)
	}
	if log.args[0].Value != int64(1) || log.args[1].Value != int64(2) {
		t.Fatalf("班级/用户必须取自入参，实际: %v", log.args)
	}
	if log.args[2].Value != nil {
		t.Fatalf("server_id=0 必须写 NULL，实际: %v", log.args[2].Value)
	}
	if log.args[4].Value != nil {
		t.Fatalf("conversation_id=0 必须写 NULL，实际: %v", log.args[4].Value)
	}
	if log.args[7].Value != nil {
		t.Fatalf("空错误文案必须写 NULL，实际: %v", log.args[7].Value)
	}
}

// 最近记录按本班过滤、新→旧，并读取 LEFT JOIN 的用户显示名；server_id 为 NULL 的历史行仍可读。
func TestRecentMCPCallsScopedAndNullServer(t *testing.T) {
	now := time.Now()
	rows := &fakeRows{
		cols: []string{"id", "server_id", "server_name", "tool_ref", "status", "error",
			"duration_ms", "user_id", "display_name", "conversation_id", "created_at"},
		rows: [][]driver.Value{
			{int64(12), nil, "campus-tools", "formula_hint@campus-tools", MCPStatusSuccess, nil,
				int64(15), int64(2), "学生A1", int64(8), now},
			{int64(11), int64(5), "campus-tools", "unit_convert@campus-tools", MCPStatusError, "远端调用失败",
				int64(3), nil, nil, nil, now},
		},
	}
	repo, conn := newTestRepo(0, rows)

	out, err := repo.RecentMCPCalls(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("应返回 2 条，得到 %d", len(out))
	}
	if out[0].ServerID != nil || out[0].Status != MCPStatusSuccess || out[0].Username != "学生A1" {
		t.Fatalf("第 1 条字段错误: %+v", out[0])
	}
	if out[1].ServerName != "campus-tools" || out[1].Error != "远端调用失败" || out[1].ServerID == nil {
		t.Fatalf("第 2 条字段错误: %+v", out[1])
	}
	if out[1].Username != "" || out[1].UserID != nil {
		t.Fatalf("已删除用户场景应留空，得到 %+v", out[1])
	}

	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "LEFT JOIN users") || !strings.Contains(log.sql, "WHERE c.class_id = ?") {
		t.Fatalf("审计查询必须按本班过滤并 LEFT JOIN 用户，语句为: %s", log.sql)
	}
	if !strings.Contains(log.sql, "ORDER BY c.id DESC") {
		t.Fatalf("审计应按新→旧排序，语句为: %s", log.sql)
	}
	if len(log.args) != 2 || log.args[0].Value != int64(1) || log.args[1].Value != int64(50) {
		t.Fatalf("参数必须是 (class_id, limit)，实际: %v", log.args)
	}
}

// 无工具清单（NULL 缓存）时读为空切片而非 nil，避免响应出现 null。
func TestScanMCPServerNullCacheEmptySlice(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "base_url", "auth_token", "connected", "tool_cache", "last_error"},
		rows: [][]driver.Value{serverRow(1, "n", "http://x", nil, false, nil, nil)},
	}
	repo, _ := newTestRepo(0, rows)
	s, err := repo.GetMCPServer(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if s.Tools == nil || len(s.Tools) != 0 {
		t.Fatalf("NULL 缓存应读为空切片，得到 %+v", s.Tools)
	}
}
