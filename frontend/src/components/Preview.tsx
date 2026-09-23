import { useEffect, useState } from 'react'
import { marked } from 'marked'
import { fetchContent } from '../api'
import type { Locator, Material } from '../types'

interface Props {
  material: Material | null
  // 从检索结果跳转时携带的溯源位置提示（PDF 无法自动翻页，以横幅提示页码）。
  locatorHint?: Locator | null
  onClearHint?: () => void
}

function hintText(loc: Locator): string {
  switch (loc.kind) {
    case 'page':
      return `引用位置：第 ${loc.page ?? '?'} 页，请对照 PDF 页码查看`
    case 'heading':
      return `引用章节：${loc.path || '文档开头'}`
    case 'lines':
      return loc.startLine === loc.endLine
        ? `引用位置：第 ${loc.startLine} 行`
        : `引用位置：第 ${loc.startLine}–${loc.endLine} 行`
    default:
      return ''
  }
}

export default function Preview({ material, locatorHint = null, onClearHint }: Props) {
  const [html, setHtml] = useState('')
  const [pdfUrl, setPdfUrl] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    let url = ''
    let cancelled = false
    setHtml('')
    setPdfUrl('')
    setError('')
    if (!material) return

    setLoading(true)
    fetchContent(material)
      .then(async (blob) => {
        if (cancelled) return
        if (material.fileType === 'pdf') {
          url = URL.createObjectURL(blob.slice(0, blob.size, 'application/pdf'))
          setPdfUrl(url)
        } else {
          const text = await blob.text()
          if (material.fileType === 'md') {
            // 先转义尖括号，关闭原始 HTML 透传面，再做 Markdown 渲染。
            const escaped = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            setHtml(await marked.parse(escaped, { async: true }))
          } else {
            setHtml(`<pre class="plain-text">${escapeHtml(text)}</pre>`)
          }
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : '加载内容失败')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
      if (url) URL.revokeObjectURL(url)
    }
  }, [material])

  if (!material) {
    return (
      <section className="card preview-card preview-empty">
        <div className="preview-placeholder">
          <span className="preview-logo">CC</span>
          <p>在左侧选择一份材料，即可在此查看内容</p>
        </div>
      </section>
    )
  }

  return (
    <section className="card preview-card">
      <h2 className="card-title" title={material.name}>
        {material.name}
      </h2>
      {loading && <p className="muted">内容加载中…</p>}
      {error && <div className="alert alert-error">{error}</div>}
      {locatorHint && hintText(locatorHint) && (
        <div className="locator-hint" role="status">
          <span>{hintText(locatorHint)}</span>
          {onClearHint && (
            <button type="button" className="locator-hint-close" onClick={onClearHint} aria-label="清除定位提示">
              ×
            </button>
          )}
        </div>
      )}
      {pdfUrl && <iframe className="pdf-frame" title={material.name} src={pdfUrl} />}
      {html && <div className="markdown-body" dangerouslySetInnerHTML={{ __html: html }} />}
    </section>
  )
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}
