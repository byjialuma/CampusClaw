package tutor

import (
	"strings"
	"testing"
)

// ---------- 3.2 词法路由 RouteSkill ----------

// table-driven：最高分命中、同分取小 id、零命中、未声明条件得 0 分、候选集外不入选。
func TestRouteSkill(t *testing.T) {
	skill := func(id int64, name, content string) Skill {
		return Skill{ID: id, Name: name, Content: content, Enabled: true}
	}

	cases := []struct {
		name     string
		skills   []Skill
		question string
		wantID   int64 // 0 表示零命中
	}{
		{
			name: "按命中关键词个数取最高分",
			skills: []Skill{
				skill(7, "低分", "适用条件：集合"),
				skill(8, "高分", "适用条件：集合，子集"),
			},
			question: "什么是集合的子集",
			wantID:   8,
		},
		{
			name: "同分取 id 较小者",
			skills: []Skill{
				skill(9, "大 id", "适用条件：集合"),
				skill(3, "小 id", "适用条件：集合"),
			},
			question: "什么是集合",
			wantID:   3,
		},
		{
			name: "零命中不选技能",
			skills: []Skill{
				skill(7, "s", "适用条件：函数"),
			},
			question: "什么是集合",
			wantID:   0,
		},
		{
			name: "未声明适用条件得 0 分",
			skills: []Skill{
				skill(7, "无条件", "这是一段没有任何适用条件的正文。"),
			},
			question: "任意问题",
			wantID:   0,
		},
		{
			name: "候选集外技能不入选",
			skills: []Skill{
				skill(7, "已绑定且命中", "适用条件：集合"),
			},
			question: "集合的概念", // 候选集仅含 1 条，未绑定者不在入参中自然不入选
			wantID:   7,
		},
		{
			name:     "空候选集零命中",
			skills:   nil,
			question: "任意问题",
			wantID:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteSkill(tc.skills, tc.question)
			if tc.wantID == 0 {
				if got != nil {
					t.Fatalf("应零命中，得到 %+v", got)
				}
				return
			}
			if got == nil || got.ID != tc.wantID {
				t.Fatalf("应命中技能 %d，得到 %+v", tc.wantID, got)
			}
		})
	}
}

// 同一输入多次调用结果一致（确定性）。
func TestRouteSkillDeterministic(t *testing.T) {
	skills := []Skill{
		{ID: 5, Name: "a", Content: "适用条件：集合", Enabled: true},
		{ID: 2, Name: "b", Content: "适用条件：集合", Enabled: true},
	}
	first := RouteSkill(skills, "集合")
	for i := 0; i < 5; i++ {
		if got := RouteSkill(skills, "集合"); got == nil || got.ID != first.ID {
			t.Fatalf("路由结果不稳定: %v vs %v", got, first)
		}
	}
	if first == nil || first.ID != 2 {
		t.Fatalf("同分应取较小 id=2，得到 %+v", first)
	}
}

// ASCII 关键词大小写折叠：问题用大写形式仍应命中。
func TestRouteSkillCaseFolding(t *testing.T) {
	skills := []Skill{{ID: 1, Name: "s", Content: "适用条件：Python", Enabled: true}}
	if got := RouteSkill(skills, "如何学习 PYTHON 函数"); got == nil || got.ID != 1 {
		t.Fatalf("大小写折叠后应命中，得到 %+v", got)
	}
}

// ---------- 4.3 技能工具栏解析器 ----------

// 多引用（半角逗号/全角逗号/顿号混用）与空行忽略。
func TestParseSkillToolsMultiRefsAndSeparators(t *testing.T) {
	content := "" +
		"解题时请参考工具。\n" +
		"工具：formula_hint@campus-tools，unit_convert@campus-tools、knowledge_search\n" +
		"\n" +
		"   \n"

	got, err := ParseSkillTools(content)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !got.HasToolLine {
		t.Fatal("应识别到「工具」行")
	}
	if len(got.Refs) != 3 {
		t.Fatalf("应解析出 3 条引用，得到 %+v", got.Refs)
	}
	if got.Refs[0] != (ToolRef{Raw: "formula_hint@campus-tools", Name: "formula_hint", Server: "campus-tools"}) {
		t.Fatalf("第 1 条引用错误: %+v", got.Refs[0])
	}
	if got.Refs[1] != (ToolRef{Raw: "unit_convert@campus-tools", Name: "unit_convert", Server: "campus-tools"}) {
		t.Fatalf("第 2 条引用错误: %+v", got.Refs[1])
	}
	if got.Refs[2] != (ToolRef{Raw: "knowledge_search", Name: "knowledge_search", Builtin: true}) {
		t.Fatalf("第 3 条内置引用错误: %+v", got.Refs[2])
	}
	if got.Condition != nil {
		t.Fatalf("未声明适用条件时应为空，得到 %v", got.Condition)
	}
}

// 「适用条件」行按分隔符解析为关键词列表。
func TestParseSkillToolsConditionLine(t *testing.T) {
	content := "工具：formula_hint@campus-tools\n适用条件：函数，方程、不等式"

	got, err := ParseSkillTools(content)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := []string{"函数", "方程", "不等式"}
	if len(got.Condition) != len(want) {
		t.Fatalf("适用条件数量错误: %+v", got.Condition)
	}
	for i := range want {
		if got.Condition[i] != want[i] {
			t.Fatalf("适用条件第 %d 项错误: %+v", i, got.Condition)
		}
	}
}

// 仅 knowledge_search：判为内置引用且无服务器段。
func TestParseSkillToolsOnlyBuiltin(t *testing.T) {
	got, err := ParseSkillTools("工具：knowledge_search")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(got.Refs) != 1 || !got.Refs[0].Builtin || got.Refs[0].Server != "" {
		t.Fatalf("内置引用解析错误: %+v", got.Refs)
	}
}

// 无「工具」行：不产生任何引用。
func TestParseSkillToolsNoToolLine(t *testing.T) {
	got, err := ParseSkillTools("这是一段普通技能正文，没有工具声明。\n适用条件：任何")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.HasToolLine || len(got.Refs) != 0 {
		t.Fatalf("无工具行时不应产生引用: %+v", got)
	}
	if len(got.Condition) != 1 || got.Condition[0] != "任何" {
		t.Fatalf("适用条件应照常解析: %+v", got.Condition)
	}
}

// 「工具」后缺少冒号不算「工具」行（标签必须紧跟冒号）。
func TestParseSkillToolsLabelNeedsColon(t *testing.T) {
	got, err := ParseSkillTools("工具公式说明如下：见附录")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.HasToolLine || len(got.Refs) != 0 {
		t.Fatalf("缺少冒号不得识别为工具行: %+v", got)
	}
}

// 非法引用：连字符工具名、缺少 @、多段 @ 均须报错并指明具体引用。
func TestParseSkillToolsInvalidRefs(t *testing.T) {
	cases := []struct {
		name    string
		content string
		badWord string
	}{
		{"连字符工具名", "工具：formula-hint@campus-tools", "formula-hint@campus-tools"},
		{"缺少 @", "工具：formula_hint", "formula_hint"},
		{"多段 @", "工具：formula_hint@campus-tools@extra", "formula_hint@campus-tools@extra"},
		{"大写工具名", "工具：Formula_hint@campus-tools", "Formula_hint@campus-tools"},
		{"服务器名非法", "工具：formula_hint@CampusTools", "formula_hint@CampusTools"},
	}
	for _, tc := range cases {
		got, err := ParseSkillTools(tc.content)
		if err == nil {
			t.Fatalf("%s 应报错，得到 %+v", tc.name, got)
		}
		if !strings.Contains(err.Error(), tc.badWord) {
			t.Fatalf("%s 错误信息须指明具体引用，得到 %q", tc.name, err.Error())
		}
	}
}

// 多条非法引用一次性全部报出。
func TestParseSkillToolsMultipleInvalid(t *testing.T) {
	_, err := ParseSkillTools("工具：formula_hint, bad-name@campus-tools")
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "formula_hint") || !strings.Contains(err.Error(), "bad-name@campus-tools") {
		t.Fatalf("应同时报出两条非法引用，得到 %q", err.Error())
	}
}

// 名称语法判定：工具名不允许连字符；服务器名允许连字符。
func TestToolAndServerNameValidation(t *testing.T) {
	if !ValidToolName("formula_hint") || ValidToolName("formula-hint") || ValidToolName("1abc") {
		t.Fatal("工具名语法判定错误")
	}
	if !ValidMCPServerName("campus-tools") || !ValidMCPServerName("campus_tools") ||
		ValidMCPServerName("Campus") {
		t.Fatal("服务器名语法判定错误")
	}
}
