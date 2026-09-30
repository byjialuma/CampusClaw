import type { TutorConversation } from '../types'

interface Props {
  conversations: TutorConversation[]
  activeId: number | null
  loading: boolean
  creating: boolean
  onNew: () => void
  onSelect: (c: TutorConversation) => void
}

// formatTime 把服务端时间渲染为 MM-DD HH:mm。
function formatTime(s: string): string {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// AssistantPanel 是助手视图的左栏：新会话入口 + 历史会话列表。
export default function AssistantPanel({
  conversations,
  activeId,
  loading,
  creating,
  onNew,
  onSelect,
}: Props) {
  return (
    <section className="card assistant-panel">
      <div className="assistant-panel-head">
        <h2 className="card-title">解题助手</h2>
        <button className="btn btn-primary btn-small" type="button" disabled={creating} onClick={onNew}>
          {creating ? '创建中…' : '新会话'}
        </button>
      </div>

      {loading ? (
        <p className="muted">加载历史会话…</p>
      ) : conversations.length === 0 ? (
        <p className="file-hint">还没有会话，点击「新会话」开始提问</p>
      ) : (
        <ul className="conversation-list">
          {conversations.map((c) => (
            <li key={c.id}>
              <button
                type="button"
                className={`conversation-item${c.id === activeId ? ' active' : ''}`}
                onClick={() => onSelect(c)}
                title={c.title}
              >
                <span className="conversation-title">{c.title}</span>
                {c.createdAt && <span className="conversation-time">{formatTime(c.createdAt)}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
