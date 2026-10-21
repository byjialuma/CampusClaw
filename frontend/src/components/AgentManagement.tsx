import { FormEvent, useEffect, useState } from 'react'
import {
  connectMCPServer,
  createAgent,
  createMCPServer,
  deleteAgent,
  deleteMCPServer,
  disconnectMCPServer,
  fetchAssistantConfig,
  fetchMCPCalls,
  fetchMCPServers,
  fetchMaterials,
  fetchTeacherAgents,
  replaceAgentMaterials,
  replaceAgentSkills,
  updateAgent,
  updateMCPServer,
} from '../api'
import type {
  MCPCall,
  MCPServer,
  Material,
  TutorAgent,
  TutorSkill,
  TutorSkillSource,
} from '../types'

const AGENT_NAME_MAX = 120
const AGENT_PROMPT_MAX = 2000

// 技能来源徽标文案。
const SOURCE_LABEL: Record<TutorSkillSource, string> = {
  seed: '内置',
  upload: '上传',
  online: '自建',
  package: '技能包',
}

// 工具调用状态文案。
const CALL_STATUS_LABEL: Record<MCPCall['status'], string> = {
  success: '成功',
  denied: '拒绝',
  error: '出错',
}

// 一个智能体的绑定编辑草稿。
interface BindDraft {
  skills: number[]
  materials: number[]
}

// 把 ISO 时间串格式化为本地「YYYY-MM-DD HH:mm」；解析失败时原样返回。
function formatCallTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}`
}

// sameIds 以集合语义比较两组 id（顺序无关）。
function sameIds(a: number[], b: number[]): boolean {
  if (a.length !== b.length) return false
  const set = new Set(a)
  return b.every((x) => set.has(x))
}

// AgentManagement 是教师「智能体管理」标签页：维护智能体（增删改含提示词）、
// 每个智能体的技能/材料绑定，以及从「助手设置」迁移过来的本班 MCP 服务器管理。
// 技能的新建/上传/删除放在「技能管理」标签页；本页只取技能库用于绑定。
export default function AgentManagement() {
  const [agents, setAgents] = useState<TutorAgent[]>([])
  const [skills, setSkills] = useState<TutorSkill[]>([])
  const [materials, setMaterials] = useState<Material[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // 新建智能体表单。
  const [showCreate, setShowCreate] = useState(false)
  const [newName, setNewName] = useState('')
  const [newPrompt, setNewPrompt] = useState('')
  const [creating, setCreating] = useState(false)

  // 编辑中的智能体（名称/提示词）。
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editName, setEditName] = useState('')
  const [editPrompt, setEditPrompt] = useState('')
  const [savingAgent, setSavingAgent] = useState(false)

  // 展开绑定面板的智能体与其草稿。
  const [openBindId, setOpenBindId] = useState<number | null>(null)
  const [bindDrafts, setBindDrafts] = useState<Record<number, BindDraft>>({})
  const [busyBind, setBusyBind] = useState<number | null>(null)

  // MCP 服务器管理区状态（自 AssistantSettings 迁移）。
  const [servers, setServers] = useState<MCPServer[]>([])
  const [calls, setCalls] = useState<MCPCall[]>([])
  const [mcpLoading, setMcpLoading] = useState(true)
  const [mcpBusy, setMcpBusy] = useState<number | null>(null)
  const [showServerForm, setShowServerForm] = useState(false)
  const [editingSrvId, setEditingSrvId] = useState<number | null>(null)
  const [srvName, setSrvName] = useState('')
  const [srvBaseUrl, setSrvBaseUrl] = useState('')
  const [srvToken, setSrvToken] = useState('')
  const [savingServer, setSavingServer] = useState(false)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const [agentList, cfg, mats] = await Promise.all([
          fetchTeacherAgents(),
          fetchAssistantConfig(),
          fetchMaterials(),
        ])
        if (cancelled) return
        setAgents(agentList)
        setSkills(cfg.skills ?? [])
        setMaterials(mats)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : '加载智能体失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    loadMcp()
  }, [])

  // loadMcp 并发拉取本班服务器清单与最近调用记录（两者互不阻塞）。
  async function loadMcp() {
    setMcpLoading(true)
    try {
      const [srvList, callList] = await Promise.all([fetchMCPServers(), fetchMCPCalls()])
      setServers(srvList)
      setCalls(callList)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载 MCP 服务器失败')
    } finally {
      setMcpLoading(false)
    }
  }

  // ---------- 智能体增删改 ----------

  function resetCreate() {
    setShowCreate(false)
    setNewName('')
    setNewPrompt('')
  }

  async function onCreateAgent(e: FormEvent) {
    e.preventDefault()
    const name = newName.trim()
    if (!name || creating) return
    setCreating(true)
    setError('')
    try {
      const ag = await createAgent(name, newPrompt.trim())
      setAgents((prev) => [...prev, ag])
      resetCreate()
    } catch (err) {
      setError(err instanceof Error ? err.message : '新建智能体失败')
    } finally {
      setCreating(false)
    }
  }

  function openEdit(a: TutorAgent) {
    setOpenBindId(null)
    setEditingId(a.id)
    setEditName(a.name)
    setEditPrompt(a.systemPrompt ?? '')
    setError('')
  }

  function cancelEdit() {
    setEditingId(null)
    setEditName('')
    setEditPrompt('')
  }

  async function onSaveAgent() {
    if (editingId === null) return
    const name = editName.trim()
    if (!name || savingAgent) return
    setSavingAgent(true)
    setError('')
    try {
      const ag = await updateAgent(editingId, name, editPrompt.trim())
      setAgents((prev) => prev.map((x) => (x.id === ag.id ? ag : x)))
      cancelEdit()
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存智能体失败')
    } finally {
      setSavingAgent(false)
    }
  }

  async function onDeleteAgent(a: TutorAgent) {
    if (busyBind !== null) return
    if (!window.confirm(`确定删除智能体「${a.name}」？删除后其技能/材料绑定一并解除。`)) {
      return
    }
    setBusyBind(a.id)
    setError('')
    try {
      await deleteAgent(a.id)
      setAgents((prev) => prev.filter((x) => x.id !== a.id))
      if (editingId === a.id) cancelEdit()
      if (openBindId === a.id) setOpenBindId(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除智能体失败')
    } finally {
      setBusyBind(null)
    }
  }

  // ---------- 绑定（技能 / 材料）----------

  function toggleBind(a: TutorAgent) {
    setEditingId(null)
    if (openBindId === a.id) {
      setOpenBindId(null)
      return
    }
    setOpenBindId(a.id)
    setBindDrafts((prev) =>
      prev[a.id]
        ? prev
        : { ...prev, [a.id]: { skills: [...a.skillIds], materials: [...a.handoutIds] } },
    )
    setError('')
  }

  function toggleDraftSkill(agentId: number, skillId: number, on: boolean) {
    setBindDrafts((prev) => {
      const d = prev[agentId]
      if (!d) return prev
      const skills = on ? [...d.skills, skillId] : d.skills.filter((x) => x !== skillId)
      return { ...prev, [agentId]: { ...d, skills } }
    })
  }

  function toggleDraftMaterial(agentId: number, materialId: number, on: boolean) {
    setBindDrafts((prev) => {
      const d = prev[agentId]
      if (!d) return prev
      const ms = on ? [...d.materials, materialId] : d.materials.filter((x) => x !== materialId)
      return { ...prev, [agentId]: { ...d, materials: ms } }
    })
  }

  async function onSaveBindings(a: TutorAgent) {
    const d = bindDrafts[a.id]
    if (!d || busyBind !== null) return
    setBusyBind(a.id)
    setError('')
    try {
      await Promise.all([
        replaceAgentSkills(a.id, d.skills),
        replaceAgentMaterials(a.id, d.materials),
      ])
      setAgents((prev) =>
        prev.map((x) => (x.id === a.id ? { ...x, skillIds: d.skills, handoutIds: d.materials } : x)),
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存绑定失败')
    } finally {
      setBusyBind(null)
    }
  }

  function skillName(id: number): string {
    return skills.find((s) => s.id === id)?.name ?? `#${id}`
  }

  function materialName(id: number): string {
    return materials.find((m) => m.id === id)?.name ?? `#${id}`
  }

  // ---------- MCP 服务器管理 ----------

  function openCreateServer() {
    setEditingSrvId(null)
    setSrvName('')
    setSrvBaseUrl('')
    setSrvToken('')
    setShowServerForm(true)
    setError('')
  }

  function openEditServer(s: MCPServer) {
    setEditingSrvId(s.id)
    setSrvName(s.name)
    setSrvBaseUrl(s.baseUrl)
    setSrvToken('')
    setShowServerForm(false)
    setError('')
  }

  function cancelServerForm() {
    setShowServerForm(false)
    setEditingSrvId(null)
    setSrvName('')
    setSrvBaseUrl('')
    setSrvToken('')
  }

  async function onSaveServer(e: FormEvent) {
    e.preventDefault()
    const name = srvName.trim()
    const baseUrl = srvBaseUrl.trim()
    if (!name || !baseUrl || savingServer) return
    setSavingServer(true)
    setError('')
    try {
      if (editingSrvId !== null) {
        // 令牌留空表示保留原令牌（不下发该字段）。
        const req = srvToken.trim()
          ? { name, baseUrl, authToken: srvToken.trim() }
          : { name, baseUrl }
        const updated = await updateMCPServer(editingSrvId, req)
        setServers((prev) => prev.map((s) => (s.id === editingSrvId ? updated : s)))
      } else {
        const created = await createMCPServer({ name, baseUrl, authToken: srvToken.trim() })
        setServers((prev) => [...prev, created])
      }
      cancelServerForm()
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存 MCP 服务器失败')
    } finally {
      setSavingServer(false)
    }
  }

  async function onToggleConnect(s: MCPServer) {
    if (mcpBusy !== null) return
    setMcpBusy(s.id)
    setError('')
    try {
      const next = s.connected ? await disconnectMCPServer(s.id) : await connectMCPServer(s.id)
      setServers((prev) => prev.map((item) => (item.id === s.id ? next : item)))
    } catch (err) {
      setError(err instanceof Error ? err.message : '操作 MCP 服务器失败')
    } finally {
      setMcpBusy(null)
    }
  }

  async function onDeleteServer(s: MCPServer) {
    if (mcpBusy !== null) return
    if (
      !window.confirm(
        `确定删除 MCP 服务器「${s.name}」？引用其工具的技能将无法再调用（历史调用记录保留服务器名）。`,
      )
    ) {
      return
    }
    setMcpBusy(s.id)
    setError('')
    try {
      await deleteMCPServer(s.id)
      setServers((prev) => prev.filter((item) => item.id !== s.id))
      if (editingSrvId === s.id) cancelServerForm()
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除 MCP 服务器失败')
    } finally {
      setMcpBusy(null)
    }
  }

  if (loading) {
    return <p className="muted">加载智能体…</p>
  }

  return (
    <section className="assistant-settings agent-management">
      <div className="agent-section-head">
        <p className="settings-label">
          智能体列表（每个智能体独立持有提示词与技能/材料绑定；学生新建会话时选择）
        </p>
        <button
          type="button"
          className="btn btn-small"
          disabled={showCreate}
          onClick={() => {
            setError('')
            setShowCreate(true)
          }}
        >
          新建智能体
        </button>
      </div>

      {showCreate && (
        <form className="skill-create-form" onSubmit={onCreateAgent}>
          <input
            className="skill-name-input"
            value={newName}
            maxLength={AGENT_NAME_MAX}
            placeholder="智能体名称（1–120 字，本班内唯一）"
            onChange={(e) => setNewName(e.target.value)}
          />
          <textarea
            className="settings-textarea"
            value={newPrompt}
            maxLength={AGENT_PROMPT_MAX}
            rows={4}
            placeholder="智能体提示词（≤2000 字），例如：你是一名耐心的解题引导者……"
            onChange={(e) => setNewPrompt(e.target.value)}
          />
          <div className="settings-actions">
            <button
              className="btn btn-primary btn-small"
              type="submit"
              disabled={creating || !newName.trim()}
            >
              {creating ? '新建中…' : '新建'}
            </button>
            <button
              type="button"
              className="btn btn-small"
              disabled={creating}
              onClick={resetCreate}
            >
              取消
            </button>
          </div>
        </form>
      )}

      {agents.length === 0 ? (
        <p className="muted">本班尚未创建智能体。</p>
      ) : (
        agents.map((a) => {
          const editing = editingId === a.id
          const open = openBindId === a.id
          const draft = bindDrafts[a.id]
          const dirty =
            !!draft &&
            (!sameIds(draft.skills, a.skillIds) || !sameIds(draft.materials, a.handoutIds))
          return (
            <div key={a.id} className="agent-card">
              <div className="agent-card-head">
                {editing ? (
                  <input
                    className="skill-name-input"
                    value={editName}
                    maxLength={AGENT_NAME_MAX}
                    placeholder="智能体名称"
                    onChange={(e) => setEditName(e.target.value)}
                  />
                ) : (
                  <span className="skill-name">{a.name}</span>
                )}
                <div className="agent-card-actions">
                  {editing ? (
                    <>
                      <button
                        type="button"
                        className="btn btn-primary btn-small"
                        disabled={savingAgent || !editName.trim()}
                        onClick={onSaveAgent}
                      >
                        {savingAgent ? '保存中…' : '保存'}
                      </button>
                      <button
                        type="button"
                        className="btn btn-small"
                        disabled={savingAgent}
                        onClick={cancelEdit}
                      >
                        取消
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        type="button"
                        className="btn btn-small"
                        disabled={busyBind !== null}
                        onClick={() => openEdit(a)}
                      >
                        编辑
                      </button>
                      <button
                        type="button"
                        className="btn btn-small"
                        disabled={busyBind !== null}
                        onClick={() => toggleBind(a)}
                      >
                        {open ? '收起绑定' : '管理绑定'}
                      </button>
                      <button
                        type="button"
                        className="btn btn-small skill-delete"
                        disabled={busyBind !== null}
                        onClick={() => onDeleteAgent(a)}
                      >
                        删除
                      </button>
                    </>
                  )}
                </div>
              </div>

              {editing ? (
                <textarea
                  className="settings-textarea"
                  value={editPrompt}
                  maxLength={AGENT_PROMPT_MAX}
                  rows={5}
                  placeholder="智能体提示词（≤2000 字）"
                  onChange={(e) => setEditPrompt(e.target.value)}
                />
              ) : (
                <div className="agent-card-body">
                  {a.systemPrompt ? (
                    <details className="skill-content-detail">
                      <summary>查看提示词</summary>
                      <pre className="skill-content-view">{a.systemPrompt}</pre>
                    </details>
                  ) : (
                    <span className="muted">未设置提示词</span>
                  )}
                  <div className="agent-bind-summary">
                    <span className="muted">
                      技能：
                      {a.skillIds.length === 0
                        ? '未绑定'
                        : a.skillIds.map((id) => skillName(id)).join('、')}
                    </span>
                    <span className="muted">
                      材料：
                      {a.handoutIds.length === 0
                        ? '未绑定'
                        : a.handoutIds.map((id) => materialName(id)).join('、')}
                    </span>
                  </div>
                </div>
              )}

              {open && !editing && draft && (
                <div className="agent-bind-panel">
                  <p className="settings-label">绑定技能（仅绑定且启用的技能会参与该智能体应答）</p>
                  {skills.length === 0 ? (
                    <p className="muted">本班暂无技能，请到「技能管理」新建。</p>
                  ) : (
                    skills.map((sk) => (
                      <label key={sk.id} className="agent-bind-row">
                        <input
                          type="checkbox"
                          className="skill-switch"
                          checked={draft.skills.includes(sk.id)}
                          disabled={busyBind !== null}
                          onChange={(e) => toggleDraftSkill(a.id, sk.id, e.target.checked)}
                        />
                        <span className="skill-name">{sk.name}</span>
                        <span className={`skill-source src-${sk.source}`}>
                          {SOURCE_LABEL[sk.source] ?? sk.source}
                        </span>
                        {!sk.enabled && <span className="muted skill-desc">已停用</span>}
                      </label>
                    ))
                  )}

                  <p className="settings-label">绑定材料（仅绑定的材料会进入该智能体的检索范围）</p>
                  {materials.length === 0 ? (
                    <p className="muted">本班暂无材料，请到「本班资料」上传。</p>
                  ) : (
                    materials.map((m) => (
                      <label key={m.id} className="agent-bind-row">
                        <input
                          type="checkbox"
                          className="skill-switch"
                          checked={draft.materials.includes(m.id)}
                          disabled={busyBind !== null}
                          onChange={(e) => toggleDraftMaterial(a.id, m.id, e.target.checked)}
                        />
                        <span className="skill-name">{m.name}</span>
                        <span className="muted skill-desc">{m.fileType.toUpperCase()}</span>
                      </label>
                    ))
                  )}

                  <div className="settings-actions">
                    <button
                      type="button"
                      className="btn btn-primary btn-small"
                      disabled={busyBind !== null || !dirty}
                      onClick={() => onSaveBindings(a)}
                    >
                      {busyBind === a.id ? '保存中…' : '保存绑定'}
                    </button>
                    {dirty && <span className="muted">有未保存的绑定改动</span>}
                  </div>
                </div>
              )}
            </div>
          )
        })
      )}

      <div className="settings-mcp">
        <div className="mcp-section-head">
          <p className="settings-label">
            MCP 服务器（远端工具服务；登记并连接后，技能正文的「工具」行可引用其工具）
          </p>
          <div className="settings-actions">
            <button type="button" className="btn btn-small" disabled={mcpLoading} onClick={loadMcp}>
              {mcpLoading ? '刷新中…' : '刷新'}
            </button>
            <button
              type="button"
              className="btn btn-small"
              disabled={showServerForm || editingSrvId !== null}
              onClick={openCreateServer}
            >
              登记服务器
            </button>
          </div>
        </div>

        {(showServerForm || editingSrvId !== null) && (
          <form className="mcp-server-form" onSubmit={onSaveServer}>
            <input
              className="skill-name-input"
              value={srvName}
              maxLength={120}
              placeholder="服务器名称（小写字母开头，仅小写字母、数字、下划线与连字符）"
              onChange={(e) => setSrvName(e.target.value)}
            />
            <input
              className="skill-name-input"
              value={srvBaseUrl}
              maxLength={512}
              placeholder="Base URL，例如 http://mcp-demo:8090"
              onChange={(e) => setSrvBaseUrl(e.target.value)}
            />
            <input
              className="skill-name-input"
              type="password"
              value={srvToken}
              autoComplete="off"
              placeholder={editingSrvId !== null ? '令牌（留空表示不修改）' : 'Bearer 令牌（可留空）'}
              onChange={(e) => setSrvToken(e.target.value)}
            />
            <div className="settings-actions">
              <button
                className="btn btn-primary btn-small"
                type="submit"
                disabled={savingServer || !srvName.trim() || !srvBaseUrl.trim()}
              >
                {savingServer ? '保存中…' : editingSrvId !== null ? '保存修改' : '登记'}
              </button>
              <button
                type="button"
                className="btn btn-small"
                disabled={savingServer}
                onClick={cancelServerForm}
              >
                取消
              </button>
            </div>
          </form>
        )}

        {servers.length === 0 ? (
          <p className="muted">本班尚未登记 MCP 服务器。</p>
        ) : (
          servers.map((s: MCPServer) => (
            <div key={s.id} className="mcp-server-row">
              <span className="skill-info">
                <span className="skill-name-row">
                  <span className="skill-name">{s.name}</span>
                  <span className={`mcp-badge${s.connected ? ' on' : ''}`}>
                    {s.connected ? '已连接' : '未连接'}
                  </span>
                  {s.hasAuth && <span className="skill-source">已配置令牌</span>}
                </span>
                <span className="muted mcp-server-url">{s.baseUrl}</span>
                {s.tools.length > 0 ? (
                  <span className="mcp-tools">
                    <span className="muted">已发现工具：</span>
                    {s.tools.map((t) => (
                      <span key={t.name} className="mcp-tool-chip" title={t.description}>
                        {t.name}
                      </span>
                    ))}
                  </span>
                ) : (
                  <span className="muted">暂无已发现工具</span>
                )}
                {s.lastError && <span className="muted">最近错误：{s.lastError}</span>}
              </span>
              <div className="mcp-server-actions">
                <button
                  type="button"
                  className="btn btn-small"
                  disabled={mcpBusy !== null}
                  onClick={() => onToggleConnect(s)}
                >
                  {mcpBusy === s.id ? '处理中…' : s.connected ? '断开' : '连接'}
                </button>
                <button
                  type="button"
                  className="btn btn-small"
                  disabled={mcpBusy !== null || showServerForm}
                  onClick={() => openEditServer(s)}
                >
                  修改
                </button>
                <button
                  type="button"
                  className="btn btn-small skill-delete"
                  disabled={mcpBusy !== null}
                  onClick={() => onDeleteServer(s)}
                >
                  删除
                </button>
              </div>
            </div>
          ))
        )}

        <p className="settings-label mcp-calls-label">最近调用记录（本班，最多 50 条）</p>
        {calls.length === 0 ? (
          <p className="muted">暂无调用记录。</p>
        ) : (
          <table className="mcp-call-table">
            <thead>
              <tr>
                <th>工具引用</th>
                <th>服务器</th>
                <th>状态</th>
                <th>学生</th>
                <th>耗时</th>
                <th>时间</th>
                <th>备注</th>
              </tr>
            </thead>
            <tbody>
              {calls.map((c: MCPCall) => (
                <tr key={c.id}>
                  <td className="mcp-call-ref">{c.toolRef}</td>
                  <td>{c.serverName}</td>
                  <td>
                    <span className={`mcp-status st-${c.status}`}>
                      {CALL_STATUS_LABEL[c.status] ?? c.status}
                    </span>
                  </td>
                  <td className="muted">{c.username || '—'}</td>
                  <td className="muted">{c.durationMs} ms</td>
                  <td className="muted">{formatCallTime(c.createdAt)}</td>
                  <td className="muted">{c.error || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {error && <div className="alert alert-error">{error}</div>}
    </section>
  )
}
