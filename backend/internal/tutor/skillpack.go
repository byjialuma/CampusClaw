package tutor

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// 技能包限制（设计 D1、D4）。
const (
	// MaxSkillPackageBytes 是压缩包体积上限。
	MaxSkillPackageBytes = 512 * 1024
	// MaxSkillUnpackedBytes 是解压后总大小上限。
	MaxSkillUnpackedBytes = 2 * 1024 * 1024
	// MaxSkillPackageFiles 是解压后文件数上限。
	MaxSkillPackageFiles = 200
	// MaxSkillPackageFileBytes 是单个文件体积上限。
	MaxSkillPackageFileBytes = 512 * 1024
	// SkillManifestName 是技能包根目录的清单文件名。
	SkillManifestName = "SKILL.md"
	// 运行时资源加载上限（设计 D4）。
	SkillResourceMaxFiles       = 5
	SkillResourceFileMaxBytes   = 16 * 1024
	SkillResourceTotalMaxBytes  = 32 * 1024
	skillFrontmatterNameKey     = "name"
	skillFrontmatterDescKey     = "description"
	skillFrontmatterCondKey     = "适用条件"
	skillFrontmatterToolsKey    = "allowed-tools"
	skillFrontmatterResourceKey = "resources"
)

// SkillPackage 是解析后的技能包：frontmatter 元信息 + 正文 + 声明的资源。
type SkillPackage struct {
	Name        string
	Description string
	Conditions  []string
	ToolRefs    []ToolRef
	Resources   []string
	Frontmatter map[string]any
	Body        string
	Raw         []byte // SKILL.md 原始字节（用于内容哈希）
}

// SkillPackageFiles 是一次 zip 解码结果：全部文件（相对路径→内容）与解析结果。
type SkillPackageFiles struct {
	Files   map[string][]byte
	Package *SkillPackage
}

// ParseSkillMD 解析 SKILL.md：可选 YAML frontmatter（仅支持标量、行内数组、块数组）
// 与 Markdown 正文。frontmatter 必须包含 name 与 description。
func ParseSkillMD(raw []byte) (*SkillPackage, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("SKILL.md 必须是 UTF-8 文本")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	fm := map[string]any{}
	bodyStart := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		closeIdx := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				closeIdx = i
				break
			}
		}
		if closeIdx < 0 {
			return nil, fmt.Errorf("frontmatter 缺少结束标记 ---")
		}
		parsed, err := parseFrontmatter(lines[1:closeIdx], 2)
		if err != nil {
			return nil, err
		}
		fm = parsed
		bodyStart = closeIdx + 1
	}

	body := strings.Join(lines[bodyStart:], "\n")
	return buildSkillPackage(fm, body, raw)
}

// buildSkillPackage 校验并归一化 frontmatter，合并正文工具声明与适用条件。
func buildSkillPackage(fm map[string]any, body string, raw []byte) (*SkillPackage, error) {
	pkg := &SkillPackage{Frontmatter: fm, Body: strings.TrimSpace(body), Raw: raw}

	name, ok := fm[skillFrontmatterNameKey].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("frontmatter 缺少必填字段 name")
	}
	desc, ok := fm[skillFrontmatterDescKey].(string)
	if !ok || strings.TrimSpace(desc) == "" {
		return nil, fmt.Errorf("frontmatter 缺少必填字段 description")
	}
	pkg.Name = strings.TrimSpace(name)
	pkg.Description = strings.TrimSpace(desc)

	if v, exists := fm[skillFrontmatterCondKey]; exists {
		conditions, err := asStringList(skillFrontmatterCondKey, v)
		if err != nil {
			return nil, err
		}
		pkg.Conditions = conditions
	}
	if v, exists := fm[skillFrontmatterToolsKey]; exists {
		tools, err := asStringList(skillFrontmatterToolsKey, v)
		if err != nil {
			return nil, err
		}
		for _, item := range tools {
			ref, errMsg := parseToolRef(strings.TrimSpace(item))
			if errMsg != "" {
				return nil, fmt.Errorf("非法工具引用：%s", errMsg)
			}
			pkg.ToolRefs = append(pkg.ToolRefs, ref)
		}
	}
	if v, exists := fm[skillFrontmatterResourceKey]; exists {
		resources, err := asStringList(skillFrontmatterResourceKey, v)
		if err != nil {
			return nil, err
		}
		for _, res := range resources {
			clean, err := normalizeResourcePath(res)
			if err != nil {
				return nil, err
			}
			pkg.Resources = append(pkg.Resources, clean)
		}
	}

	// 正文的「工具：」「适用条件：」行保留为兼容写法：工具合并去重（frontmatter 优先），
	// 适用条件仅在 frontmatter 未声明时兜底。
	bodyParsed, err := ParseSkillTools(pkg.Body)
	if err != nil {
		return nil, err
	}
	pkg.ToolRefs = mergeToolRefs(pkg.ToolRefs, bodyParsed.Refs)
	if len(pkg.Conditions) == 0 && len(bodyParsed.Condition) > 0 {
		pkg.Conditions = bodyParsed.Condition
	}
	return pkg, nil
}

// parseFrontmatter 解析 frontmatter 行；startLine 是首行的实际行号（用于错误定位）。
func parseFrontmatter(lines []string, startLine int) (map[string]any, error) {
	fm := map[string]any{}
	for i := 0; i < len(lines); i++ {
		lineNo := startLine + i
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("frontmatter 第 %d 行语法错误：%s", lineNo, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch {
		case strings.HasPrefix(value, "["):
			if !strings.HasSuffix(value, "]") {
				return nil, fmt.Errorf("frontmatter 第 %d 行数组缺少结束符 ]：%s", lineNo, value)
			}
			items, err := parseInlineList(value)
			if err != nil {
				return nil, fmt.Errorf("frontmatter 第 %d 行数组语法错误：%v", lineNo, err)
			}
			fm[key] = items
		case value == "":
			// 块数组：后续连续「- item」行归入本键。
			items := []string{}
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if !strings.HasPrefix(next, "- ") && next != "-" {
					break
				}
				i++
				item := strings.TrimSpace(strings.TrimPrefix(next, "-"))
				if unquoted := parseScalar(item); unquoted != "" {
					items = append(items, unquoted)
				}
			}
			fm[key] = items
		default:
			fm[key] = parseScalar(value)
		}
	}
	return fm, nil
}

// parseInlineList 解析 [a, b, c]；空数组与带引号元素均支持。
func parseInlineList(value string) ([]string, error) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "["), "]"))
	if inner == "" {
		return []string{}, nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := parseScalar(strings.TrimSpace(part))
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// parseScalar 去掉值两侧的成对引号。
func parseScalar(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return strings.TrimSpace(v[1 : len(v)-1])
		}
	}
	return v
}

// asStringList 把 frontmatter 值归一为字符串切片（标量视为单元素）。
func asStringList(key string, v any) ([]string, error) {
	switch val := v.(type) {
	case string:
		if strings.TrimSpace(val) == "" {
			return []string{}, nil
		}
		return []string{strings.TrimSpace(val)}, nil
	case []string:
		return val, nil
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("frontmatter 字段 %s 必须是字符串数组", key)
			}
			out = append(out, strings.TrimSpace(s))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("frontmatter 字段 %s 必须是字符串或字符串数组", key)
	}
}

// mergeToolRefs 按原始引用去重合并，frontmatter 声明排在前面。
func mergeToolRefs(primary, secondary []ToolRef) []ToolRef {
	seen := map[string]bool{}
	out := make([]ToolRef, 0, len(primary)+len(secondary))
	for _, refs := range [][]ToolRef{primary, secondary} {
		for _, ref := range refs {
			if seen[ref.Raw] {
				continue
			}
			seen[ref.Raw] = true
			out = append(out, ref)
		}
	}
	return out
}

// normalizeResourcePath 校验资源相对路径：必须在 references/、assets/ 或 scripts/ 下，
// 不允许绝对路径、.. 段与反斜杠。
func normalizeResourcePath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", fmt.Errorf("技能资源路径不能为空")
	}
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("技能资源路径不能包含反斜杠：%s", raw)
	}
	clean := path.Clean(raw)
	if strings.HasPrefix(clean, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("技能资源路径必须位于包内：%s", raw)
	}
	seg := strings.SplitN(clean, "/", 2)
	if len(seg) < 2 {
		return "", fmt.Errorf("技能资源必须位于 references/、assets/ 或 scripts/ 下：%s", raw)
	}
	switch seg[0] {
	case "references", "assets", "scripts":
		return clean, nil
	default:
		return "", fmt.Errorf("技能资源必须位于 references/、assets/ 或 scripts/ 下：%s", raw)
	}
}

// DecodeSkillPackageZip 在内存中校验并展开 zip：限制体积与文件数，拒绝绝对路径、
// .. 段与符号链接；要求根目录存在 SKILL.md，且声明的资源文件真实存在。
func DecodeSkillPackageZip(data []byte) (*SkillPackageFiles, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("技能包为空")
	}
	if len(data) > MaxSkillPackageBytes {
		return nil, fmt.Errorf("技能包大小不能超过 512KB")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("技能包不是合法的 zip 文件")
	}
	if len(zr.File) > MaxSkillPackageFiles {
		return nil, fmt.Errorf("技能包文件数不能超过 %d 个", MaxSkillPackageFiles)
	}

	files := map[string][]byte{}
	var total int64
	for _, f := range zr.File {
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("技能包不允许符号链接：%s", f.Name)
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		clean := path.Clean(name)
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") ||
			strings.Contains(clean, "/../") || (len(name) >= 2 && name[1] == ':') {
			return nil, fmt.Errorf("技能包包含非法路径：%s", f.Name)
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		if !f.Mode().IsRegular() && f.Mode() != 0 {
			return nil, fmt.Errorf("技能包包含不支持的文件类型：%s", f.Name)
		}
		if int64(f.UncompressedSize64) > MaxSkillPackageFileBytes {
			return nil, fmt.Errorf("技能包单文件不能超过 512KB：%s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("读取技能包文件失败：%s", f.Name)
		}
		content, err := io.ReadAll(io.LimitReader(rc, MaxSkillPackageFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("读取技能包文件失败：%s", f.Name)
		}
		if int64(len(content)) > MaxSkillPackageFileBytes {
			return nil, fmt.Errorf("技能包单文件不能超过 512KB：%s", f.Name)
		}
		total += int64(len(content))
		if total > MaxSkillUnpackedBytes {
			return nil, fmt.Errorf("技能包解压后总大小不能超过 2MB")
		}
		if !utf8.Valid(content) || bytes.ContainsRune(content, 0) {
			return nil, fmt.Errorf("技能包内文件必须是 UTF-8 文本：%s", f.Name)
		}
		if _, dup := files[clean]; dup {
			return nil, fmt.Errorf("技能包包含重复路径：%s", clean)
		}
		files[clean] = content
	}

	manifest, ok := files[SkillManifestName]
	if !ok {
		return nil, fmt.Errorf("技能包根目录缺少 %s", SkillManifestName)
	}
	pkg, err := ParseSkillMD(manifest)
	if err != nil {
		return nil, err
	}
	for _, res := range pkg.Resources {
		if _, ok := files[res]; !ok {
			return nil, fmt.Errorf("frontmatter 声明的资源不存在：%s", res)
		}
	}
	return &SkillPackageFiles{Files: files, Package: pkg}, nil
}

// SkillSlug 由技能名称的规范化结果生成稳定标识（SHA-256 前 16 位十六进制）。
func SkillSlug(name string) string {
	norm := strings.ToLower(strings.TrimSpace(name))
	norm = strings.Join(strings.Fields(norm), " ")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])[:16]
}

// ContentHash 返回字节内容的 SHA-256 十六进制摘要。
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MarshalSkillMD 合成规范技能包清单（迁移与示例 fixture 使用）。
func MarshalSkillMD(name, description string, conditions, tools, resources []string, body string) []byte {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", quoteYAML(name))
	fmt.Fprintf(&sb, "description: %s\n", quoteYAML(description))
	fmt.Fprintf(&sb, "适用条件: %s\n", inlineYAMLList(conditions))
	fmt.Fprintf(&sb, "allowed-tools: %s\n", inlineYAMLList(tools))
	fmt.Fprintf(&sb, "resources: %s\n", inlineYAMLList(resources))
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(body))
	sb.WriteString("\n")
	return []byte(sb.String())
}

// quoteYAML 对含特殊字符的标量加双引号并转义。
func quoteYAML(v string) string {
	if v == "" || strings.ContainsAny(v, ":#[]{}\"'") || strings.Contains(v, ",") {
		replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", " ")
		return "\"" + replacer.Replace(v) + "\""
	}
	return v
}

// inlineYAMLList 输出 [a, b] 形式数组；元素统一加引号避免逗号歧义。
func inlineYAMLList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, quoteYAML(item))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// FrontmatterJSON 序列化 frontmatter（用于入库）；失败时返回 "{}"。
func FrontmatterJSON(fm map[string]any) string {
	raw, err := json.Marshal(fm)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
