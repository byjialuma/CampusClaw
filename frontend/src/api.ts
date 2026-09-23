import type { Material, Me } from './types'

// 所有请求都携带同源会话 Cookie（HttpOnly，JS 不可读）。
async function request<T>(path: string, init: RequestInit = {}): Promise<{ status: number; data: T }> {
  const resp = await fetch(path, { credentials: 'include', ...init })
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

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export async function login(username: string, password: string): Promise<Me> {
  const { status, data } = await request<Me | { error: string }>('/api/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (status !== 200) {
    throw new ApiError(status, (data as { error: string }).error ?? '登录失败')
  }
  return data as Me
}

export async function logout(): Promise<void> {
  await request('/api/sessions', { method: 'DELETE' })
}

export async function fetchMe(): Promise<Me | null> {
  const { status, data } = await request<Me>('/api/me')
  if (status === 401) return null
  if (status !== 200) throw new ApiError(status, '获取当前用户失败')
  return data
}

export async function fetchMaterials(): Promise<Material[]> {
  const { status, data } = await request<{ materials: Material[] }>('/api/materials')
  if (status === 401) {
    window.location.replace('/login?next=' + encodeURIComponent(window.location.pathname))
    throw new ApiError(401, '未登录')
  }
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
  if (status !== 201) {
    throw new ApiError(status, (data as { error: string }).error ?? '上传失败')
  }
  return data as Material
}

export async function fetchContent(m: Material): Promise<Blob> {
  const resp = await fetch(`/api/materials/${m.id}/content`, { credentials: 'include' })
  if (resp.status === 401) {
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
  if (status !== 200) throw new ApiError(status, data.error ?? '创建下载凭证失败')
  return data.ticket
}

export type DownloadResult =
  | { ok: true; blob: Blob; fileName: string }
  | { ok: false; status: number }

// 下载页：凭一次性票据取文件，返回 Blob 与原始文件名。
export async function downloadByTicket(ticket: string): Promise<DownloadResult> {
  const resp = await fetch(`/api/download?ticket=${encodeURIComponent(ticket)}`, {
    credentials: 'include',
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
