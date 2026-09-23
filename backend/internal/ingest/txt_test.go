package ingest

import (
	"strings"
	"testing"

	"campusclaw/internal/model"
)

func TestChunkTXT_ShortDocumentOneChunk(t *testing.T) {
	chunks, err := Parse("a.txt", []byte("第一行\n第二行\n第三行"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("短文档应为 1 块，实际 %d", len(chunks))
	}
	c := chunks[0]
	if c.Index != 0 {
		t.Fatalf("首块编号应为 0，实际 %d", c.Index)
	}
	if c.Locator.Kind != model.LocatorLines || c.Locator.StartLine != 1 || c.Locator.EndLine != 3 {
		t.Fatalf("行号定位异常: %+v", c.Locator)
	}
}

func TestChunkTXT_BlankLinesAndCRLF(t *testing.T) {
	chunks, err := Parse("a.txt", []byte("\r\n\r\n有效行一\r\n\r\n有效行二\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应折叠为 1 块，实际 %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "有效行一") || !strings.Contains(chunks[0].Text, "有效行二") {
		t.Fatalf("正文丢失: %q", chunks[0].Text)
	}
	if chunks[0].Locator.StartLine != 3 {
		t.Fatalf("起始行应为 3（前两行为空），实际 %d", chunks[0].Locator.StartLine)
	}
}

func TestChunkTXT_LongDocumentOverlapsAndCovers(t *testing.T) {
	var sb strings.Builder
	const lines = 60
	for i := 1; i <= lines; i++ {
		sb.WriteString(strings.Repeat("字", 40))
		sb.WriteString("第")
		sb.WriteString(strings.Repeat("x", 3))
		sb.WriteString("行\n")
	}
	chunks, err := Parse("long.txt", []byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("60 行长文档应产生多块，实际 %d", len(chunks))
	}

	allLines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	for i, c := range chunks {
		if c.Locator.Kind != model.LocatorLines {
			t.Fatalf("块 %d 类型错误", i)
		}
		if c.Locator.StartLine < 1 || c.Locator.EndLine > lines || c.Locator.StartLine > c.Locator.EndLine {
			t.Fatalf("块 %d 行号越界: %+v", i, c.Locator)
		}
		// 块正文必须由其声明行号区间内的行组成。
		covered := strings.Join(allLines[c.Locator.StartLine-1:c.Locator.EndLine], "\n")
		if !strings.Contains(covered, strings.SplitN(c.Text, "\n", 2)[0]) {
			t.Fatalf("块 %d 首行不在声明区间内", i)
		}
		if i > 0 {
			// 相邻块行号区间应重叠（overlap 行）。
			if c.Locator.StartLine > chunks[i-1].Locator.EndLine {
				t.Fatalf("块 %d 与前一块无重叠: %+v", i, c.Locator)
			}
		}
	}
}
