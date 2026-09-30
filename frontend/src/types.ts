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

// 知识问答引用来源。
export interface Citation {
  documentId: number
  fileName: string
  fileType: 'txt' | 'md' | 'pdf'
  snippet: string
  locator: Locator
  score: number
}

// 知识问答响应。
export interface QAResult {
  answer: string
  citations: Citation[]
}

// ---------- 解题助手 ----------

// 一次多轮解题会话，归属唯一学生。
export interface TutorConversation {
  id: number
  title: string
  createdAt?: string
}

// 会话中的一轮消息；assistant 轮带引用。
export interface TutorMessage {
  id: number
  conversationId?: number
  role: 'user' | 'assistant'
  content: string
  citations?: Citation[]
  createdAt?: string
}

// 助手的一个技能（本课仅内置「解题引导」）。
export interface TutorSkill {
  id: number
  name: string
  description: string
  enabled: boolean
}

// 本班助手配置（教师侧）。
export interface AssistantConfigData {
  id: number
  name: string
  systemPrompt: string
  skills: TutorSkill[]
}

// 流式提问过程中前端收到的事件。
export type TutorStreamEvent =
  | { type: 'meta'; citations: Citation[]; skillEnabled: boolean }
  | { type: 'delta'; text: string }
  | { type: 'done'; messageId: number }
  | { type: 'error'; error: string }
