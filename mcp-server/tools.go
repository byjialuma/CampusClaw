package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// toolDefinition 是 tools/list 中单个工具的对外描述。
type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolDefinition `json:"tools"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolDefinitions 返回全部暴露的工具，两个工具均只有一个必填字符串参数 query。
func toolDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "formula_hint",
			Description: "按 query 识别数学公式主题，返回公式与分步解题提示。可识别主题词：一元二次/quadratic、面积/area、勾股/直角三角形/pythagorean、圆/circle。",
			InputSchema: querySchema("要识别的公式主题文本，例如“一元二次方程的求根公式”。"),
		},
		{
			Name:        "unit_convert",
			Description: "按 query 解析“数值 + 单位”的换算请求，返回换算结果。支持长度（千米/km、米/m、厘米/cm）、质量（千克/kg、克/g）、时间（小时/h、分钟/min、秒/s）。",
			InputSchema: querySchema("要换算的文本，例如“5 千米是多少米”或“convert 5 km to m”。"),
		},
	}
}

// querySchema 是工具统一入参 schema：单个必填字符串 query。
func querySchema(desc string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": desc,
			},
		},
		"required": []string{"query"},
	}
}

// runTool 按工具名执行；known 为 false 表示工具名不存在。
func runTool(name, query string) (text string, known bool) {
	switch name {
	case "formula_hint":
		return formulaHint(query), true
	case "unit_convert":
		return unitConvert(query), true
	default:
		return "未知工具：" + name, false
	}
}

// matchAny 判断 s 是否包含任一子串。
func matchAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// formulaHint 依据主题词确定性地给出公式与分步提示；无法识别时返回用法说明。
func formulaHint(query string) string {
	q := strings.ToLower(query)
	switch {
	case matchAny(q, "一元二次", "二次方程", "quadratic"):
		return textQuadratic
	case matchAny(q, "勾股", "直角三角形", "pythagorean", "right triangle"):
		return textPythagorean
	case matchAny(q, "面积", "area"):
		return textArea
	case matchAny(q, "圆", "circle"):
		return textCircle
	default:
		return textFormulaUsage
	}
}

const textQuadratic = `一元二次方程 ax^2 + bx + c = 0（a≠0）的求根公式：
x = (-b ± √(b^2 - 4ac)) / (2a)

分步提示：
1. 化为标准形式 ax^2 + bx + c = 0，确认 a≠0。
2. 计算判别式 Δ = b^2 - 4ac。
3. 判断根的情况：Δ>0 有两个不等实根；Δ=0 有一个重根；Δ<0 无实根。
4. 代入求根公式 x = (-b ± √Δ) / (2a) 求出各根。`

const textPythagorean = `勾股定理（直角三角形）：a^2 + b^2 = c^2，其中 c 为斜边，a、b 为两条直角边。

分步提示：
1. 确认三角形为直角三角形，并标出直角与斜边 c。
2. 列出已知边，选择求未知边的变形：c = √(a^2 + b^2) 或 a = √(c^2 - b^2)。
3. 代入已知数值计算平方和或平方差，再开平方。
4. 检查结果是否符合三角形三边关系（斜边最长）。`

const textArea = `常见平面图形面积公式：
- 矩形：S = 长 × 宽
- 三角形：S = 底 × 高 ÷ 2
- 平行四边形：S = 底 × 高
- 梯形：S = (上底 + 下底) × 高 ÷ 2
- 圆：S = πr^2

分步提示：
1. 判断图形类型，选择对应的面积公式。
2. 明确公式中各量含义（底、高、半径等）。
3. 代入已知数值，注意单位统一。
4. 计算并写出结果，面积使用平方单位。`

const textCircle = `圆的相关公式（半径 r，直径 d = 2r）：
- 周长：C = 2πr = πd
- 面积：S = πr^2

分步提示：
1. 明确已知量是半径还是直径，必要时先换算。
2. 求周长用 C = 2πr；求面积用 S = πr^2。
3. 代入数值，π 通常取 3.14 或保留 π。
4. 计算并注明单位（长度单位或平方单位）。`

const textFormulaUsage = `用法：在 query 中写清要查询的公式主题，例如：
- "一元二次方程的求根公式" 或 "quadratic formula"
- "三角形面积怎么算" 或 "area of triangle"
- "勾股定理" 或 "pythagorean theorem"
- "圆的周长和面积" 或 "circle formula"
可识别的主题词：一元二次/quadratic、面积/area、勾股/直角三角形/pythagorean、圆/circle。`

// unitSpec 描述一个可换算单位的别名、类别与相对基准单位的换算系数。
type unitSpec struct {
	key      string
	name     string
	abbr     string
	category string
	factor   float64
	aliases  []string
}

var unitSpecs = []unitSpec{
	{key: "km", name: "千米", abbr: "km", category: "length", factor: 1000, aliases: []string{"千米", "公里", "km"}},
	{key: "m", name: "米", abbr: "m", category: "length", factor: 1, aliases: []string{"米", "m"}},
	{key: "cm", name: "厘米", abbr: "cm", category: "length", factor: 0.01, aliases: []string{"厘米", "cm"}},
	{key: "kg", name: "千克", abbr: "kg", category: "mass", factor: 1000, aliases: []string{"千克", "公斤", "kg"}},
	{key: "g", name: "克", abbr: "g", category: "mass", factor: 1, aliases: []string{"克", "g"}},
	{key: "h", name: "小时", abbr: "h", category: "time", factor: 3600, aliases: []string{"小时", "h"}},
	{key: "min", name: "分钟", abbr: "min", category: "time", factor: 60, aliases: []string{"分钟", "min"}},
	{key: "s", name: "秒", abbr: "s", category: "time", factor: 1, aliases: []string{"秒", "s"}},
}

var numberRe = regexp.MustCompile(`\d+(?:\.\d+)?`)

// unitMatch 记录一次单位命中及其在原文本中的字节位置。
type unitMatch struct {
	spec unitSpec
	pos  int
}

const textUnitUsage = `用法：在 query 中写清"数值 + 原单位 + 目标单位"，例如：
- "5 千米是多少米"
- "convert 5 km to m"
- "把 2000 克换算成千克"
支持的单位：
- 长度：千米/km、米/m、厘米/cm
- 质量：千克/kg、克/g
- 时间：小时/h、分钟/min、秒/s
注意：仅支持同一类别单位之间的换算。`

// unitConvert 解析"数值 + 单位"换算请求；无法识别或跨类别时返回用法说明。
func unitConvert(query string) string {
	text := strings.ToLower(query)

	loc := numberRe.FindStringIndex(text)
	if loc == nil {
		return textUnitUsage
	}
	value, err := strconv.ParseFloat(text[loc[0]:loc[1]], 64)
	if err != nil {
		return textUnitUsage
	}

	units := scanUnits(text)
	var from, to *unitMatch
	for i := range units {
		if units[i].pos < loc[1] {
			continue
		}
		if from == nil {
			from = &units[i]
			continue
		}
		to = &units[i]
		break
	}
	if from == nil || to == nil || from.spec.category != to.spec.category {
		return textUnitUsage
	}

	result := value * from.spec.factor / to.spec.factor
	return fmt.Sprintf("换算结果：%s %s(%s) = %s %s(%s)",
		formatNumber(value), from.spec.name, from.spec.abbr,
		formatNumber(result), to.spec.name, to.spec.abbr)
}

// scanUnits 从左到右扫描文本，优先匹配最长别名；拉丁别名要求词边界，
// 避免 km 中的 m、千克中的克被误拆。
func scanUnits(text string) []unitMatch {
	var matches []unitMatch
	for i := 0; i < len(text); {
		var best *unitSpec
		bestLen := 0
		for idx := range unitSpecs {
			spec := &unitSpecs[idx]
			for _, alias := range spec.aliases {
				if !strings.HasPrefix(text[i:], alias) {
					continue
				}
				if isLatinAlias(alias) && !latinBoundary(text, i, len(alias)) {
					continue
				}
				if len(alias) > bestLen {
					bestLen = len(alias)
					best = spec
				}
			}
		}
		if best != nil {
			matches = append(matches, unitMatch{spec: *best, pos: i})
			i += bestLen
			continue
		}
		i++
	}
	return matches
}

// isLatinAlias 判断别名是否全为 ASCII 字符（需词边界约束）。
func isLatinAlias(alias string) bool {
	for i := 0; i < len(alias); i++ {
		if alias[i] >= 0x80 {
			return false
		}
	}
	return true
}

// latinBoundary 判断位置 i、长度 n 的拉丁别名两侧都不是字母（数字/空格/标点均可）。
func latinBoundary(text string, i, n int) bool {
	if i > 0 && isLatinLetter(text[i-1]) {
		return false
	}
	j := i + n
	if j < len(text) && isLatinLetter(text[j]) {
		return false
	}
	return true
}

func isLatinLetter(b byte) bool {
	return b >= 'a' && b <= 'z'
}

// formatNumber 以最简形式输出浮点数，避免多余小数位。
func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
