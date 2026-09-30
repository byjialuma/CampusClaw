import type { Material, Me, SearchResult, QAResult } from './types'

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
