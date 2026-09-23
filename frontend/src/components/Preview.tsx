import { useEffect, useState } from 'react'
import { marked } from 'marked'
import { fetchContent } from '../api'
import type { Material } from '../types'

interface Props {
  material: Material | null
}

export default function Preview({ material }: Props) {
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
