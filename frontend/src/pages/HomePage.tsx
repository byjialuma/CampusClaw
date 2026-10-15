import { useEffect, useRef, useState } from 'react'
import {
  askTutor,
  createConversation,
  createTicket,
  fetchConversations,
  fetchMaterials,
  fetchMe,
  fetchStudentAgents,
  fetchTutorMessages,
  getToken,
} from '../api'
import type {
  Citation,
  Locator,
  Material,
  Me,
  StudentAgent,
  TutorConversation,
  TutorMessage,
} from '../types'
import TopBar from '../components/TopBar'
import MaterialView from '../components/MaterialView'
import AssistantPanel from '../components/AssistantPanel'
import AgentManagement from '../components/AgentManagement'
import SkillManagement from '../components/SkillManagement'
import StudentList from '../components/StudentList'
import TutorView from '../components/TutorView'

// 教师标签页类型。
type TeacherView = 'students' | 'materials' | 'agents' | 'skills'
// 学生标签页类型。
type StudentView = 'tutor' | 'materials'

// pickDefaultAgentId 按「更新时间降序、同刻编号较大者优先」选出默认智能体。
function pickDefaultAgentId(agents: StudentAgent[]): number | null {
  if (agents.length === 0) return null
  const sorted = [...agents].sort((a, b) => {
    const ta = new Date(a.updatedAt).getTime()
    const tb = new Date(b.updatedAt).getTime()
    if (ta !== tb) return tb - ta
    return b.id - a.id
  })
  return sorted[0].id
}

export default function HomePage() {
  const [me, setMe] = useState<Me | null>(null)
  const [materials, setMaterials] = useState<Material[]>([])
  const [selected, setSelected] = useState<Material | null>(null)
  const [loading, setLoading] = useState(true)
  const [downloadError, setDownloadError] = useState('')
  const [locatorHint, setLocatorHint] = useState<Locator | null>(null)

  // 角色 view：教师默认学生名单，学生默认解题助手。
  const [teacherView, setTeacherView] = useState<TeacherView>('students')
  const [studentView, setStudentView] = useState<StudentView>('tutor')

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

  // 学生侧：本班智能体列表与当前选中项（新会话默认绑定它）。
  const [agents, setAgents] = useState<StudentAgent[]>([])
  const [activeAgentId, setActiveAgentId] = useState<number | null>(null)

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
      // 学生默认进入解题助手，预加载会话列表与智能体列表。
      if (!cancelled && u.role === 'student') {
        await loadConversations()
        await loadAgents()
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  // ---------- 解题助手 ----------

  function clearLiveTurn() {
    setStreamAnswer('')
    setStreamCitations([])
    setStreamError('')
  }

  async function loadConversations() {
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

  // 加载本班智能体并按规则选中默认项（教师最近更新者；无智能体则为 null）。
  async function loadAgents() {
    try {
      const list = await fetchStudentAgents()
      setAgents(list)
      setActiveAgentId(pickDefaultAgentId(list))
    } catch (err) {
      setStreamError(err instanceof Error ? err.message : '加载智能体列表失败')
    }
  }

  async function handleNewConversation() {
    if (activeAgentId === null) {
      setStreamError('本班尚未配置智能体，暂时无法提问')
      return
    }
    setCreatingConv(true)
    try {
      const c = await createConversation(activeAgentId)
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
    // 切换既有会话时，左侧智能体选择同步为该会话绑定者（会话内不切换智能体）。
    setActiveAgentId(c.agentId)
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
    // 尚未选择会话时自动创建一个，并绑定当前选中的智能体。
    if (!conv) {
      if (activeAgentId === null) {
        setStreamError('本班尚未配置智能体，暂时无法提问')
        return
      }
      try {
        conv = await createConversation(activeAgentId)
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

  // 从助手消息打开材料预览（学生切到资料库视图并定位）。
  function handleOpenTutorCitation(c: Citation) {
    const target = materials.find((m) => m.id === c.documentId)
    if (!target) return
    setSelected(target)
    setLocatorHint(c.locator)
    setStudentView('materials')
  }

  function handleViewMaterial(m: Material) {
    setLocatorHint(null)
    setSelected(m)
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

  const isTeacher = me.role === 'teacher'

  return (
    <div className="app">
      <TopBar me={me} />
      <main className={`layout ${isTeacher ? 'layout-teacher' : 'layout-student'}`}>
        {isTeacher ? (
          <>
            <nav className="nav-tabs" aria-label="教师导航">
              <button
                type="button"
                className={`nav-tab${teacherView === 'students' ? ' active' : ''}`}
                onClick={() => setTeacherView('students')}
              >
                学生名单
              </button>
              <button
                type="button"
                className={`nav-tab${teacherView === 'materials' ? ' active' : ''}`}
                onClick={() => setTeacherView('materials')}
              >
                本班资料
              </button>
              <button
                type="button"
                className={`nav-tab${teacherView === 'agents' ? ' active' : ''}`}
                onClick={() => setTeacherView('agents')}
              >
                智能体管理
              </button>
              <button
                type="button"
                className={`nav-tab${teacherView === 'skills' ? ' active' : ''}`}
                onClick={() => setTeacherView('skills')}
              >
                技能管理
              </button>
            </nav>
            <section className="right-pane right-pane-teacher">
              {teacherView === 'students' ? (
                <StudentList />
              ) : teacherView === 'materials' ? (
                <MaterialView
                  me={me}
                  materials={materials}
                  selectedId={selected?.id ?? null}
                  loading={loading}
                  selected={selected}
                  locatorHint={locatorHint}
                  downloadError={downloadError}
                  onView={handleViewMaterial}
                  onDownload={handleDownload}
                  onUploaded={(m) => setMaterials((prev) => [m, ...prev])}
                  onClearHint={() => setLocatorHint(null)}
                />
              ) : teacherView === 'agents' ? (
                <AgentManagement />
              ) : (
                <SkillManagement />
              )}
            </section>
          </>
        ) : (
          <>
            <aside className="left-pane nav-sidebar">
              <div className="nav-sidebar-buttons">
                <button
                  type="button"
                  className={`nav-sidebar-btn${studentView === 'tutor' ? ' active' : ''}`}
                  onClick={() => setStudentView('tutor')}
                >
                  解题助手
                </button>
                <button
                  type="button"
                  className={`nav-sidebar-btn${studentView === 'materials' ? ' active' : ''}`}
                  onClick={() => setStudentView('materials')}
                >
                  资料库
                </button>
              </div>
              <div className="nav-sidebar-agents">
                <p className="nav-sidebar-agents-title">本班智能体</p>
                {agents.length === 0 ? (
                  <p className="muted nav-sidebar-empty">本班尚未配置智能体</p>
                ) : (
                  <ul className="agent-select-list">
                    {agents.map((a) => (
                      <li key={a.id}>
                        <button
                          type="button"
                          className={`agent-select-item${a.id === activeAgentId ? ' active' : ''}`}
                          onClick={() => setActiveAgentId(a.id)}
                          title={a.name}
                        >
                          {a.name}
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
              <AssistantPanel
                conversations={conversations}
                activeId={activeConv?.id ?? null}
                loading={convLoading}
                creating={creatingConv}
                onNew={handleNewConversation}
                onSelect={(c) => {
                  handleSelectConversation(c)
                  setStudentView('tutor')
                }}
              />
            </aside>
            <section className="right-pane">
              {studentView === 'tutor' ? (
                <TutorView
                  conversation={activeConv}
                  messages={convMessages}
                  streaming={streaming}
                  streamCitations={streamCitations}
                  streamAnswer={streamAnswer}
                  streamError={streamError}
                  onAsk={handleAskTutor}
                  onOpenCitation={handleOpenTutorCitation}
                  inputDisabled={agents.length === 0}
                />
              ) : (
                <MaterialView
                  me={me}
                  materials={materials}
                  selectedId={selected?.id ?? null}
                  loading={loading}
                  selected={selected}
                  locatorHint={locatorHint}
                  downloadError={downloadError}
                  onView={handleViewMaterial}
                  onDownload={handleDownload}
                  onUploaded={(m) => setMaterials((prev) => [m, ...prev])}
                  onClearHint={() => setLocatorHint(null)}
                />
              )}
            </section>
          </>
        )}
      </main>
    </div>
  )
}
