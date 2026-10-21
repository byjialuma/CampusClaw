package tutor

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

const fullSkillMD = `---
name: 勾股定理解题引导
description: 面向直角三角形边长提供分步引导
适用条件: [勾股定理, 直角三角形, 斜边, 直角边]
allowed-tools:
  - formula_hint@campus-tools
  - knowledge_search
resources:
  - references/pythagorean-theorem.md
  - references/common-mistakes.md
  - assets/answer-template.md
---

# 勾股定理解题引导

不写出最终数值结果。
`

func TestParseSkillMDFullPackage(t *testing.T) {
	pkg, err := ParseSkillMD([]byte(fullSkillMD))
	if err != nil {
		t.Fatalf("解析完整技能包失败: %v", err)
	}
	if pkg.Name != "勾股定理解题引导" || !strings.Contains(pkg.Description, "直角三角形") {
		t.Fatalf("名称/描述解析不符: %+v", pkg)
	}
	wantConds := []string{"勾股定理", "直角三角形", "斜边", "直角边"}
	if len(pkg.Conditions) != len(wantConds) {
		t.Fatalf("适用条件数量不符: %v", pkg.Conditions)
	}
	for i, want := range wantConds {
		if pkg.Conditions[i] != want {
			t.Fatalf("适用条件[%d] 期望 %q 得到 %q", i, want, pkg.Conditions[i])
		}
	}
	if len(pkg.ToolRefs) != 2 || pkg.ToolRefs[0].Raw != "formula_hint@campus-tools" || !pkg.ToolRefs[1].Builtin {
		t.Fatalf("工具声明解析不符: %+v", pkg.ToolRefs)
	}
	if len(pkg.Resources) != 3 || pkg.Resources[0] != "references/pythagorean-theorem.md" {
		t.Fatalf("资源声明解析不符: %v", pkg.Resources)
	}
	if !strings.Contains(pkg.Body, "不写出最终数值结果") || strings.HasPrefix(pkg.Body, "---") {
		t.Fatalf("正文解析不符: %q", pkg.Body)
	}
	// frontmatter 原样保留未知/全部键。
	if _, ok := pkg.Frontmatter["适用条件"]; !ok {
		t.Fatalf("frontmatter 未保留适用条件: %v", pkg.Frontmatter)
	}
}

func TestParseSkillMDRequiresFrontmatterFields(t *testing.T) {
	cases := map[string]string{
		"仅正文":           "# 标题\n\n只有正文，没有 frontmatter。",
		"缺 description": "---\nname: 只有名称\n---\n正文",
		"缺 name":        "---\ndescription: 只有描述\n---\n正文",
	}
	for name, input := range cases {
		if _, err := ParseSkillMD([]byte(input)); err == nil {
			t.Fatalf("%s：期望返回错误", name)
		}
	}
}

func TestParseSkillMDInvalidSyntaxHasLineNumber(t *testing.T) {
	// 第 3 行数组缺少结束符 ]，错误必须带行号定位。
	input := "---\nname: 测试\n适用条件: [勾股定理, 直角三角形\n---\n正文"
	_, err := ParseSkillMD([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "第 3 行") {
		t.Fatalf("期望带行号的数组错误，得到 %v", err)
	}

	// 第 2 行缺少冒号，同样必须带行号。
	input = "---\nname 测试\n---\n正文"
	_, err = ParseSkillMD([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("期望带行号的语法错误，得到 %v", err)
	}
}

func TestParseSkillMDToolDeclarationMerge(t *testing.T) {
	// 两者同时存在：frontmatter 优先，重复引用去重，正文独有补在后面。
	input := `---
name: 合并测试
description: 合并
allowed-tools: [formula_hint@campus-tools]
---
工具：unit_convert@campus-tools, formula_hint@campus-tools
适用条件：单位换算
`
	pkg, err := ParseSkillMD([]byte(input))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pkg.ToolRefs) != 2 {
		t.Fatalf("合并后应有 2 条工具，得到 %+v", pkg.ToolRefs)
	}
	if pkg.ToolRefs[0].Raw != "formula_hint@campus-tools" || pkg.ToolRefs[1].Raw != "unit_convert@campus-tools" {
		t.Fatalf("frontmatter 应排在前面且去重，得到 %+v", pkg.ToolRefs)
	}
	if len(pkg.Conditions) != 1 || pkg.Conditions[0] != "单位换算" {
		t.Fatalf("frontmatter 未声明适用条件时应回退正文，得到 %v", pkg.Conditions)
	}

	// 仅正文声明。
	input = "---\nname: 仅正文工具\ndescription: d\n---\n工具：unit_convert@campus-tools\n"
	pkg, err = ParseSkillMD([]byte(input))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pkg.ToolRefs) != 1 || pkg.ToolRefs[0].Server != "campus-tools" {
		t.Fatalf("仅正文工具解析不符: %+v", pkg.ToolRefs)
	}

	// 仅 frontmatter 声明。
	input = "---\nname: 仅声明\ndescription: d\nallowed-tools: [knowledge_search]\n---\n正文"
	pkg, err = ParseSkillMD([]byte(input))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pkg.ToolRefs) != 1 || !pkg.ToolRefs[0].Builtin {
		t.Fatalf("仅 frontmatter 工具解析不符: %+v", pkg.ToolRefs)
	}
}

func TestSkillSlugNormalized(t *testing.T) {
	a := SkillSlug("  勾股定理  ")
	b := SkillSlug("勾股定理")
	if a != b {
		t.Fatalf("规范化后同名应得到同一 slug: %s vs %s", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("slug 长度应为 16，得到 %d", len(a))
	}
	if SkillSlug("单位换算") == a {
		t.Fatal("不同名称不应得到相同 slug")
	}
}

// buildZip 把 map 打成 zip；entries 以相对路径为键。
func buildZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目失败: %v", err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeSkillPackageZipValid(t *testing.T) {
	data := buildZip(t, map[string][]byte{
		"SKILL.md":                          []byte(fullSkillMD),
		"references/pythagorean-theorem.md": []byte("# 定理"),
		"references/common-mistakes.md":     []byte("# 错误"),
		"assets/answer-template.md":         []byte("# 模板"),
	})
	decoded, err := DecodeSkillPackageZip(data)
	if err != nil {
		t.Fatalf("合法技能包应解析成功: %v", err)
	}
	if decoded.Package.Name != "勾股定理解题引导" || len(decoded.Files) != 4 {
		t.Fatalf("解析结果不符: %+v", decoded)
	}
}

func TestDecodeSkillPackageZipRejects(t *testing.T) {
	valid := fullSkillMD
	cases := []struct {
		name    string
		entries map[string][]byte
		want    string
	}{
		{"缺 SKILL.md", map[string][]byte{"references/a.md": []byte("x")}, "SKILL.md"},
		{"路径穿越", map[string][]byte{"SKILL.md": []byte(valid), "../evil.md": []byte("x")}, "非法路径"},
		{"绝对路径", map[string][]byte{"SKILL.md": []byte(valid), "/etc/passwd": []byte("x")}, "非法路径"},
		{"反斜杠穿越", map[string][]byte{"SKILL.md": []byte(valid), "..\\evil.md": []byte("x")}, "非法路径"},
		{"声明资源缺失", map[string][]byte{"SKILL.md": []byte(valid)}, "声明的资源不存在"},
		{"非 UTF-8", map[string][]byte{"SKILL.md": []byte(valid), "assets/a.bin": {0xff, 0xfe}}, "UTF-8"},
	}
	for _, tc := range cases {
		if _, err := DecodeSkillPackageZip(buildZip(t, tc.entries)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s：期望错误含 %q，得到 %v", tc.name, tc.want, err)
		}
	}

	if _, err := DecodeSkillPackageZip([]byte("not a zip")); err == nil {
		t.Fatal("非 zip 输入应报错")
	}

	// 超限：压缩包体积。
	big := make([]byte, MaxSkillPackageBytes+1)
	if _, err := DecodeSkillPackageZip(big); err == nil || !strings.Contains(err.Error(), "512KB") {
		t.Fatalf("压缩包超限应报错，得到 %v", err)
	}
}

func TestDecodeSkillPackageZipRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "SKILL.md"}
	h.SetMode(0o644)
	w, _ := zw.CreateHeader(h)
	_, _ = w.Write([]byte(fullSkillMD))

	link := &zip.FileHeader{Name: "link.md"}
	link.SetMode(os.ModeSymlink | 0o777)
	lw, _ := zw.CreateHeader(link)
	_, _ = lw.Write([]byte("references/a.md"))
	_ = zw.Close()

	if _, err := DecodeSkillPackageZip(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("符号链接应被拒绝，得到 %v", err)
	}
}

func TestDecodeSkillPackageZipFileCountAndSize(t *testing.T) {
	// 文件数超出上限（SKILL.md + 200 个资源）。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("SKILL.md")
	_, _ = w.Write([]byte(fullSkillMD))
	for i := 0; i <= MaxSkillPackageFiles; i++ {
		w, _ := zw.Create("assets/" + strings.Repeat("f", 5) + string(rune('0'+i%10)) + string(rune('a'+i/10)) + ".md")
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	if _, err := DecodeSkillPackageZip(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "文件数") {
		t.Fatalf("文件数超限应报错，得到 %v", err)
	}

	// 单文件超过 512KB。
	var single bytes.Buffer
	zw = zip.NewWriter(&single)
	w, _ = zw.Create("SKILL.md")
	_, _ = w.Write([]byte(fullSkillMD))
	w, _ = zw.Create("assets/huge.md")
	_, _ = w.Write(bytes.Repeat([]byte("a"), MaxSkillPackageFileBytes+1))
	_ = zw.Close()
	if _, err := DecodeSkillPackageZip(single.Bytes()); err == nil || !strings.Contains(err.Error(), "单文件") {
		t.Fatalf("单文件超限应报错，得到 %v", err)
	}
}
