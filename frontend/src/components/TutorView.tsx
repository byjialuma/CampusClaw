import { FormEvent, useState } from 'react'
import type { Citation, TutorConversation, TutorMessage } from '../types'

interface Props {
  conversation: TutorConversation | null
  messages: TutorMessage[]
  streaming: boolean
  streamCitations: Citation[]
  streamAnswer: string
  streamError: string
  onAsk: (question: string) => void
  onOpenCitation: (c: Citation) => void
}

// uniqueByMaterial 按材料去重：documentId 为键（缺失时退化为文件名），
// 同一材料只保留首个命中（检索结果按相关度降序，首条最相关）。
function uniqueByMaterial(citations: Citation[]): Citation[] {
  const seen = new Set<string>()
  const out: Citation[] = []
  for (const c of citations) {
    const key = c.documentId ? `id:${c.documentId}` : `name:${c.fileName}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(c)
  }
  return out
}

// CitationCards 引用区：每份材料一张紧凑小型跳转卡片，卡片上仅显示文件名；
// 位置/片段/相关度均不展示，点击后用载荷中的 locator 定位到首个命中位置。
function CitationCards({
  citations,
  onOpenCitation,
}: {
  citations: Citation[]
  onOpenCitation: (c: Citation) => void
}) {
  const uniq = uniqueByMaterial(citations)
  if (uniq.length === 0) return null
  return (
    <ul className="tutor-citation-chips">
      {uniq.map((c) => (
        <li key={c.documentId ? c.documentId : c.fileName}>
          <button
            type="button"
            className="tutor-citation-chip"
            title={`在《${c.fileName}》中打开原文`}
            onClick={() => onOpenCitation(c)}
          >
            {c.fileName}
          </button>
        </li>
      ))}
    </ul>
  )
}

// TutorView 对话主区：欢迎语 → 消息流 → 输入框。
// 不再内嵌设置/技能管理标签页——标签页由外层 HomePage 驱动。
export default function TutorView({
  conversation,
  messages,
  streaming,
  streamCitations,
  streamAnswer,
  streamError,
  onAsk,
  onOpenCitation,
}: Props) {
  const [draft, setDraft] = useState('')

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const q = draft.trim()
    if (!q || streaming) return
    setDraft('')
    onAsk(q)
  }

  // 正在流式生成、或已收到 meta/部分文本但连接尚未收尾时，展示活动气泡。
  const showLiveTurn =
    streaming || streamCitations.length > 0 || streamAnswer.length > 0 || streamError.length > 0

  // 无活跃会话时显示欢迎语；有会话但无消息时不显示欢迎语，保持简洁。
  const showWelcome = !conversation

  return (
    <section className="card preview-card tutor-view">
      {showWelcome ? (
        <div className="tutor-welcome">
          <h2 className="tutor-welcome-title">解题助手，我帮你</h2>
          <p className="muted">在下方输入你的第一个问题，开始一段新的解题会话。</p>
          <form className="tutor-input-row tutor-welcome-input" onSubmit={onSubmit}>
            <input
              className="search-input tutor-input"
              type="text"
              maxLength={500}
              value={draft}
              disabled={streaming}
              placeholder={streaming ? '助手正在生成回答…' : '输入你的问题，回车发送'}
              aria-label="向解题助手提问"
              onChange={(e) => setDraft(e.target.value)}
            />
            <button className="btn btn-primary" type="submit" disabled={streaming || !draft.trim()}>
              {streaming ? '思考中…' : '发送'}
            </button>
          </form>
        </div>
      ) : (
        <>
          <div className="tutor-messages">
            {messages.length === 0 && !showLiveTurn && (
              <p className="muted tutor-empty-hint">会话已就绪，在下方输入你的问题。</p>
            )}

            {messages.map((m) => (
              <div key={m.id} className={`tutor-message tutor-message-${m.role}`}>
                <div className="tutor-bubble">
                  <p className="tutor-text">{m.content}</p>
                </div>
                <CitationCards citations={m.citations ?? []} onOpenCitation={onOpenCitation} />
              </div>
            ))}

            {showLiveTurn && (
              <div className="tutor-message tutor-message-assistant">
                {/* meta 先到：引用卡片在正文之前渲染 */}
                <CitationCards citations={streamCitations} onOpenCitation={onOpenCitation} />
                <div className="tutor-bubble">
                  {streamAnswer ? (
                    <p className="tutor-text">{streamAnswer}</p>
                  ) : (
                    !streamError && <p className="tutor-text tutor-placeholder">正在检索本班资料…</p>
                  )}
                  {streaming && <span className="tutor-cursor" aria-hidden />}
                </div>
                {streamError && <div className="alert alert-error tutor-stream-error">{streamError}</div>}
              </div>
            )}
          </div>

          <form className="tutor-input-row" onSubmit={onSubmit}>
            <input
              className="search-input tutor-input"
              type="text"
              maxLength={500}
              value={draft}
              disabled={streaming}
              placeholder={streaming ? '助手正在生成回答…' : '输入追问，回车发送'}
              aria-label="向解题助手追问"
              onChange={(e) => setDraft(e.target.value)}
            />
            <button className="btn btn-primary" type="submit" disabled={streaming || !draft.trim()}>
              {streaming ? '思考中…' : '发送'}
            </button>
          </form>
        </>
      )}
    </section>
  )
}
