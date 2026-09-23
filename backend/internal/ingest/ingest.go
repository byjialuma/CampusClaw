// Package ingest 负责把材料文件解析为带来源位置的分片（Chunk）。
package ingest

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"campusclaw/internal/model"
)

// 切块参数：约 500 rune 一块，相邻块保留约 80 rune 重叠。
const (
	TargetRunes  = 500
	OverlapRunes = 80
)

// 支持的扩展名（与 store 的上传白名单一致）。
const (
	extTXT = "txt"
	extMD  = "md"
	extPDF = "pdf"
)

// ErrEmptyDocument 表示文件没有任何可索引文本。
var ErrEmptyDocument = errors.New("文档内容为空，无法索引")

// ErrUnsupportedType 表示不支持的文件类型。
var ErrUnsupportedType = errors.New("仅支持 txt、md、pdf 文件")

// Chunk 是文档切分后的一个文本分片。
type Chunk struct {
	Index   int
	Text    string
	Locator model.Locator
}

// Parse 按原始文件名的扩展名分派解析器，返回顺序编号的分片。
func Parse(originalName string, data []byte) ([]Chunk, error) {
	if len(data) == 0 {
		return nil, ErrEmptyDocument
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(originalName), "."))

	var chunks []Chunk
	var err error
	switch ext {
	case extTXT:
		chunks, err = chunkTXT(data)
	case extMD:
		chunks, err = chunkMarkdown(data)
	case extPDF:
		chunks, err = chunkPDF(data)
	default:
		return nil, ErrUnsupportedType
	}
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, ErrEmptyDocument
	}
	for i := range chunks {
		chunks[i].Index = i
	}
	return chunks, nil
}

// windowRunes 把长文本按 rune 切成目标长度、相邻重叠的窗口；空白窗口被丢弃。
func windowRunes(s string, target, overlap int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}
	var windows []string
	start := 0
	for start < len(runes) {
		end := start + target
		if end > len(runes) {
			end = len(runes)
		}
		w := strings.TrimSpace(string(runes[start:end]))
		if w != "" {
			windows = append(windows, w)
		}
		if end >= len(runes) {
			break
		}
		next := end - overlap
		if next <= start {
			next = start + 1 // 必须前进，避免极端参数下死循环
		}
		start = next
	}
	return windows
}

// normalizeNewlines 统一换行并去掉孤立回车。
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
