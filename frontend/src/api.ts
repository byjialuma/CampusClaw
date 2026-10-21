import type {
  Material,
  Me,
  SearchResult,
  QAResult,
  Citation,
  StudentInfo,
  TutorConversation,
  TutorMessage,
  TutorStreamEvent,
  TutorSkill,
  TutorSkillRef,
  AssistantConfigData,
  TutorAgent,
  StudentAgent,
  MCPServer,
  MCPCall,
  CreateMCPServerRequest,
  UpdateMCPServerRequest,
  SkillResourceStatus,
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

// 教师：本班学生名单（class_id 由服务端从 JWT claims 解析）。
export async function fetchStudents(): Promise<StudentInfo[]> {
  const { status, data } = await request<StudentInfo[] | { error: string }>('/api/class/students')
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '加载学生名单失败'
    throw new ApiError(status, msg)
  }
  return (data as StudentInfo[]) ?? []
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

// 创建会话并绑定本班智能体（class_id/user_id 由服务端从 token 解析）。
export async function createConversation(agentId: number): Promise<TutorConversation> {
  const { status, data } = await request<TutorConversation | { error: string }>(
    '/api/tutor/conversations',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agentId }),
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
      const d = data as {
        citations?: Citation[]
        skills?: TutorSkillRef[]
        skillResources?: SkillResourceStatus[]
      }
      onEvent({
        type: 'meta',
        citations: Array.isArray(d.citations) ? d.citations : [],
        skills: Array.isArray(d.skills) ? d.skills : [],
        skillResources: Array.isArray(d.skillResources) ? d.skillResources : [],
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

// 保存本班助手提示词已随智能体配置退役，改用智能体接口维护提示词。

// ==================== 智能体 ====================

// 教师视图：本班智能体列表（含提示词与绑定清单）。
export async function fetchTeacherAgents(): Promise<TutorAgent[]> {
  const { status, data } = await request<TutorAgent[] | { error: string }>('/api/tutor/agents')
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '加载智能体列表失败'
    throw new ApiError(status, msg)
  }
  return (data as TutorAgent[]) ?? []
}

// 学生视图：本班智能体列表（仅 id/name/updatedAt）。
export async function fetchStudentAgents(): Promise<StudentAgent[]> {
  const { status, data } = await request<StudentAgent[] | { error: string }>('/api/tutor/agents')
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '加载智能体列表失败'
    throw new ApiError(status, msg)
  }
  return (data as StudentAgent[]) ?? []
}

// 新建本班智能体（仅教师；同班重名 400）。
export async function createAgent(name: string, systemPrompt: string): Promise<TutorAgent> {
  const { status, data } = await request<TutorAgent | { error: string }>('/api/tutor/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, systemPrompt }),
  })
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '新建智能体失败')
  }
  return data as TutorAgent
}

// 修改智能体名称与提示词（仅教师；同班重名 400，跨班或不存在 404）。
export async function updateAgent(
  id: number,
  name: string,
  systemPrompt: string,
): Promise<TutorAgent> {
  const { status, data } = await request<TutorAgent | { error: string }>(
    `/api/tutor/agents/${id}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, systemPrompt }),
    },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '保存智能体失败')
  }
  return data as TutorAgent
}

// 删除智能体（仅教师；仍被会话引用时服务端返回 409）。
export async function deleteAgent(id: number): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    `/api/tutor/agents/${id}`,
    { method: 'DELETE' },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '删除智能体失败')
  }
}

// 全量替换智能体绑定的技能（仅教师；含跨班 id 时服务端 404 且绑定不变）。
export async function replaceAgentSkills(id: number, skillIds: number[]): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    `/api/tutor/agents/${id}/skills`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ skillIds }),
    },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '保存技能绑定失败')
  }
}

// 全量替换智能体绑定的材料（仅教师；含跨班 id 时服务端 404 且绑定不变）。
export async function replaceAgentMaterials(id: number, handoutIds: number[]): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    `/api/tutor/agents/${id}/materials`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ handoutIds }),
    },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '保存材料绑定失败')
  }
}

// 启用/停用技能。
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

// 在线新建技能文档（名称 1–120 字，内容非空 ≤20000 字；默认停用）。
export async function createSkill(name: string, content: string): Promise<TutorSkill> {
  const { status, data } = await request<TutorSkill | { error: string }>(
    '/api/tutor/assistant/skills',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, content }),
    },
  )
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '新建技能失败')
  }
  return data as TutorSkill
}

// 上传技能文档（.md/.txt ≤64KB UTF-8；名称缺省取文件名去扩展名；默认停用）。
export async function uploadSkill(file: File, name?: string): Promise<TutorSkill> {
  const form = new FormData()
  form.append('file', file)
  if (name && name.trim()) {
    form.append('name', name.trim())
  }
  const { status, data } = await request<TutorSkill | { error: string }>(
    '/api/tutor/assistant/skills/upload',
    { method: 'POST', body: form },
  )
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '上传技能失败')
  }
  return data as TutorSkill
}

// 上传技能包（.zip ≤512KB，根目录必须含 SKILL.md；默认停用）。
export async function uploadSkillPackage(file: File): Promise<TutorSkill> {
  const form = new FormData()
  form.append('file', file)
  const { status, data } = await request<TutorSkill | { error: string }>(
    '/api/tutor/assistant/skills/package',
    { method: 'POST', body: form },
  )
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '上传技能包失败')
  }
  return data as TutorSkill
}

// 读取本班技能包详情（含工具引用与包内文件清单）；跨班或不存在 404。
export async function fetchSkillPackage(skillId: number): Promise<TutorSkill> {
  const { status, data } = await request<TutorSkill | { error: string }>(
    `/api/tutor/assistant/skills/${skillId}`,
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '加载技能包详情失败')
  }
  return data as TutorSkill
}

// 读取技能包内单个资源；路径限定在 references/assets/scripts 内。
export async function fetchSkillResource(skillId: number, path: string): Promise<Blob> {
  const token = getToken()
  const headers: HeadersInit = {}
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  const resp = await fetch(
    `/api/tutor/assistant/skills/${skillId}/resources?path=${encodeURIComponent(path)}`,
    { headers, cache: 'no-store' },
  )
  if (resp.status === 401) {
    clearToken()
    window.location.replace('/login')
    throw new ApiError(401, '未登录')
  }
  if (!resp.ok) throw new ApiError(resp.status, '加载技能资源失败')
  return resp.blob()
}

// 删除技能（内置技能服务端返回 409，前端在调用前已禁用）。
export async function deleteSkill(skillId: number): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    `/api/tutor/assistant/skills/${skillId}`,
    { method: 'DELETE' },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '删除技能失败')
  }
}

// ---------- 教师：MCP 服务器与调用记录 ----------

// 本班已登记的 MCP 服务器列表；响应只回 hasAuth 布尔，不含令牌。
export async function fetchMCPServers(): Promise<MCPServer[]> {
  const { status, data } = await request<MCPServer[] | { error: string }>('/api/tutor/mcp/servers')
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '加载 MCP 服务器失败'
    throw new ApiError(status, msg)
  }
  return (data as MCPServer[]) ?? []
}

// 登记一台本班 MCP 服务器（同班重名服务端返回 400）。
export async function createMCPServer(req: CreateMCPServerRequest): Promise<MCPServer> {
  const { status, data } = await request<MCPServer | { error: string }>('/api/tutor/mcp/servers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (status === 401) handle401()
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '登记 MCP 服务器失败')
  }
  return data as MCPServer
}

// 修改 MCP 服务器；authToken 缺省/null 表示保留原令牌，跨班/不存在返回 404。
export async function updateMCPServer(id: number, req: UpdateMCPServerRequest): Promise<MCPServer> {
  const { status, data } = await request<MCPServer | { error: string }>(
    `/api/tutor/mcp/servers/${id}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '修改 MCP 服务器失败')
  }
  return data as MCPServer
}

// 删除 MCP 服务器。
export async function deleteMCPServer(id: number): Promise<void> {
  const { status, data } = await request<{ ok: boolean } | { error: string }>(
    `/api/tutor/mcp/servers/${id}`,
    { method: 'DELETE' },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '删除 MCP 服务器失败')
  }
}

// 连接服务器完成握手与工具发现并缓存清单；失败服务端返回 502。
export async function connectMCPServer(id: number): Promise<MCPServer> {
  const { status, data } = await request<MCPServer | { error: string }>(
    `/api/tutor/mcp/servers/${id}/connect`,
    { method: 'POST' },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '连接 MCP 服务器失败')
  }
  return data as MCPServer
}

// 断开服务器并清空工具清单。
export async function disconnectMCPServer(id: number): Promise<MCPServer> {
  const { status, data } = await request<MCPServer | { error: string }>(
    `/api/tutor/mcp/servers/${id}/disconnect`,
    { method: 'POST' },
  )
  if (status === 401) handle401()
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '断开 MCP 服务器失败')
  }
  return data as MCPServer
}

// 本班最近调用记录（新→旧）；serverId 可为空（服务器已删）。
export async function fetchMCPCalls(): Promise<MCPCall[]> {
  const { status, data } = await request<MCPCall[] | { error: string }>('/api/tutor/mcp/calls')
  if (status === 401) handle401()
  if (status !== 200) {
    const msg = (data as { error?: string }).error ?? '加载调用记录失败'
    throw new ApiError(status, msg)
  }
  return (data as MCPCall[]) ?? []
}
