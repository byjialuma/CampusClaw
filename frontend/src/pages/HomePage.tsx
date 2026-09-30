import { useEffect, useState } from 'react'
import { askQuestion, createTicket, fetchMaterials, fetchMe, getToken, searchMaterials } from '../api'
import type { Citation, Locator, Material, Me, QAResult, SearchResult } from '../types'
import TopBar from '../components/TopBar'
import UploadPanel from '../components/UploadPanel'
import SearchPanel from '../components/SearchPanel'
import QAPanel from '../components/QAPanel'
import SearchResults from '../components/SearchResults'
import QAResults from '../components/QAResults'
import MaterialList from '../components/MaterialList'
import Preview from '../components/Preview'

export default function HomePage() {
  const [me, setMe] = useState<Me | null>(null)
  const [materials, setMaterials] = useState<Material[]>([])
  const [selected, setSelected] = useState<Material | null>(null)
  const [loading, setLoading] = useState(true)
  const [downloadError, setDownloadError] = useState('')

  // 右栏在「材料预览」「检索结果」「问答结果」三个视图间切换。
  const [view, setView] = useState<'preview' | 'results' | 'qa'>('preview')
  const [searching, setSearching] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<SearchResult[]>([])
  const [searchError, setSearchError] = useState('')
  const [locatorHint, setLocatorHint] = useState<Locator | null>(null)

  // 问答状态
  const [qaLoading, setQaLoading] = useState(false)
  const [qaQuestion, setQaQuestion] = useState('')
  const [qaAnswer, setQaAnswer] = useState('')
  const [qaCitations, setQaCitations] = useState<Citation[]>([])
  const [qaError, setQaError] = useState('')

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      if (!getToken()) {
        window.location.replace('/login?next=' + encodeURIComponent('/'))
        return
      }
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

  async function handleSearch(query: string) {
    setSearchQuery(query)
    setSearching(true)
    setSearchError('')
    setSearchResults([])
    setView('results')
    try {
      setSearchResults(await searchMaterials(query))
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : '知识库检索暂不可用')
    } finally {
      setSearching(false)
    }
  }

  async function handleAsk(question: string) {
    setQaQuestion(question)
    setQaLoading(true)
    setQaError('')
    setQaAnswer('')
    setQaCitations([])
    setView('qa')
    try {
      const result: QAResult = await askQuestion(question)
      setQaAnswer(result.answer)
      setQaCitations(result.citations)
    } catch (err) {
      setQaError(err instanceof Error ? err.message : '知识库问答暂不可用')
    } finally {
      setQaLoading(false)
    }
  }

  function handleViewMaterial(m: Material) {
    setLocatorHint(null)
    setSelected(m)
    setView('preview')
  }

  // 点击检索结果徽标：只能在本班材料列表内定位；找不到对应材料时不发请求、不报错。
  function handleOpenSource(r: SearchResult) {
    const target = materials.find((m) => m.id === r.documentId)
    if (!target) return
    setSelected(target)
    setLocatorHint(r.locator)
    setView('preview')
  }

  // 点击问答引用徽标：打开对应材料预览。
  function handleOpenCitation(c: Citation) {
    const target = materials.find((m) => m.id === c.documentId)
    if (!target) return
    setSelected(target)
    setLocatorHint(c.locator)
    setView('preview')
  }

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
          <SearchPanel searching={searching} onSearch={handleSearch} />
          <QAPanel loading={qaLoading} onAsk={handleAsk} />
          <UploadPanel onUploaded={(m) => setMaterials((prev) => [m, ...prev])} />
          {downloadError && <div className="alert alert-error">{downloadError}</div>}
          <MaterialList
            materials={materials}
            selectedId={selected?.id ?? null}
            loading={loading}
            onView={handleViewMaterial}
            onDownload={handleDownload}
          />
        </aside>
        <section className="right-pane">
          {view === 'results' ? (
            <SearchResults
              query={searchQuery}
              results={searchResults}
              searching={searching}
              error={searchError}
              onOpenSource={handleOpenSource}
              onBack={() => setView('preview')}
            />
          ) : view === 'qa' ? (
            <QAResults
              question={qaQuestion}
              answer={qaAnswer}
              citations={qaCitations}
              loading={qaLoading}
              error={qaError}
              onOpenSource={handleOpenCitation}
              onBack={() => setView('preview')}
            />
          ) : (
            <Preview
              material={selected}
              locatorHint={locatorHint}
              onClearHint={() => setLocatorHint(null)}
            />
          )}
        </section>
      </main>
    </div>
  )
}
