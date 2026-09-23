import type { Material } from '../types'

interface Props {
  materials: Material[]
  selectedId: number | null
  loading: boolean
  onView: (m: Material) => void
  onDownload: (m: Material) => void
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

function formatTime(s: string): string {
  const d = new Date(s)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

export default function MaterialList({ materials, selectedId, loading, onView, onDownload }: Props) {
  return (
    <section className="card list-card">
      <h2 className="card-title">
        本班材料
        <span className="card-count">{materials.length}</span>
      </h2>
      {loading ? (
        <p className="muted">加载中…</p>
      ) : materials.length === 0 ? (
        <p className="muted">本班还没有材料，教师可从上方上传。</p>
      ) : (
        <ul className="material-list">
          {materials.map((m) => (
            <li key={m.id} className={`material-item ${m.id === selectedId ? 'active' : ''}`}>
              <button type="button" className="material-main" onClick={() => onView(m)} title="点击查看内容">
                <span className={`file-icon file-${m.fileType}`}>{m.fileType.toUpperCase()}</span>
                <span className="material-meta">
                  <span className="material-name">{m.name}</span>
                  <span className="material-sub">
                    {formatSize(m.sizeBytes)} · {m.uploaderName} · {formatTime(m.createdAt)}
                  </span>
                </span>
              </button>
              <button type="button" className="btn btn-small" onClick={() => onDownload(m)}>
                下载
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
