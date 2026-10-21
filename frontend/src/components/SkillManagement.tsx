import { ChangeEvent, FormEvent, useEffect, useRef, useState } from 'react'
import {
  createSkill,
  deleteSkill,
  fetchAssistantConfig,
  fetchSkillPackage,
  setSkillEnabled,
  uploadSkill,
  uploadSkillPackage,
} from '../api'
import type {
  AssistantConfigData,
  SkillPackageFile,
  TutorSkill,
  TutorSkillSource,
} from '../types'

// 技能来源徽标文案。
const SOURCE_LABEL: Record<TutorSkillSource, string> = {
  seed: '内置',
  upload: '上传',
  online: '自建',
  package: '技能包',
}

// 资源清单按顶层目录分组展示。
const RESOURCE_GROUPS: { kind: string; label: string }[] = [
  { kind: 'references', label: 'references（参考资料）' },
  { kind: 'assets', label: 'assets（模板与素材）' },
  { kind: 'scripts', label: 'scripts（脚本，不会被执行）' },
]

const SKILL_FILE_MAX_BYTES = 64 * 1024
const SKILL_PACKAGE_MAX_BYTES = 512 * 1024
const SKILL_CONTENT_MAX = 20000
const SKILL_NAME_MAX = 120

// 技能正文「工具」行解析出的一条引用。
interface ParsedToolRef {
  raw: string
  builtin: boolean
}

// parseSkillTools 在前端按与后端相同的「工具」行语法解析：行首「工具：」/「工具:」，
// 值以半角逗号、全角逗号或顿号分隔；knowledge_search 为内置引用。
function parseSkillTools(content: string): ParsedToolRef[] {
  const refs: ParsedToolRef[] = []
  for (const rawLine of content.split('\n')) {
    const line = rawLine.trim()
    let value = ''
    if (line.startsWith('工具：') || line.startsWith('工具:')) {
      value = line.slice(3)
    } else {
      continue
    }
    for (const item of value.split(/[,，、]/)) {
      const ref = item.trim()
      if (ref) refs.push({ raw: ref, builtin: ref === 'knowledge_search' })
    }
  }
  return refs
}

// SkillManagement 是教师「技能管理」标签页：技能列表 + 上传 + 在线新建 + 删除 + 启停。
// 与 AssistantSettings 共享同一份配置（双方都会拉取最新数据），不通过 props 同步——
// 教师在两页间切换时各自重新拉取，避免跨页状态漂移。
export default function SkillManagement() {
  const [cfg, setCfg] = useState<AssistantConfigData | null>(null)
  const [loading, setLoading] = useState(true)
  const [busySkill, setBusySkill] = useState<number | null>(null)
  const [uploading, setUploading] = useState(false)
  const [packageUploading, setPackageUploading] = useState(false)
  // 技能包资源清单：列表接口不含文件明细，按需拉取详情后缓存。
  const [packageFiles, setPackageFiles] = useState<Record<number, SkillPackageFile[]>>({})
  const [packageLoading, setPackageLoading] = useState<number | null>(null)
  const [creating, setCreating] = useState(false)
  const [showCreateForm, setShowCreateForm] = useState(false)
  const [newName, setNewName] = useState('')
  const [newContent, setNewContent] = useState('')
  const [error, setError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)
  const packageRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const c = await fetchAssistantConfig()
        if (cancelled) return
        setCfg(c)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : '加载技能列表失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

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

  async function onUploadPackage(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file || packageUploading) return
    if (!file.name.toLowerCase().endsWith('.zip')) {
      setError('技能包仅支持 .zip 文件')
      return
    }
    if (file.size > SKILL_PACKAGE_MAX_BYTES) {
      setError('技能包不能超过 512KB')
      return
    }
    setPackageUploading(true)
    setError('')
    try {
      const sk = await uploadSkillPackage(file)
      setCfg((prev) => (prev ? { ...prev, skills: [...prev.skills, sk] } : prev))
      if (sk.files) {
        setPackageFiles((prev) => ({ ...prev, [sk.id]: sk.files ?? [] }))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '上传技能包失败')
    } finally {
      setPackageUploading(false)
    }
  }

  async function onLoadPackageFiles(skillId: number) {
    if (packageLoading !== null) return
    setPackageLoading(skillId)
    setError('')
    try {
      const detail = await fetchSkillPackage(skillId)
      setPackageFiles((prev) => ({ ...prev, [skillId]: detail.files ?? [] }))
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载资源清单失败')
    } finally {
      setPackageLoading(null)
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

  if (loading) {
    return <p className="muted">加载技能列表…</p>
  }

  return (
    <section className="assistant-settings skill-management">
      <div className="settings-skills">
        <p className="settings-label">
          技能列表（开启后内容会逐条注入助手指令；内置「解题引导」可停用不可删除）
        </p>
        {cfg?.skills.map((sk: TutorSkill) => {
          const toolRefs = parseSkillTools(sk.content)
          return (
            <div key={sk.id} className="skill-toggle-row">
              <span className="skill-info">
                <span className="skill-name-row">
                  <span className="skill-name">{sk.name}</span>
                  <span className={`skill-source src-${sk.source}`}>
                    {SOURCE_LABEL[sk.source] ?? sk.source}
                  </span>
                </span>
                {sk.description && <span className="muted skill-desc">{sk.description}</span>}
                {sk.source === 'package' && (
                  <span className="skill-package-files">
                    {packageFiles[sk.id] === undefined ? (
                      <button
                        type="button"
                        className="btn btn-small"
                        disabled={packageLoading !== null}
                        onClick={() => onLoadPackageFiles(sk.id)}
                      >
                        {packageLoading === sk.id ? '加载资源清单…' : '查看资源清单'}
                      </button>
                    ) : (
                      <>
                        {packageFiles[sk.id].length === 0 && (
                          <span className="muted">包内没有资源文件</span>
                        )}
                        {RESOURCE_GROUPS.map((group) => {
                          const items = packageFiles[sk.id].filter((f) => f.kind === group.kind)
                          if (items.length === 0) return null
                          return (
                            <span key={group.kind} className="skill-package-group">
                              <span className="muted">{group.label}</span>
                              {items.map((f) => (
                                <span key={f.path} className="skill-resource-chip" title={`${f.size} 字节`}>
                                  {f.path}
                                </span>
                              ))}
                            </span>
                          )
                        })}
                        <span className="muted skill-package-note">
                          资源仅在技能命中时按白名单加载；脚本不会被执行。
                        </span>
                      </>
                    )}
                  </span>
                )}
                {toolRefs.length > 0 && (
                  <span className="skill-tool-refs">
                    <span className="muted">工具引用：</span>
                    {toolRefs.map((r) => (
                      <span
                        key={r.raw}
                        className={`skill-tool-chip${r.builtin ? ' builtin' : ''}`}
                        title={r.builtin ? '内置引用（本班知识库检索）' : '远端 MCP 工具引用'}
                      >
                        {r.raw}
                      </span>
                    ))}
                  </span>
                )}
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
          )
        })}

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
          <input
            ref={packageRef}
            type="file"
            accept=".zip"
            hidden
            onChange={onUploadPackage}
          />
          <button
            type="button"
            className="btn btn-small"
            disabled={packageUploading}
            title="zip 包根目录需包含 SKILL.md；references/assets/scripts 为可选目录，脚本不会被执行"
            onClick={() => packageRef.current?.click()}
          >
            {packageUploading ? '上传中…' : '上传技能包（.zip ≤512KB）'}
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
    </section>
  )
}
