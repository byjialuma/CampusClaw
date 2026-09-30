import { FormEvent, useEffect, useState } from 'react'
import { fetchAssistantConfig, saveAssistantPrompt, setSkillEnabled } from '../api'
import type { AssistantConfigData } from '../types'

// AssistantSettings 仅对教师渲染：维护本班助手提示词与「解题引导」技能开关。
// 学生侧不渲染本组件，且服务端同样以 403 拒绝学生调用。
export default function AssistantSettings() {
  const [cfg, setCfg] = useState<AssistantConfigData | null>(null)
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [savingPrompt, setSavingPrompt] = useState(false)
  const [togglingSkill, setTogglingSkill] = useState(false)
  const [error, setError] = useState('')
  const [savedAt, setSavedAt] = useState('')

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
    if (togglingSkill) return
    setTogglingSkill(true)
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
      setTogglingSkill(false)
    }
  }

  return (
    <section className="card assistant-settings">
      <h2 className="card-title">助手设置（仅本班教师可见）</h2>

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
            <p className="settings-label">技能</p>
            {cfg?.skills.map((sk) => (
              <label key={sk.id} className="skill-toggle-row">
                <span className="skill-info">
                  <span className="skill-name">{sk.name}</span>
                  <span className="muted skill-desc">{sk.description}</span>
                </span>
                <input
                  type="checkbox"
                  className="skill-switch"
                  checked={sk.enabled}
                  disabled={togglingSkill}
                  onChange={(e) => onToggleSkill(sk.id, e.target.checked)}
                />
                <span className={`skill-state${sk.enabled ? ' on' : ''}`}>
                  {sk.enabled ? '已开启' : '已停用'}
                </span>
              </label>
            ))}
          </div>

          {error && <div className="alert alert-error">{error}</div>}
        </>
      )}
    </section>
  )
}
