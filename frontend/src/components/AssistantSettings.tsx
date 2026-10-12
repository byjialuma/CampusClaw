import { ChangeEvent, FormEvent, useEffect, useRef, useState } from 'react'
import {
  createSkill,
  deleteSkill,
  fetchAssistantConfig,
  saveAssistantPrompt,
  setSkillEnabled,
  uploadSkill,
} from '../api'
import type { AssistantConfigData, TutorSkillSource } from '../types'

// 技能来源徽标文案。
const SOURCE_LABEL: Record<TutorSkillSource, string> = {
  seed: '内置',
  upload: '上传',
  online: '自建',
}

// 上传技能文档的前端预校验口径（服务端仍做最终校验）。
const SKILL_FILE_MAX_BYTES = 64 * 1024
const SKILL_CONTENT_MAX = 20000
const SKILL_NAME_MAX = 120

// AssistantSettings 仅对教师渲染：维护本班助手提示词与技能列表。
// 学生侧不渲染本组件，且服务端同样以 403 拒绝学生调用。
export default function AssistantSettings() {
  const [cfg, setCfg] = useState<AssistantConfigData | null>(null)
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [savingPrompt, setSavingPrompt] = useState(false)
  const [busySkill, setBusySkill] = useState<number | null>(null)
  const [uploading, setUploading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [showCreateForm, setShowCreateForm] = useState(false)
  const [newName, setNewName] = useState('')
  const [newContent, setNewContent] = useState('')
  const [error, setError] = useState('')
  const [savedAt, setSavedAt] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const c = await fetchAssistantConfig()
        if (cancelled) return
        setCfg(c)
        setDraft(c.systemPrompt)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : '加载助手配置失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  async function onSavePrompt(e: FormEvent) {
    e.preventDefault()
    const p = draft.trim()
    if (!p || savingPrompt || p === cfg?.systemPrompt) return
    setSavingPrompt(true)
    setError('')
    setSavedAt('')
    try {
      await saveAssistantPrompt(p)
      const now = new Date()
      setSavedAt(
        `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}:${String(
          now.getSeconds(),
        ).padStart(2, '0')}`,
      )
      setCfg((prev) => (prev ? { ...prev, systemPrompt: p } : prev))
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存提示词失败')
    } finally {
      setSavingPrompt(false)
    }
  }

  async function onToggleSkill(skillId: number, enabled: boolean) {
    if (busySkill !== null) return
    setBusySkill(skillId)
    setError('')
    try {
      await setSkillEnabled(skillId, enabled)
      setCfg((prev) =>
        prev
          ? { ...prev, skills: prev.skills.map((s) => (s.id === skillId ? { ...s, enabled } : s)) }
          : prev,
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : '更新技能失败')
    } finally {
      setBusySkill(null)
    }
  }

  async function onDeleteSkill(skillId: number, name: string) {
    if (busySkill !== null) return
    if (!window.confirm(`确定删除技能「${name}」？删除后不再参与回答，历史记录中的快照仍保留名称。`)) {
      return
    }
    setBusySkill(skillId)
    setError('')
    try {
      await deleteSkill(skillId)
      setCfg((prev) => (prev ? { ...prev, skills: prev.skills.filter((s) => s.id !== skillId) } : prev))
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除技能失败')
    } finally {
      setBusySkill(null)
    }
  }

  async function onUploadFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file || uploading) return
    const lower = file.name.toLowerCase()
    if (!lower.endsWith('.md') && !lower.endsWith('.txt')) {
      setError('技能文档仅支持 .md 或 .txt 文件')
      return
    }
    if (file.size > SKILL_FILE_MAX_BYTES) {
      setError('技能文档不能超过 64KB')
      return
    }
    setUploading(true)
    setError('')
    try {
      const sk = await uploadSkill(file)
      setCfg((prev) => (prev ? { ...prev, skills: [...prev.skills, sk] } : prev))
    } catch (err) {
      setError(err instanceof Error ? err.message : '上传技能失败')
    } finally {
      setUploading(false)
    }
  }

  async function onCreateSkill(e: FormEvent) {
    e.preventDefault()
    const name = newName.trim()
    const content = newContent.trim()
    if (!name || !content || creating) return
    setCreating(true)
    setError('')
    try {
      const sk = await createSkill(name, content)
      setCfg((prev) => (prev ? { ...prev, skills: [...prev.skills, sk] } : prev))
      setNewName('')
      setNewContent('')
      setShowCreateForm(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : '新建技能失败')
    } finally {
      setCreating(false)
    }
  }

  return (
    <section className="assistant-settings">
      {loading ? (
        <p className="muted">加载配置…</p>
      ) : (
        <>
          <form className="settings-form" onSubmit={onSavePrompt}>
            <label className="settings-label" htmlFor="assistant-prompt">
              助手提示词（下一轮对话起生效，最多 2000 字）
            </label>
            <textarea
              id="assistant-prompt"
              className="settings-textarea"
              value={draft}
              maxLength={2000}
              rows={5}
              placeholder="定义助手的身份、回答风格与要求，例如：你是一名耐心的解题引导者……"
              onChange={(e) => setDraft(e.target.value)}
            />
            <div className="settings-actions">
              <button
                className="btn btn-primary btn-small"
                type="submit"
                disabled={savingPrompt || !draft.trim() || draft.trim() === cfg?.systemPrompt}
              >
                {savingPrompt ? '保存中…' : '保存提示词'}
              </button>
              {savedAt && <span className="muted">已保存 {savedAt}</span>}
            </div>
          </form>

          <div className="settings-skills">
            <p className="settings-label">
              技能（开启的技能内容会逐条注入助手指令；内置「解题引导」可停用不可删除）
            </p>
            {cfg?.skills.map((sk) => (
              <div key={sk.id} className="skill-toggle-row">
                <span className="skill-info">
                  <span className="skill-name-row">
                    <span className="skill-name">{sk.name}</span>
                    <span className={`skill-source src-${sk.source}`}>
                      {SOURCE_LABEL[sk.source] ?? sk.source}
                    </span>
                  </span>
                  {sk.description && <span className="muted skill-desc">{sk.description}</span>}
                  {sk.content && (
                    <details className="skill-content-detail">
                      <summary>查看内容</summary>
                      <pre className="skill-content-view">{sk.content}</pre>
                    </details>
                  )}
                </span>
                <input
                  type="checkbox"
                  className="skill-switch"
                  checked={sk.enabled}
                  disabled={busySkill !== null}
                  onChange={(e) => onToggleSkill(sk.id, e.target.checked)}
                />
                <span className={`skill-state${sk.enabled ? ' on' : ''}`}>
                  {sk.enabled ? '已开启' : '已停用'}
                </span>
                <button
                  type="button"
                  className="btn btn-small skill-delete"
                  disabled={sk.builtin || busySkill !== null}
                  title={sk.builtin ? '内置技能不可删除' : '删除该技能'}
                  onClick={() => onDeleteSkill(sk.id, sk.name)}
                >
                  删除
                </button>
              </div>
            ))}

            <div className="skill-add-row">
              <input
                ref={fileRef}
                type="file"
                accept=".md,.txt"
                hidden
                onChange={onUploadFile}
              />
              <button
                type="button"
                className="btn btn-small"
                disabled={uploading}
                onClick={() => fileRef.current?.click()}
              >
                {uploading ? '上传中…' : '上传技能文档（.md/.txt ≤64KB）'}
              </button>
              <button
                type="button"
                className="btn btn-small"
                onClick={() => setShowCreateForm((v) => !v)}
              >
                {showCreateForm ? '收起新建表单' : '在线新建技能'}
              </button>
            </div>

            {showCreateForm && (
              <form className="skill-create-form" onSubmit={onCreateSkill}>
                <input
                  className="skill-name-input"
                  value={newName}
                  maxLength={SKILL_NAME_MAX}
                  placeholder="技能名称（1–120 字）"
                  onChange={(e) => setNewName(e.target.value)}
                />
                <textarea
                  className="settings-textarea"
                  value={newContent}
                  maxLength={SKILL_CONTENT_MAX}
                  rows={4}
                  placeholder="技能指令内容，例如：回答时先回顾相关公式，再引导学生自行推导……"
                  onChange={(e) => setNewContent(e.target.value)}
                />
                <div className="settings-actions">
                  <button
                    className="btn btn-primary btn-small"
                    type="submit"
                    disabled={creating || !newName.trim() || !newContent.trim()}
                  >
                    {creating ? '新建中…' : '新建技能'}
                  </button>
                  <span className="muted">新建后默认为停用状态，开启后生效</span>
                </div>
              </form>
            )}
          </div>

          {error && <div className="alert alert-error">{error}</div>}
        </>
      )}
    </section>
  )
}
