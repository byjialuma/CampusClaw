import type { Locator, SearchResult } from '../types'

interface Props {
  query: string
  results: SearchResult[]
  searching: boolean
  error: string
  onOpenSource: (result: SearchResult) => void
  onBack: () => void
}

// locatorLabel 生成徽标文案：PDF 页码 / Markdown 章节 / TXT 行号。
function locatorLabel(loc: Locator): string {
  switch (loc.kind) {
    case 'page':
      return loc.page ? `第 ${loc.page} 页` : '页码未知'
    case 'heading':
      return loc.path || '文档开头'
    case 'lines':
      return loc.startLine === loc.endLine
        ? `第 ${loc.startLine} 行`
        : `第 ${loc.startLine}–${loc.endLine} 行`
    default:
      return '原文位置'
  }
}

function locatorTitle(r: SearchResult): string {
  if (r.fileType === 'pdf') return `在《${r.fileName}》中打开（引用位置：${locatorLabel(r.locator)}）`
  return `在《${r.fileName}》中打开该片段`
}

export default function SearchResults({ query, results, searching, error, onOpenSource, onBack }: Props) {
  return (
    <section className="card preview-card search-results">
      <div className="search-results-head">
        <h2 className="card-title">检索结果</h2>
        <button type="button" className="btn btn-small" onClick={onBack}>
          返回材料预览
        </button>
      </div>

      {error ? (
        <div className="alert alert-error search-error">{error}</div>
      ) : searching ? (
        <p className="muted">正在检索「{query}」…</p>
      ) : results.length === 0 ? (
        <div className="search-empty">
          <p className="search-empty-title">未找到与「{query}」相关的内容</p>
          <p className="muted">可换个关键词重试，或等待教师上传新材料并完成索引。</p>
        </div>
      ) : (
        <>
          <p className="muted search-summary">共 {results.length} 条来自本班材料的命中</p>
          <ul className="result-list">
            {results.map((r) => (
              <li key={r.chunkId} className="result-item">
                <div className="result-head">
                  <span className={`file-icon file-${r.fileType}`}>{r.fileType.toUpperCase()}</span>
                  <span className="result-file" title={r.fileName}>
                    {r.fileName}
                  </span>
                  <span className="result-score">相关度 {(r.score * 100).toFixed(0)}%</span>
                </div>
                <p className="result-snippet">{r.snippet}</p>
                <div className="result-foot">
                  <button
                    type="button"
                    className="locator-badge"
                    title={locatorTitle(r)}
                    onClick={() => onOpenSource(r)}
                  >
                    {locatorLabel(r.locator)}
                  </button>
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  )
}
