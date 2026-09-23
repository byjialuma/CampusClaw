package ingest

import (
	"strings"

	"campusclaw/internal/model"
)

// chunkTXT 按行组织分片，Locator 记录 1 基起止行号；相邻分片保留尾部行重叠。
func chunkTXT(data []byte) ([]Chunk, error) {
	text := normalizeNewlines(string(data))
	lines := strings.Split(text, "\n")
	// 文件末尾换行产生的空行不参与编号。
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var chunks []Chunk
	i := 0
	for i < len(lines) {
		for i < len(lines) && isBlank(lines[i]) {
			i++
		}
		if i >= len(lines) {
			break
		}

		start := i
		size := 0
		for i < len(lines) {
			lr := runeLen(lines[i])
			if size > 0 && size+1+lr > TargetRunes {
				break
			}
			size += 1 + lr
			i++
			if size >= TargetRunes {
				break
			}
		}

		joined := strings.Join(lines[start:i], "\n")
		for _, w := range windowRunes(joined, TargetRunes, OverlapRunes) {
			chunks = append(chunks, Chunk{
				Text:    w,
				Locator: model.Locator{Kind: model.LocatorLines, StartLine: start + 1, EndLine: i},
			})
		}

		// 下一片从当前块尾部选若干行作为重叠（行号区间相应重叠）。
		if i < len(lines) {
			back, used := 0, 0
			for i-1-back > start {
				add := runeLen(lines[i-1-back]) + 1
				if used+add > OverlapRunes {
					break
				}
				used += add
				back++
			}
			i -= back
		}
	}
	return chunks, nil
}
