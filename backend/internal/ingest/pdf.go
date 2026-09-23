package ingest

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"

	"campusclaw/internal/model"
)

// chunkPDF 逐页提取文本，Locator 记录 1 基页码；单页超长时在页内开窗且窗口共用页码。
func chunkPDF(data []byte) ([]Chunk, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("解析 PDF 失败: %w", err)
	}

	pageCount := reader.NumPage()
	if pageCount == 0 {
		return nil, errors.New("PDF 没有页面")
	}

	var chunks []Chunk
	for pno := 1; pno <= pageCount; pno++ {
		page := reader.Page(pno)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return nil, fmt.Errorf("提取 PDF 第 %d 页文本失败: %w", pno, err)
		}
		text = strings.TrimSpace(normalizeNewlines(text))
		if text == "" {
			continue
		}
		for _, w := range windowRunes(text, TargetRunes, OverlapRunes) {
			chunks = append(chunks, Chunk{
				Text:    w,
				Locator: model.Locator{Kind: model.LocatorPage, Page: pno},
			})
		}
	}

	if len(chunks) == 0 {
		return nil, errors.New("PDF 中未提取到任何文本（可能是扫描件/纯图片 PDF），暂不支持 OCR")
	}
	return chunks, nil
}
