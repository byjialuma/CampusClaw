import type { Citation, Locator } from '../types'

interface Props {
  question: string
  answer: string
  citations: Citation[]
  loading: boolean
  error: string
  onOpenSource: (citation: Citation) => void
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

function locatorTitle(c: Citation): string {
  return `在《${c.fileName}》中打开（引用位置：${locatorLabel(c.locator)}）`
}

export default function QAResults({ question, answer, citations, loading, error, onOpenSource, onBack }: Props) {
  return (
    <section className="card preview-card search-results">
      <div className="search-results-head">
        <h2 className="card-title">知识问答</h2>
        <button type="button" className="btn btn-small" onClick={onBack}>
          返回材料预览
        </button>
      </div>

      {error ? (
        <div className="alert alert-error search-error">{error}</div>
      ) : loading ? (
        <p className="muted">正在思考「{question}」…</p>
      ) : (
        <div className="qa-body">
          <div className="qa-question">
            <p className="muted">问题</p>
            <p className="qa-question-text">{question}</p>
          </div>
          <div className="qa-answer">
            <p className="muted">回答</p>
            <p className="qa-answer-text">{answer}</p>
          </div>
          {citations.length > 0 && (
            <div className="qa-citations">
              <p className="muted">引用来源</p>
              <ul className="result-list">
                {citations.map((c) => (
                  <li key={c.documentId} className="result-item">
                    <div className="result-head">
                      <span className={`file-icon file-${c.fileType}`}>{c.fileType.toUpperCase()}</span>
                      <span className="result-file" title={c.fileName}>
                        {c.fileName}
                      </span>
                      <span className="result-score">相关度 {(c.score * 100).toFixed(0)}%</span>
                    </div>
                    <p className="result-snippet">{c.snippet}</p>
                    <div className="result-foot">
                      <button
                        type="button"
                        className="locator-badge"
                        title={locatorTitle(c)}
                        onClick={() => onOpenSource(c)}
                      >
                        {locatorLabel(c.locator)}
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </section>
  )
}
