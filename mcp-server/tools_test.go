package main

import (
	"regexp"
	"strings"
	"testing"
)

// toolNamePattern 是主机侧引用工具名的合法规则。
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestToolNamesValidAndComplete(t *testing.T) {
	defs := toolDefinitions()
	if len(defs) != 2 {
		t.Fatalf("工具数量 = %d, 期望 2", len(defs))
	}
	want := map[string]bool{"formula_hint": false, "unit_convert": false}
	for _, def := range defs {
		if !toolNamePattern.MatchString(def.Name) {
			t.Errorf("工具名 %q 不匹配 %s", def.Name, toolNamePattern.String())
		}
		if _, ok := want[def.Name]; !ok {
			t.Errorf("出现未预期工具名 %q", def.Name)
			continue
		}
		want[def.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("缺少工具 %q", name)
		}
	}
}

func TestRunToolUnknown(t *testing.T) {
	if _, known := runTool("no_such_tool", "x"); known {
		t.Errorf("未知工具应返回 known=false")
	}
}

func TestFormulaHintThemes(t *testing.T) {
	cases := []struct {
		query    string
		contains string
	}{
		{"一元二次方程的求根公式", "ax^2"},
		{"quadratic formula", "ax^2"},
		{"勾股定理", "勾股定理"},
		{"pythagorean theorem", "勾股定理"},
		{"三角形面积怎么算", "面积公式"},
		{"area of triangle", "面积公式"},
		{"圆的周长公式", "C = 2πr"},
		{"circle formula", "C = 2πr"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := formulaHint(tc.query)
			if got == textFormulaUsage {
				t.Fatalf("命中主题词却返回用法说明: %q", tc.query)
			}
			if !strings.Contains(got, tc.contains) {
				t.Errorf("结果未包含 %q:\n%s", tc.contains, got)
			}
		})
	}
}

func TestFormulaHintMiss(t *testing.T) {
	got := formulaHint("请介绍一下常见的几何图形")
	if got != textFormulaUsage {
		t.Errorf("未命中主题词应返回用法说明，实际:\n%s", got)
	}
	if !strings.Contains(got, "用法") {
		t.Errorf("用法说明应包含“用法”提示:\n%s", got)
	}
}

func TestUnitConvertHits(t *testing.T) {
	cases := []struct {
		query    string
		contains string
	}{
		{"5 千米是多少米", "5000"},
		{"convert 5 km to m", "5000"},
		{"把 2000 克换算成千克", "2 千克"},
		{"3600 秒是多少小时", "1 小时"},
		{"2 小时是多少分钟", "120 分钟"},
		{"5 米是多少厘米", "500 厘米"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := unitConvert(tc.query)
			if got == textUnitUsage {
				t.Fatalf("命中换算请求却返回用法说明: %q", tc.query)
			}
			if !strings.Contains(got, tc.contains) {
				t.Errorf("结果未包含 %q:\n%s", tc.contains, got)
			}
		})
	}
}

func TestUnitConvertMiss(t *testing.T) {
	cases := []string{
		"你好世界",        // 无数字
		"5 apples",   // 有数字无单位
		"5 kg to km", // 跨类别
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			got := unitConvert(q)
			if got != textUnitUsage {
				t.Errorf("无法识别应以用法说明返回，实际:\n%s", got)
			}
		})
	}
}
