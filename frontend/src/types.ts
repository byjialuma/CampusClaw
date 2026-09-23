export interface Me {
  id: number
  username: string
  displayName: string
  role: 'teacher' | 'student'
  classId: number
  className: string
}

export interface Material {
  id: number
  name: string
  fileType: 'txt' | 'md' | 'pdf'
  sizeBytes: number
  uploaderName: string
  createdAt: string
  indexStatus: 'pending' | 'ready' | 'failed'
}

// 分片在原文档中的溯源位置，按 kind 使用不同字段。
export interface Locator {
  kind: 'page' | 'heading' | 'lines'
  page?: number
  path?: string
  startLine?: number
  endLine?: number
}

export interface SearchResult {
  documentId: number
  chunkId: string
  fileName: string
  fileType: 'txt' | 'md' | 'pdf'
  snippet: string
  score: number
  locator: Locator
}
