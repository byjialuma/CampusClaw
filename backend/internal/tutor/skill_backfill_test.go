package tutor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// bfConn 是回填测试专用 fake driver：固定返回一组行，Exec 结果按队列返回。
type bfConn struct {
	mu       sync.Mutex
	rows     driver.Rows
	execErrs []error
	logs     []queryLog
}

func (c *bfConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare 不应被触发")
}
func (c *bfConn) Close() error              { return nil }
func (c *bfConn) Begin() (driver.Tx, error) { return nil, errors.New("不使用事务") }

func (c *bfConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, queryLog{query, args})
	r := c.rows
	c.rows = nil
	if r == nil {
		return &fakeRows{}, nil
	}
	return r, nil
}

func (c *bfConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, queryLog{query, args})
	if len(c.execErrs) > 0 {
		err := c.execErrs[0]
		c.execErrs = c.execErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return fakeResult{n: 1}, nil
}

type bfConnector struct{ conn *bfConn }

func (c bfConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c bfConnector) Driver() driver.Driver                        { return bfDriver{} }

type bfDriver struct{}

func (bfDriver) Open(string) (driver.Conn, error) { return nil, errors.New("使用 OpenDB") }

func newBackfillDB(rows *fakeRows, execErrs ...error) (*sql.DB, *bfConn) {
	conn := &bfConn{rows: rows, execErrs: execErrs}
	return sql.OpenDB(bfConnector{conn}), conn
}

func updateCount(conn *bfConn) int {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	n := 0
	for _, log := range conn.logs {
		if strings.Contains(log.sql, "UPDATE skills") {
			n++
		}
	}
	return n
}

var backfillCols = []string{"id", "class_id", "name", "description", "content", "dir_path", "slug", "frontmatter"}

func TestDeriveDescription(t *testing.T) {
	cases := []struct {
		name, desc, content, want string
	}{
		{"优先原描述", "已有描述", "# 标题\n正文", "已有描述"},
		{"空描述取正文首行", "", "## 解题引导\n\n正文", "解题引导"},
		{"跳过工具与适用条件行", "", "工具：formula_hint@campus-tools\n适用条件：集合\n真正的首行", "真正的首行"},
		{"空描述且正文为空", "", "", "空描述 技能"},
	}
	for _, tc := range cases {
		got := deriveDescription("空描述", tc.desc, tc.content)
		if got != tc.want {
			t.Fatalf("%s：期望 %q 得到 %q", tc.name, tc.want, got)
		}
	}

	long := strings.Repeat("长", 200)
	got := deriveDescription("名称", "", long)
	if len([]rune(got)) != 120 {
		t.Fatalf("超长首行应截断到 120 字，得到 %d", len([]rune(got)))
	}
}

func TestBackfillSkillPackagesFaultTolerantAndIdempotent(t *testing.T) {
	st, root := newTestStorage(t)
	// 让 class 1 的写入失败：skills/1 先占位为普通文件。
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o750); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "1"), []byte("x"), 0o640); err != nil {
		t.Fatalf("写入占位文件失败: %v", err)
	}

	rows := &fakeRows{cols: backfillCols, rows: [][]driver.Value{
		{int64(1), int64(1), "解题引导", nil, "学生正在解题。\n适用条件：集合", nil, nil, nil},
		{int64(2), int64(2), "单位换算", "换算工具", "工具：unit_convert@campus-tools", nil, nil, nil},
	}}
	db, conn := newBackfillDB(rows)
	if err := BackfillSkillPackages(context.Background(), db, st); err != nil {
		t.Fatalf("回填不应返回整体错误: %v", err)
	}
	if updateCount(conn) != 1 {
		t.Fatalf("失败技能不应回填，成功技能应回填一次，实际 %d", updateCount(conn))
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "2", "2", SkillManifestName)); err != nil {
		t.Fatalf("成功技能应生成 SKILL.md: %v", err)
	}

	// 第二次启动：已回填且 SKILL.md 仍在的技能应跳过。
	rows = &fakeRows{cols: backfillCols, rows: [][]driver.Value{
		{int64(1), int64(1), "解题引导", nil, "学生正在解题。\n适用条件：集合", nil, nil, nil},
		{int64(2), int64(2), "单位换算", "换算工具", "工具：unit_convert@campus-tools",
			"skills/2/2", "abc", []byte(`{"name":"单位换算"}`)},
	}}
	db, conn = newBackfillDB(rows)
	if err := BackfillSkillPackages(context.Background(), db, st); err != nil {
		t.Fatalf("重复回填不应报错: %v", err)
	}
	if updateCount(conn) != 0 {
		t.Fatalf("已回填技能不应重复更新，实际 %d", updateCount(conn))
	}
}

func TestBackfillSlugConflictAppendsID(t *testing.T) {
	st, _ := newTestStorage(t)
	rows := &fakeRows{cols: backfillCols, rows: [][]driver.Value{
		{int64(5), int64(7), "单位换算", "换算", "工具：unit_convert@campus-tools", nil, nil, nil},
	}}
	db, conn := newBackfillDB(rows, &mysql.MySQLError{Number: 1062}, nil)
	if err := BackfillSkillPackages(context.Background(), db, st); err != nil {
		t.Fatalf("回填不应返回整体错误: %v", err)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	var updates []queryLog
	for _, log := range conn.logs {
		if strings.Contains(log.sql, "UPDATE skills") {
			updates = append(updates, log)
		}
	}
	if len(updates) != 2 {
		t.Fatalf("冲突后应重试一次，实际 %d 次", len(updates))
	}
	want := SkillSlug("单位换算") + "-5"
	if got, _ := updates[1].args[0].Value.(string); got != want {
		t.Fatalf("冲突后 slug 应为 %q，得到 %q", want, got)
	}
}
