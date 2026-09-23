import { useEffect, useState } from 'react'
import { createTicket, fetchMaterials, fetchMe } from '../api'
import type { Material, Me } from '../types'
import TopBar from '../components/TopBar'
import UploadPanel from '../components/UploadPanel'
import MaterialList from '../components/MaterialList'
import Preview from '../components/Preview'

export default function HomePage() {
  const [me, setMe] = useState<Me | null>(null)
  const [materials, setMaterials] = useState<Material[]>([])
  const [selected, setSelected] = useState<Material | null>(null)
  const [loading, setLoading] = useState(true)
  const [downloadError, setDownloadError] = useState('')

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      const u = await fetchMe()
      if (!u) {
        window.location.replace('/login?next=' + encodeURIComponent('/'))
        return
      }
      if (cancelled) return
      setMe(u)
      const items = await fetchMaterials()
      if (!cancelled) {
        setMaterials(items)
        setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  async function handleDownload(m: Material) {
    setDownloadError('')
    // 必须在点击手势内同步开窗，否则 await 之后临时激活态过期，弹窗会被拦截。
    const win = window.open('', '_blank')
    if (!win) {
      setDownloadError('浏览器拦截了下载新标签页，请允许本站弹出窗口后重试。')
      return
    }
    try {
      const ticket = await createTicket(m.id)
      win.location.href = `/download?ticket=${encodeURIComponent(ticket)}`
    } catch (err) {
      win.close()
      setDownloadError(err instanceof Error ? err.message : '创建下载链接失败')
    }
  }

  if (!me) {
    return <div className="app-loading">正在校验登录状态…</div>
  }

  return (
    <div className="app">
      <TopBar me={me} />
      <main className="layout">
        <aside className="left-pane">
          <UploadPanel onUploaded={(m) => setMaterials((prev) => [m, ...prev])} />
          {downloadError && <div className="alert alert-error">{downloadError}</div>}
          <MaterialList
            materials={materials}
            selectedId={selected?.id ?? null}
            loading={loading}
            onView={setSelected}
            onDownload={handleDownload}
          />
        </aside>
        <section className="right-pane">
          <Preview material={selected} />
        </section>
      </main>
    </div>
  )
}
