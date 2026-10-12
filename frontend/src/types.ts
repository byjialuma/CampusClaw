export interface Me {
  id: number
  username: string
  displayName: string
  role: 'teacher' | 'student'
  classId: number
  className: string
}

// 教师查看本班学生名单时的一行数据。
export interface StudentInfo {
  id: number
  username: string
  displayName: string
  createdAt: string
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

// 某轮回答启用技能的快照（仅 id+name，技能删除后历史仍可展示）。
export interface TutorSkillRef {
  id: number
  name: string
}

// 会话中的一轮消息；assistant 轮带引用与技能快照。
export interface TutorMessage {
  id: number
  conversationId?: number
  role: 'user' | 'assistant'
  content: string
  citations?: Citation[]
  skillsUsed?: TutorSkillRef[]
  createdAt?: string
}

// 技能来源：seed=内置种子、upload=教师上传、online=教师在线新建。
export type TutorSkillSource = 'seed' | 'upload' | 'online'

// 助手的一个技能：指令文档（名称+内容），教师可上传/新建/启停/删除。
export interface TutorSkill {
  id: number
  name: string
  description: string
  content: string
  source: TutorSkillSource
  builtin: boolean
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
  | { type: 'meta'; citations: Citation[]; skills: TutorSkillRef[] }
  | { type: 'delta'; text: string }
  | { type: 'done'; messageId: number }
  | { type: 'error'; error: string }
