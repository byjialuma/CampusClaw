package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"campusclaw/internal/model"
)

// buildMinimalPDF 手工构造带正确 xref 的多页文本 PDF（Helvetica 标准字体，ASCII 文本）。
// 每个 contentStreams 元素是一页的内容流；空串表示无文本页。
func buildMinimalPDF(t *testing.T, contentStreams []string) []byte {
	t.Helper()
	n := len(contentStreams)
	fontID := 3 + n
	streamStart := 4 + n

	type obj struct {
		id      int
		content string
	}
	var objs []obj
	objs = append(objs, obj{1, "<< /Type /Catalog /Pages 2 0 R >>"})

	var kids []string
	for i := 1; i <= n; i++ {
		pageID := 2 + i
		streamID := streamStart + i - 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		objs = append(objs, obj{pageID, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
				"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			fontID, streamID)})
	}
	objs = append(objs, obj{2, fmt.Sprintf(
		"<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n)})
	objs = append(objs, obj{fontID, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"})
	for i, stream := range contentStreams {
		objs = append(objs, obj{streamStart + i,
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)})
	}
	// 按对象号排序输出。
	byID := map[int]string{}
	for _, o := range objs {
		byID[o.id] = o.content
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := map[int]int{}
	for id := 1; id <= fontID+n; id++ {
		offsets[id] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", id, byID[id]))
	}
	xref := b.Len()
	total := fontID + n + 1
	b.WriteString(fmt.Sprintf("xref\n0 %d\n", total))
	b.WriteString("0000000000 65535 f \n")
	for id := 1; id < total; id++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[id]))
	}
	b.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total, xref))
	return []byte(b.String())
}

func pageText(label string) string {
	return fmt.Sprintf("BT /F1 14 Tf 72 720 Td (%s alpha beta gamma) Tj ET", label)
}

func TestChunkPDF_PageLocator(t *testing.T) {
	data := buildMinimalPDF(t, []string{pageText("PAGEONE"), pageText("PAGETWO")})
	if !strings.HasPrefix(string(data), "%PDF-") {
		t.Fatal("PDF 魔数错误")
	}

	chunks, err := Parse("two.pdf", data)
	if err != nil {
		t.Fatalf("解析双页 PDF 失败: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("双页各一片应为 2 块，实际 %d", len(chunks))
	}
	pages := map[int]bool{}
	for i, c := range chunks {
		if c.Locator.Kind != model.LocatorPage {
			t.Fatalf("PDF 分片必须按页定位: %+v", c.Locator)
		}
		pages[c.Locator.Page] = true
		if c.Index != i {
			t.Fatalf("分片编号不连续: %d", c.Index)
		}
	}
	if !pages[1] || !pages[2] {
		t.Fatalf("页码必须为 1、2: %v", pages)
	}
	if !strings.Contains(strings.ToUpper(chunks[0].Text), "PAGEONE") {
		t.Fatalf("第 1 页文本未提取: %q", chunks[0].Text)
	}
	if !strings.Contains(strings.ToUpper(chunks[1].Text), "PAGETWO") {
		t.Fatalf("第 2 页文本未提取: %q", chunks[1].Text)
	}
}

func TestChunkPDF_NoTextFails(t *testing.T) {
	// 无任何文本操作符的页面（模拟扫描件/纯图片 PDF）必须明确报错。
	blank := "0.5 0.5 0.5 RG 100 100 200 200 re S"
	data := buildMinimalPDF(t, []string{blank, blank})
	if _, err := Parse("scan.pdf", data); err == nil {
		t.Fatal("扫描件式无文本 PDF 必须返回错误")
	}
}

func TestParse_DispatcherGuards(t *testing.T) {
	if _, err := Parse("x.docx", []byte("%PDF-1.4 x")); err == nil {
		t.Fatal("docx 必须拒绝")
	}
	if _, err := Parse("x.txt", nil); err == nil {
		t.Fatal("空文件必须拒绝")
	}
	if _, err := Parse("x.txt", []byte("   \n\t\n")); err == nil {
		t.Fatal("纯空白文本必须拒绝")
	}
}

// 回归：现有种子 B 班 PDF（Node 生成）必须能提取出文本，否则启动回填会把它标 failed。
func TestChunkPDF_SeedBClassExtracts(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "seed", "materials", "《B班·数学第一章·集合讲义》.pdf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("种子 PDF 不存在，跳过: %v", err)
	}
	chunks, err := Parse("《B班·数学第一章·集合讲义》.pdf", data)
	if err != nil {
		t.Fatalf("种子 PDF 无法提取文本，启动回填会失败: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("种子 PDF 提取结果为空")
	}
}
