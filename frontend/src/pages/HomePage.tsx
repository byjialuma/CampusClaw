import { useEffect, useRef, useState } from 'react'
import {
  askQuestion,
  askTutor,
  createConversation,
  createTicket,
  fetchConversations,
  fetchMaterials,
  fetchMe,
  fetchTutorMessages,
  getToken,
  searchMaterials,
} from '../api'
import type {
  Citation,
  Locator,
  Material,
  Me,
  QAResult,
  SearchResult,
  TutorConversation,
  TutorMessage,
} from '../types'
import TopBar from '../components/TopBar'
import UploadPanel from '../components/UploadPanel'
import SearchPanel from '../components/SearchPanel'
import QAPanel from '../components/QAPanel'
import SearchResults from '../components/SearchResults'
import QAResults from '../components/QAResults'
import MaterialList from '../components/MaterialList'
import Preview from '../components/Preview'
import AssistantPanel from '../components/AssistantPanel'
import TutorView from '../components/TutorView'

export default function HomePage() {
  const [me, setMe] = useState<Me | null>(null)
  const [materials, setMaterials] = useState<Material[]>([])
  const [selected, setSelected] = useState<Material | null>(null)
  const [loading, setLoading] = useState(true)
  const [downloadError, setDownloadError] = useState('')

  // 右栏在「材料预览」「检索结果」「问答结果」「解题助手」四个视图间切换。
  const [view, setView] = useState<'preview' | 'results' | 'qa' | 'tutor'>('preview')
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

  // 解题助手状态
  const [conversations, setConversations] = useState<TutorConversation[]>([])
  const [activeConv, setActiveConv] = useState<TutorConversation | null>(null)
  const [convMessages, setConvMessages] = useState<TutorMessage[]>([])
  const [convLoading, setConvLoading] = useState(false)
  const [creatingConv, setCreatingConv] = useState(false)
  const [streaming, setStreaming] = useState(false)
  const [streamAnswer, setStreamAnswer] = useState('')
  const [streamCitations, setStreamCitations] = useState<Citation[]>([])
  const [streamError, setStreamError] = useState('')
  const abortRef = useRef<AbortController | null>(null)

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

  // ---------- 解题助手 ----------

  function clearLiveTurn() {
    setStreamAnswer('')
    setStreamCitations([])
    setStreamError('')
  }

  // 进入助手视图并加载历史会话。
  async function enterTutor() {
    setView('tutor')
    setConvLoading(true)
    setStreamError('')
    try {
      setConversations(await fetchConversations())
    } catch (err) {
      setStreamError(err instanceof Error ? err.message : '加载会话列表失败')
    } finally {
      setConvLoading(false)
    }
  }

  function exitTutor() {
    if (streaming) abortRef.current?.abort()
    setView('preview')
  }

  async function handleNewConversation() {
    setCreatingConv(true)
    try {
      const c = await createConversation()
      setConversations((prev) => [c, ...prev])
      setActiveConv(c)
      setConvMessages([])
      clearLiveTurn()
    } catch (err) {
      setStreamError(err instanceof Error ? err.message : '创建会话失败')
    } finally {
      setCreatingConv(false)
    }
  }

  async function handleSelectConversation(c: TutorConversation) {
    if (streaming) abortRef.current?.abort()
    setActiveConv(c)
    clearLiveTurn()
    setConvLoading(true)
    try {
      setConvMessages(await fetchTutorMessages(c.id))
    } catch (err) {
      setStreamError(err instanceof Error ? err.message : '加载消息失败')
    } finally {
      setConvLoading(false)
    }
  }

  // 一轮结束后从服务端重同步：消息（含可能保存的部分回答）与会话标题都以服务端为准。
  async function resyncConversation(convId: number) {
    try {
      const [msgs, convs] = await Promise.all([
        fetchTutorMessages(convId),
        fetchConversations(),
      ])
      setConvMessages(msgs)
      setConversations(convs)
      setActiveConv((prev) => convs.find((c) => c.id === convId) ?? prev)
    } catch {
      /* 重同步失败不阻断界面 */
    }
  }

  async function handleAskTutor(question: string) {
    let conv = activeConv
    // 尚未选择会话时自动创建一个。
    if (!conv) {
      try {
        conv = await createConversation()
        setConversations((prev) => [conv as TutorConversation, ...prev])
        setActiveConv(conv)
      } catch (err) {
        setStreamError(err instanceof Error ? err.message : '创建会话失败')
        return
      }
    }

    // 先本地插入用户气泡，服务端已在发流前持久化该消息。
    const optimistic: TutorMessage = { id: -Date.now(), role: 'user', content: question }
    setConvMessages((prev) => [...prev, optimistic])
    clearLiveTurn()
    setStreaming(true)

    const controller = new AbortController()
    abortRef.current = controller
    const targetConv = conv

    try {
      await askTutor(
        targetConv.id,
        question,
        (ev) => {
          switch (ev.type) {
            case 'meta':
              setStreamCitations(ev.citations)
              break
            case 'delta':
              setStreamAnswer((prev) => prev + ev.text)
              break
            case 'done':
              break
            case 'error':
              setStreamError(ev.error)
              break
          }
        },
        controller.signal,
      )
    } catch (err) {
      // 用户主动中断由 AbortError 表示，中断后同样重同步（部分回答可能已落库）。
      if (err instanceof Error && err.name !== 'AbortError') {
        setStreamError(err.message || '解题助手暂不可用')
      }
    } finally {
      setStreaming(false)
      abortRef.current = null
      clearLiveTurn()
      await resyncConversation(targetConv.id)
    }
  }

  // 从助手消息打开材料预览（离开助手视图）。
  function handleOpenTutorCitation(c: Citation) {
    const target = materials.find((m) => m.id === c.documentId)
    if (!target) return
    setSelected(target)
    setLocatorHint(c.locator)
    setView('preview')
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
          {view === 'tutor' ? (
            <AssistantPanel
              conversations={conversations}
              activeId={activeConv?.id ?? null}
              loading={convLoading}
              creating={creatingConv}
              onNew={handleNewConversation}
              onSelect={handleSelectConversation}
            />
          ) : (
            <>
              <SearchPanel searching={searching} onSearch={handleSearch} />
              <QAPanel loading={qaLoading} onAsk={handleAsk} />
              <section className="card tutor-entry-card">
                <div className="tutor-entry">
                  <div className="tutor-entry-text">
                    <h2 className="card-title">解题助手</h2>
                    <p className="file-hint">多轮会话提问，流式呈现思路与引用</p>
                  </div>
                  <button className="btn btn-primary btn-small" type="button" onClick={enterTutor}>
                    打开助手
                  </button>
                </div>
              </section>
              <UploadPanel onUploaded={(m) => setMaterials((prev) => [m, ...prev])} />
              {downloadError && <div className="alert alert-error">{downloadError}</div>}
              <MaterialList
                materials={materials}
                selectedId={selected?.id ?? null}
                loading={loading}
                onView={handleViewMaterial}
                onDownload={handleDownload}
              />
            </>
          )}
        </aside>
        <section className="right-pane">
          {view === 'tutor' ? (
            <TutorView
              me={me}
              conversation={activeConv}
              messages={convMessages}
              streaming={streaming}
              streamCitations={streamCitations}
              streamAnswer={streamAnswer}
              streamError={streamError}
              onAsk={handleAskTutor}
              onExit={exitTutor}
              onOpenCitation={handleOpenTutorCitation}
            />
          ) : view === 'results' ? (
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
