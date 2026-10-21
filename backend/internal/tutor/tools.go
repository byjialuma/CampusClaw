package tutor

import (
	"fmt"
	"regexp"
	"strings"
)

// builtinToolRef 是内置工具引用：对应既有的本班向量检索，永远被接受且不参与 MCP 校验。
const builtinToolRef = "knowledge_search"

// 引用语法：工具名小写字母开头、仅含小写字母/数字/下划线（无连字符）；
// 服务器名小写字母开头，额外允许连字符。
var (
	toolNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	serverNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// ValidToolName 判断工具名是否符合引用语法。
func ValidToolName(name string) bool { return toolNameRe.MatchString(name) }

// ValidMCPServerName 判断服务器名是否符合引用语法（可作为引用的服务器段）。
func ValidMCPServerName(name string) bool { return serverNameRe.MatchString(name) }

// ToolRef 是技能正文声明的一条工具引用。
type ToolRef struct {
	Raw     string `json:"raw"`              // 原始引用文本（去空白后）
	Name    string `json:"name"`             // 工具名；内置引用即 knowledge_search
	Server  string `json:"server,omitempty"` // 服务器名；内置引用为空
	Builtin bool   `json:"builtin"`          // 是否为内置引用 knowledge_search
}

// SkillTools 是技能正文解析出的工具声明。
type SkillTools struct {
	Refs        []ToolRef // 工具引用（声明顺序，未去重）
	Condition   []string  // 「适用条件」关键词；未声明时为空
	HasToolLine bool      // 是否存在「工具」行
}

// ParseSkillTools 解析技能正文中的「工具」行与可选「适用条件」行。
// 「工具」行以「工具」开头后接冒号，值以 , ， 、 分隔；语法非法的引用返回可读错误。
func ParseSkillTools(content string) (SkillTools, error) {
	var out SkillTools
	var invalid []string

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)

		if value, ok := cutLabel(line, "工具"); ok {
			out.HasToolLine = true
			for _, item := range splitItems(value) {
				ref, errMsg := parseToolRef(item)
				if errMsg != "" {
					invalid = append(invalid, errMsg)
					continue
				}
				out.Refs = append(out.Refs, ref)
			}
			continue
		}

		if value, ok := cutLabel(line, "适用条件"); ok {
			out.Condition = append(out.Condition, splitItems(value)...)
		}
	}

	if len(invalid) > 0 {
		return SkillTools{}, fmt.Errorf("非法工具引用：%s", strings.Join(invalid, "；"))
	}
	return out, nil
}

// parseToolRef 校验单条引用；内置引用直接放行，MCP 引用严格校验两段命名。
func parseToolRef(item string) (ToolRef, string) {
	if item == builtinToolRef {
		return ToolRef{Raw: item, Name: item, Builtin: true}, ""
	}
	name, server, ok := strings.Cut(item, "@")
	if !ok {
		return ToolRef{}, fmt.Sprintf("%s（缺少 @ 分隔的服务器名）", item)
	}
	if strings.Contains(server, "@") {
		return ToolRef{}, fmt.Sprintf("%s（只允许一个 @）", item)
	}
	if !toolNameRe.MatchString(name) {
		return ToolRef{}, fmt.Sprintf("%s（工具名需小写字母开头，仅含小写字母、数字与下划线）", item)
	}
	if !serverNameRe.MatchString(server) {
		return ToolRef{}, fmt.Sprintf("%s（服务器名需小写字母开头，仅含小写字母、数字、下划线与连字符）", item)
	}
	return ToolRef{Raw: item, Name: name, Server: server}, ""
}

// RouteSkill 在候选技能集内做一次词法路由：计分口径为「问题文本中命中的『适用条件』关键词个数」，
// 取分数最高者；同分取 skills.id 较小者；最高分为 0（零命中）时返回 nil。未声明适用条件的技能得 0 分。
// 候选集由调用方按「绑定到当前智能体且已启用」预先收敛。同一输入 MUST 得到同一结果（确定性）。
func RouteSkill(skills []Skill, question string) *Skill {
	var best *Skill
	bestScore := 0
	for i := range skills {
		sk := &skills[i]
		// 技能包以 frontmatter 的适用条件优先；纯文本技能回退正文「适用条件：」行。
		conditions := sk.Conditions
		if len(conditions) == 0 {
			parsed, err := ParseSkillTools(sk.Content)
			if err != nil {
				continue // 正文非法时不参与计分（保存入口已校验）
			}
			conditions = parsed.Condition
		}
		score := countConditionHits(conditions, question)
		if score == 0 {
			continue
		}
		// 取最高分；同分取 id 较小者（候选已按 id 升序，这里用严格大于保持「较小 id 优先」）。
		if best == nil || score > bestScore || (score == bestScore && sk.ID < best.ID) {
			best = sk
			bestScore = score
		}
	}
	return best
}

// countConditionHits 统计适用条件关键词在问题文本中出现的个数；ASCII 关键词做大小写折叠。
func countConditionHits(conditions []string, question string) int {
	if len(conditions) == 0 {
		return 0
	}
	lower := strings.ToLower(question)
	n := 0
	for _, kw := range conditions {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.Contains(question, kw) {
			n++
			continue
		}
		// ASCII 关键词大小写不敏感匹配（中文大小写折叠等同原串）。
		if strings.Contains(lower, strings.ToLower(kw)) {
			n++
		}
	}
	return n
}

// cutLabel 判断行是否以 label 开头并紧跟冒号（半角/全角），返回冒号后的值。
func cutLabel(line, label string) (string, bool) {
	if !strings.HasPrefix(line, label) {
		return "", false
	}
	rest := strings.TrimPrefix(line, label)
	switch {
	case strings.HasPrefix(rest, "："):
		return strings.TrimSpace(strings.TrimPrefix(rest, "：")), true
	case strings.HasPrefix(rest, ":"):
		return strings.TrimSpace(strings.TrimPrefix(rest, ":")), true
	default:
		return "", false
	}
}

// splitItems 按 , ， 、 切分并去除空白；空项（连续分隔符）自然被丢弃。
func splitItems(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == '、'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if item := strings.TrimSpace(f); item != "" {
			out = append(out, item)
		}
	}
	return out
}
