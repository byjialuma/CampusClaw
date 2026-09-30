import type {
  Material,
  Me,
  SearchResult,
  QAResult,
  Citation,
  TutorConversation,
  TutorMessage,
  TutorStreamEvent,
  AssistantConfigData,
} from './types'

const TOKEN_KEY = 'cc_access_token'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

// 所有受保护请求自动携带 Authorization: Bearer 头。
async function request<T>(path: string, init: RequestInit = {}): Promise<{ status: number; data: T }> {
  const token = getToken()
  const headers = new Headers(init.headers)
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }
  const resp = await fetch(path, { ...init, headers })
  const text = await resp.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  return { status: resp.status, data: data as T }
}

function handle401(): never {
  clearToken()
  window.location.replace('/login?next=' + encodeURIComponent(window.location.pathname))
  throw new ApiError(401, '未登录')
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export interface LoginResult {
  user: Me
  accessToken: string
  expiresAt: string
}

export async function login(username: string, password: string): Promise<Me> {
  const { status, data } = await request<LoginResult | { error: string }>('/api/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '登录失败')
  }
  const result = data as LoginResult
  setToken(result.accessToken)
  return result.user
}

export async function logout(): Promise<void> {
  clearToken()
}

export async function fetchMe(): Promise<Me | null> {
  if (!getToken()) return null
  const { status, data } = await request<Me>('/api/me')
  if (status === 401) {
    clearToken()
    return null
  }
  if (status !== 200) throw new ApiError(status, '获取当前用户失败')
  return data
}

export async function fetchMaterials(): Promise<Material[]> {
  const { status, data } = await request<{ materials: Material[] }>('/api/materials')
  if (status === 401) handle401()
  if (status !== 200) throw new ApiError(status, '加载材料列表失败')
  return data.materials
}

export async function uploadMaterial(file: File): Promise<Material> {
  const form = new FormData()
  form.append('file', file)
  const { status, data } = await request<Material | { error: string }>('/api/materials', {
    method: 'POST',
    body: form,
  })
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '上传失败')
  }
  return data as Material
}

// 班级范围由服务端从 JWT 解析，前端不传任何班级参数。
export async function searchMaterials(query: string): Promise<SearchResult[]> {
  const { status, data } = await request<{ results: SearchResult[] } | { error: string }>(
    `/api/search?q=${encodeURIComponent(query)}`,
  )
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '知识库检索失败'
    throw new ApiError(status, msg)
  }
  return (data as { results: SearchResult[] }).results
}

// 知识问答：提交问题，返回回答与引用来源。
export async function askQuestion(question: string): Promise<QAResult> {
  const { status, data } = await request<QAResult | { error: string }>('/api/qa', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question }),
  })
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '知识库问答失败'
    throw new ApiError(status, msg)
  }
  return data as QAResult
}

export async function fetchContent(m: Material): Promise<Blob> {
  const token = getToken()
  const headers: HeadersInit = {}
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  const resp = await fetch(`/api/materials/${m.id}/content`, { headers })
  if (resp.status === 401) {
    clearToken()
    window.location.replace('/login')
    throw new ApiError(401, '未登录')
  }
  if (!resp.ok) throw new ApiError(resp.status, '加载文件内容失败')
  return resp.blob()
}

export async function createTicket(materialId: number): Promise<string> {
  const { status, data } = await request<{ ticket: string; error?: string }>(
    `/api/materials/${materialId}/download-ticket`,
    { method: 'POST' },
  )
  if (status === 401) handle401()
  if (status !== 200) throw new ApiError(status, data.error ?? '创建下载凭证失败')
  return data.ticket
}

export type DownloadResult =
  | { ok: true; blob: Blob; fileName: string }
  | { ok: false; status: number }

// 下载页：凭一次性票据取文件，返回 Blob 与原始文件名。
export async function downloadByTicket(ticket: string): Promise<DownloadResult> {
  const token = getToken()
  const headers: HeadersInit = {}
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  const resp = await fetch(`/api/download?ticket=${encodeURIComponent(ticket)}`, {
    headers,
    cache: 'no-store',
  })
  if (!resp.ok) return { ok: false, status: resp.status }
  const blob = await resp.blob()
  const fileName = parseFileName(resp.headers.get('Content-Disposition')) || 'download'
  return { ok: true, blob, fileName }
}

function parseFileName(cd: string | null): string {
  if (!cd) return ''
  const utf8 = /filename\*=UTF-8''([^;]+)/i.exec(cd)
  if (utf8) {
    try {
      return decodeURIComponent(utf8[1])
    } catch {
      return utf8[1]
    }
  }
  return ''
}

// ==================== 解题助手 ====================

// 创建会话（class_id/user_id 由服务端从 token 解析）。
export async function createConversation(): Promise<TutorConversation> {
  const { status, data } = await request<TutorConversation | { error: string }>(
    '/api/tutor/conversations',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    },
  )
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '创建会话失败')
  }
  return data as TutorConversation
}

// 本人会话列表。
export async function fetchConversations(): Promise<TutorConversation[]> {
  const { status, data } = await request<TutorConversation[] | { error: string }>(
    '/api/tutor/conversations',
  )
  if (status === 401) handle401()
  if (status !== 200) throw new ApiError(status, '加载会话列表失败')
  return (data as TutorConversation[]) ?? []
}

// 读取会话全部消息（刷新后回看历史）。
export async function fetchTutorMessages(conversationId: number): Promise<TutorMessage[]> {
  const { status, data } = await request<TutorMessage[] | { error: string }>(
    `/api/tutor/conversations/${conversationId}/messages`,
  )
  if (status === 401) handle401()
  if (status !== 200) throw new ApiError(status, '加载消息失败')
  return (data as TutorMessage[]) ?? []
}

// askTutor 以 SSE 流式提问：通过 onEvent 回调 meta/delta/done/error。
// 中断由调用方传入 AbortSignal（AbortController）。
export async function askTutor(
  conversationId: number,
  question: string,
  onEvent: (e: TutorStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const token = getToken()
  const resp = await fetch(`/api/tutor/conversations/${conversationId}/messages`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({ question }),
    signal,
  })

  if (resp.status === 401) handle401()
  // meta 之前失败（400/404/503）：响应仍是 JSON，抛出带服务端文案的错误。
  if (!resp.ok) {
    let msg = '解题助手暂不可用'
    try {
      const errBody = (await resp.clone().json()) as { error?: string }
      if (errBody.error) msg = errBody.error
    } catch {
      /* 非 JSON 响应时使用默认文案 */
    }
    throw new ApiError(resp.status, msg)
  }
  if (!resp.body) throw new ApiError(500, '浏览器不支持流式响应')

  await readSSEStream(resp.body, onEvent)
}

// readSSEStream 手工解析 SSE 帧（fetch 无法用 EventSource 携带 Authorization 头）。
async function readSSEStream(
  body: ReadableStream<Uint8Array>,
  onEvent: (e: TutorStreamEvent) => void,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    // 统一换行，事件以空行分隔。
    buffer = buffer.replace(/\r\n/g, '\n').replace(/\r/g, '\n')

    let idx: number
    while ((idx = buffer.indexOf('\n\n')) >= 0) {
      const rawEvent = buffer.slice(0, idx)
      buffer = buffer.slice(idx + 2)
      dispatchSSEEvent(rawEvent, onEvent)
    }
  }
  buffer += decoder.decode()
  if (buffer.trim()) dispatchSSEEvent(buffer, onEvent)
}

// dispatchSSEEvent 解析单个事件块（event: / data: 行）并回调。
function dispatchSSEEvent(rawEvent: string, onEvent: (e: TutorStreamEvent) => void): void {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of rawEvent.split('\n')) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).replace(/^ /, ''))
    }
  }
  const dataStr = dataLines.join('\n')
  if (!dataStr) return

  let data: unknown
  try {
    data = JSON.parse(dataStr)
  } catch {
    return
  }

  switch (event) {
    case 'meta': {
      const d = data as { citations?: Citation[]; skillEnabled?: boolean }
      onEvent({
        type: 'meta',
        citations: Array.isArray(d.citations) ? d.citations : [],
        skillEnabled: d.skillEnabled === true,
      })
      break
    }
    case 'delta': {
      const d = data as { text?: string }
      if (d.text) onEvent({ type: 'delta', text: d.text })
      break
    }
    case 'done': {
      const d = data as { messageId?: number }
      onEvent({ type: 'done', messageId: d.messageId ?? 0 })
      break
    }
    case 'error': {
      const d = data as { error?: string }
      onEvent({ type: 'error', error: d.error ?? '助手暂不可用，请稍后再试' })
      break
    }
  }
}

// ---------- 教师：助手配置管理 ----------

// 读取本班助手配置（仅教师，学生调用服务端 403）。
export async function fetchAssistantConfig(): Promise<AssistantConfigData> {
  const { status, data } = await request<AssistantConfigData | { error: string }>(
    '/api/tutor/assistant',
  )
  if (status === 401) handle401()
  if (status !== 200) throw new ApiError(status, '加载助手配置失败')
  return data as AssistantConfigData
}

// 保存本班助手提示词，下一轮提问起生效。
export async function saveAssistantPrompt(systemPrompt: string): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    '/api/tutor/assistant/prompt',
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ systemPrompt }),
    },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '保存提示词失败')
  }
}

// 启用/停用解题引导技能。
export async function setSkillEnabled(skillId: number, enabled: boolean): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    `/api/tutor/assistant/skills/${skillId}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '更新技能失败')
  }
}
