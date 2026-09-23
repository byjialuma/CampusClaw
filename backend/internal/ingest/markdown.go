package ingest

import (
	"regexp"
	"strings"

	"campusclaw/internal/model"
)

// preamblePath 是第一个标题之前内容的章节名。
const preamblePath = "（文档开头）"

// atxHeading 匹配 ATX 风格标题（1-6 个 # 后需空白）。
var atxHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)\s*#*\s*$`)

// heading 是标题栈中的一层。
type heading struct {
	level int
	title string
}

// chunkMarkdown 按 ATX 标题切章，超长章节在章内按 rune 开窗，窗口继承章节路径。
func chunkMarkdown(data []byte) ([]Chunk, error) {
	lines := strings.Split(normalizeNewlines(string(data)), "\n")

	var chunks []Chunk
	var stack []heading

	// flush 把一段正文按窗口写出并继承当前章节路径。
	flush := func(body []string) {
		if len(body) == 0 || isBlank(strings.Join(body, "\n")) {
			return
		}
		path := currentPath(stack)
		for _, w := range windowRunes(strings.Join(body, "\n"), TargetRunes, OverlapRunes) {
			chunks = append(chunks, Chunk{
				Text:    w,
				Locator: model.Locator{Kind: model.LocatorHeading, Path: path},
			})
		}
	}

	var body []string
	for _, line := range lines {
		if m := atxHeading.FindStringSubmatch(line); m != nil {
			flush(body)
			body = nil

			lvl := len(m[1])
			title := strings.TrimSpace(m[2])
		stackLoop:
			for {
				switch {
				case len(stack) == 0:
					stack = append(stack, heading{level: lvl, title: title})
					break stackLoop
				case stack[len(stack)-1].level < lvl:
					stack = append(stack, heading{level: lvl, title: title})
					break stackLoop
				default:
					stack = stack[:len(stack)-1]
				}
			}
			// 标题行本身计入下一章节正文，保留上下文。
			body = append(body, line)
			continue
		}
		body = append(body, line)
	}
	flush(body)

	return chunks, nil
}

// currentPath 把标题栈渲染为「一级 > 二级」章节路径；无标题时返回文档开头。
func currentPath(stack []heading) string {
	if len(stack) == 0 {
		return preamblePath
	}
	parts := make([]string, len(stack))
	for i, h := range stack {
		parts[i] = h.title
	}
	return strings.Join(parts, " > ")
}
