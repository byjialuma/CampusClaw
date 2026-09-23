package ingest

import (
	"strings"
	"testing"

	"campusclaw/internal/model"
)

func TestChunkMarkdown_HeadingPath(t *testing.T) {
	src := "# 第一章 集合\n\n集合是数学基础。\n\n## 第一节 集合的概念\n\n集合由元素构成。\n\n# 第二章 函数\n\n函数描述对应关系。"
	chunks, err := Parse("book.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, c := range chunks {
		if c.Locator.Kind != model.LocatorHeading {
			t.Fatalf("md 分片必须是 heading 定位: %+v", c.Locator)
		}
		paths = append(paths, c.Locator.Path)
	}
	joined := strings.Join(paths, "|")
	if !strings.Contains(joined, "第一章 集合") ||
		!strings.Contains(joined, "第一章 集合 > 第一节 集合的概念") ||
		!strings.Contains(joined, "第二章 函数") {
		t.Fatalf("章节路径不完整: %s", joined)
	}

	// 子章节文本应在对应路径的分片中。
	for _, c := range chunks {
		if c.Locator.Path == "第一章 集合 > 第一节 集合的概念" &&
			!strings.Contains(c.Text, "集合由元素构成") {
			t.Fatalf("子章节正文未继承章节归属: %q", c.Text)
		}
	}
}

func TestChunkMarkdown_NoHeadingsUsesPreamble(t *testing.T) {
	chunks, err := Parse("plain.md", []byte("这是一段没有任何标题的文档内容。"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Locator.Path != preamblePath {
		t.Fatalf("无标题文档应归入 %q: %+v", preamblePath, chunks)
	}
}

func TestChunkMarkdown_LongSectionWindowsSharePath(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# 大章节\n\n")
	for i := 0; i < 30; i++ {
		sb.WriteString(strings.Repeat("段落内容", 60))
		sb.WriteString("\n\n")
	}
	chunks, err := Parse("long.md", []byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("超长章节应开窗为多块: %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Locator.Path != "大章节" {
			t.Fatalf("窗口必须继承章节路径: %q", c.Locator.Path)
		}
	}
}

func TestChunkMarkdown_HeadingLineIncluded(t *testing.T) {
	chunks, err := Parse("h.md", []byte("# 标题甲\n\n正文内容"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chunks[0].Text, "# 标题甲") {
		t.Fatalf("标题行应计入章节正文以保留上下文: %q", chunks[0].Text)
	}
}
