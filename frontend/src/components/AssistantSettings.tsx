import { FormEvent, useEffect, useState } from 'react'
import {
  fetchAssistantConfig,
  saveAssistantPrompt,
  setSkillEnabled,
} from '../api'
import type { AssistantConfigData, TutorSkill, TutorSkillSource } from '../types'

// 技能来源徽标文案。
const SOURCE_LABEL: Record<TutorSkillSource, string> = {
  seed: '内置',
  upload: '上传',
  online: '自建',
}

// AssistantSettings 是教师「助手设置」标签页：仅维护提示词与技能启停。
// 技能的新建/上传/删除放在 SkillManagement 标签页。两页各自拉取最新配置，
// 切换时重新加载，避免跨页状态同步漂移。
export default function AssistantSettings() {
  const [cfg, setCfg] = useState<AssistantConfigData | null>(null)
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [savingPrompt, setSavingPrompt] = useState(false)
  const [busySkill, setBusySkill] = useState<number | null>(null)
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

  if (loading) {
    return <p className="muted">加载配置…</p>
  }

  return (
    <section className="assistant-settings">
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
          已有技能（开启后内容会逐条注入助手指令；新建/上传/删除请到「技能管理」标签页）
        </p>
        {cfg?.skills.map((sk: TutorSkill) => (
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
          </div>
        ))}
      </div>

      {error && <div className="alert alert-error">{error}</div>}
    </section>
  )
}
