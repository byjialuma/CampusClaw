package tutor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStorage(t *testing.T) (*SkillStorage, string) {
	t.Helper()
	root := t.TempDir()
	st, err := NewSkillStorage(root)
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	return st, root
}

func TestWritePackagePathAndCleanup(t *testing.T) {
	st, root := newTestStorage(t)
	rel, err := st.WritePackage(3, 42, map[string][]byte{
		"SKILL.md":                  []byte("---\nname: n\ndescription: d\n---\n正文"),
		"references/theorem.md":     []byte("# 定理"),
		"scripts/check_triangle.py": []byte("print(1)"),
	})
	if err != nil {
		t.Fatalf("写入技能包失败: %v", err)
	}
	if rel != "skills/3/42" {
		t.Fatalf("目录相对路径不符: %s", rel)
	}
	abs, err := st.absDir(rel)
	if err != nil {
		t.Fatalf("解析目录失败: %v", err)
	}
	if !strings.HasPrefix(abs, filepath.Join(root, "skills")) {
		t.Fatalf("目录必须位于 DATA_DIR/skills 下: %s", abs)
	}
	if _, err := os.Stat(filepath.Join(abs, "references", "theorem.md")); err != nil {
		t.Fatalf("资源未落盘: %v", err)
	}

	// 含越界路径时整体回滚：目录不残留。
	if _, err := st.WritePackage(3, 43, map[string][]byte{
		"SKILL.md":     []byte("x"),
		"../escape.md": []byte("x"),
	}); err == nil {
		t.Fatal("越界路径应报错")
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "3", "43")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("失败后应清理目录，stat 得到 %v", err)
	}
}

func TestResourceListAndRead(t *testing.T) {
	st, _ := newTestStorage(t)
	if _, err := st.WritePackage(1, 7, map[string][]byte{
		"SKILL.md":         []byte("x"),
		"references/a.md":  []byte("abc"),
		"assets/t.md":      []byte("de"),
		"scripts/check.py": []byte("f"),
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	files, err := st.ResourceList("skills/1/7")
	if err != nil {
		t.Fatalf("列资源失败: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("应列出 3 个资源（不含 SKILL.md），得到 %+v", files)
	}
	kinds := map[string]bool{}
	for _, f := range files {
		kinds[f.Kind] = true
	}
	for _, want := range []string{"references", "assets", "scripts"} {
		if !kinds[want] {
			t.Fatalf("缺少资源类别 %s: %+v", want, files)
		}
	}

	data, isText, err := st.ReadResource("skills/1/7", "references/a.md")
	if err != nil || string(data) != "abc" || !isText {
		t.Fatalf("读取资源不符: %q text=%v err=%v", data, isText, err)
	}

	for _, bad := range []string{"../SKILL.md", "/etc/passwd", "references/../../x.md", "unknown/a.md", "SKILL.md"} {
		if _, _, err := st.ReadResource("skills/1/7", bad); !errors.Is(err, ErrInvalidSkillResourcePath) {
			t.Fatalf("路径 %q 应判定为非法，得到 %v", bad, err)
		}
	}

	if _, _, err := st.ReadResource("skills/1/7", "references/missing.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失文件应返回 NotExist，得到 %v", err)
	}
}

func TestLoadResourcesLimits(t *testing.T) {
	st, _ := newTestStorage(t)
	files := map[string][]byte{
		"SKILL.md":          []byte("x"),
		"references/a.md":   []byte("aaa"),
		"references/big.md": []byte(strings.Repeat("中", SkillResourceFileMaxBytes)), // 3 字节/字，超出单文件上限
		"references/c.md":   []byte(strings.Repeat("c", SkillResourceTotalMaxBytes)),
	}
	for i := 0; i < 6; i++ {
		files["assets/e"+string(rune('0'+i))+".md"] = []byte("x")
	}
	if _, err := st.WritePackage(1, 8, files); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	statuses, data := st.LoadResources("skills/1/8", []string{
		"references/a.md",
		"references/big.md",
		"references/c.md",
		"assets/e0.md",
		"assets/e1.md",
		"assets/e2.md",
	})
	if !strings.Contains(data, "references/a.md") || !strings.Contains(data, "aaa") {
		t.Fatalf("正常资源应进入数据块: %q", data)
	}
	if !strings.Contains(data, "技能资源") {
		t.Fatalf("数据块缺少说明: %q", data)
	}
	var truncated bool
	var skipped bool
	for _, st := range statuses {
		if st.Path == "references/big.md" && st.Truncated {
			truncated = true
		}
		if st.Path == "assets/e2.md" && st.Error != "" {
			skipped = true
		}
	}
	if !truncated {
		t.Fatalf("超大单文件应标记截断: %+v", statuses)
	}
	if !skipped {
		t.Fatalf("第 6 条资源应因数量上限被跳过: %+v", statuses)
	}

	// 读取失败不阻断：缺失资源只记录状态，不影响其余资源。
	statuses, data = st.LoadResources("skills/1/8", []string{"references/missing.md", "references/a.md"})
	if len(statuses) != 2 || statuses[0].Error == "" || !statuses[1].Loaded || !strings.Contains(data, "aaa") {
		t.Fatalf("读取失败处理不符: %+v data=%q", statuses, data)
	}

	// 未声明资源或目录为空时不读取。
	if s, d := st.LoadResources("skills/1/8", nil); s != nil || d != "" {
		t.Fatalf("无声明资源应返回空: %+v %q", s, d)
	}
	if s, d := st.LoadResources("", []string{"references/a.md"}); s != nil || d != "" {
		t.Fatalf("无包目录应返回空: %+v %q", s, d)
	}
}

func TestRemovePackage(t *testing.T) {
	st, root := newTestStorage(t)
	rel, err := st.WritePackage(2, 5, map[string][]byte{"SKILL.md": []byte("x")})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := st.RemovePackage(rel); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "2", "5")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("目录应已删除: %v", err)
	}
	if err := st.RemovePackage(""); err != nil {
		t.Fatalf("空目录应静默通过: %v", err)
	}
}
