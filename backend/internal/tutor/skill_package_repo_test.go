package tutor

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestSlugExistsClassScoped(t *testing.T) {
	repo, conn := newTestRepo(0, &fakeRows{
		cols: []string{"1"},
		rows: [][]driver.Value{{int64(1)}},
	})
	ok, err := repo.SlugExists(context.Background(), 3, "abc123")
	if err != nil || !ok {
		t.Fatalf("应判定 slug 已存在: ok=%v err=%v", ok, err)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "a.class_id = ?") {
		t.Fatalf("slug 查询必须限定本班，语句为: %s", log.sql)
	}
	if len(log.args) != 2 || log.args[0].Value != int64(3) || log.args[1].Value != "abc123" {
		t.Fatalf("参数应为 (class_id, slug)，实际 %v", log.args)
	}

	repo, _ = newTestRepo(0) // 无行 → 不存在
	ok, err = repo.SlugExists(context.Background(), 3, "none")
	if err != nil || ok {
		t.Fatalf("无行时应判定不存在: ok=%v err=%v", ok, err)
	}
}

func TestCreateSkillPackageClassScoped(t *testing.T) {
	repo, conn := newTestRepo(1)
	pkg := &SkillPackage{
		Name:        "勾股定理解题引导",
		Description: "分步引导",
		Body:        "# 正文",
		Conditions:  []string{"勾股定理"},
		ToolRefs:    []ToolRef{{Raw: "formula_hint@campus-tools", Name: "formula_hint", Server: "campus-tools"}},
		Resources:   []string{"references/a.md"},
		Frontmatter: map[string]any{"name": "勾股定理解题引导"},
	}
	sk, err := repo.CreateSkillPackage(context.Background(), 1, pkg, "slug123")
	if err != nil {
		t.Fatalf("写入技能包失败: %v", err)
	}
	if sk.ID != 101 || sk.Name != pkg.Name || sk.Source != SkillSourcePackage || sk.Slug != "slug123" {
		t.Fatalf("返回技能字段不符: %+v", sk)
	}
	if len(sk.Resources) != 1 || len(sk.Conditions) != 1 || len(sk.ToolRefs) != 1 {
		t.Fatalf("技能包元信息未带回: %+v", sk)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "FROM assistants a WHERE a.class_id = ?") {
		t.Fatalf("插入必须按本班助手解析，语句为: %s", log.sql)
	}
	if len(log.args) != 7 || log.args[6].Value != int64(1) {
		t.Fatalf("最后一个参数必须是 class_id，实际 %v", log.args)
	}
}

func TestCreateSkillPackageConflict(t *testing.T) {
	repo, conn := newTestRepo(1)
	conn.execErr = &mysql.MySQLError{Number: 1062}
	_, err := repo.CreateSkillPackage(context.Background(), 1, &SkillPackage{Name: "重复"}, "dup")
	if !errors.Is(err, ErrSkillPackageConflict) {
		t.Fatalf("期望 ErrSkillPackageConflict，得到 %v", err)
	}
}

func TestGetSkillPackageNotFound(t *testing.T) {
	repo, _ := newTestRepo(0)
	_, err := repo.GetSkillPackage(context.Background(), 1, 404)
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("期望 ErrSkillNotFound，得到 %v", err)
	}
}

func TestGetSkillPackageMapsFrontmatter(t *testing.T) {
	fm := []byte(`{"name":"勾股定理解题引导","适用条件":["勾股定理","斜边"],` +
		`"allowed-tools":["formula_hint@campus-tools"],"resources":["references/a.md"]}`)
	rows := &fakeRows{
		cols: []string{"id", "name", "description", "content", "source", "builtin", "enabled",
			"slug", "frontmatter", "dir_path"},
		rows: [][]driver.Value{{int64(9), "勾股定理解题引导", "引导", "# 正文", "package", false, true,
			"slug9", fm, "skills/1/9"}},
	}
	repo, conn := newTestRepo(0, rows)
	sk, err := repo.GetSkillPackage(context.Background(), 1, 9)
	if err != nil {
		t.Fatalf("读取技能包失败: %v", err)
	}
	if sk.Slug != "slug9" || sk.DirPath != "skills/1/9" || len(sk.Resources) != 1 || len(sk.Conditions) != 2 {
		t.Fatalf("frontmatter 元信息映射不符: %+v", sk)
	}
	if len(sk.ToolRefs) != 1 || sk.ToolRefs[0].Server != "campus-tools" {
		t.Fatalf("工具引用映射不符: %+v", sk.ToolRefs)
	}
	log := lastLog(t, conn)
	if !strings.Contains(log.sql, "a.class_id = ?") {
		t.Fatalf("读取必须限定本班: %s", log.sql)
	}
	if len(log.args) != 2 || log.args[0].Value != int64(9) || log.args[1].Value != int64(1) {
		t.Fatalf("参数应为 (skill_id, class_id)，实际 %v", log.args)
	}
}

// setSkillPath 的 0 行更新需要区分「已存在但值未变」与「不存在」。
func TestSetSkillPackagePathNotFound(t *testing.T) {
	repo, _ := newTestRepo(0) // UPDATE 0 行，存在性查询也落空
	err := repo.SetSkillPackagePath(context.Background(), 1, 404, "skills/1/404", "hash")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("期望 ErrSkillNotFound，得到 %v", err)
	}

	repo, _ = newTestRepo(0, &fakeRows{cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}})
	if err := repo.SetSkillPackagePath(context.Background(), 1, 5, "skills/1/5", "hash"); err != nil {
		t.Fatalf("技能存在时应视为成功: %v", err)
	}
}
