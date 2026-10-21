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
  // 引用列表序号（自 1 递增），与回答文本中的 [n] 标号一致。
  index: number
}

// 知识问答响应。
export interface QAResult {
  answer: string
  citations: Citation[]
}

// ---------- 解题助手 ----------

// 一次多轮解题会话，归属唯一学生；绑定一个本班智能体（会话内不切换）。
export interface TutorConversation {
  id: number
  agentId: number
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

// 技能来源：seed=内置种子、upload=教师上传、online=教师在线新建、package=技能包。
export type TutorSkillSource = 'seed' | 'upload' | 'online' | 'package'

// 技能包内落盘的一个文件条目（按顶层目录分类）。
export interface SkillPackageFile {
  path: string
  kind: 'references' | 'assets' | 'scripts' | string
  size: number
}

// 本轮技能资源加载状态（SSE meta 返回）。
export interface SkillResourceStatus {
  path: string
  loaded: boolean
  truncated?: boolean
  error?: string
}

// 技能正文「工具」行解析出的一条引用。
export interface ToolRef {
  raw: string
  name: string
  server?: string
  builtin: boolean
}

// 助手的一个技能：指令文档（名称+内容），教师可上传/新建/启停/删除。
export interface TutorSkill {
  id: number
  name: string
  description: string
  content: string
  source: TutorSkillSource
  builtin: boolean
  enabled: boolean
  // 技能包的唯一标识（仅 package 来源返回）。
  slug?: string
  // 运行时按需加载的资源白名单（仅 package 来源返回）。
  resources?: string[]
  // 包内实际文件清单（仅技能包详情接口返回）。
  files?: SkillPackageFile[]
  // 技能正文「工具」行解析出的引用；无引用时服务端可能返回 null。
  tools?: ToolRef[] | null
}

// 本班技能库视图（教师侧）；提示词随智能体配置，不在此返回。
export interface AssistantConfigData {
  id: number
  name: string
  skills: TutorSkill[]
}

// 教师视图的智能体：独立提示词 + 绑定的技能/材料 id 清单。
export interface TutorAgent {
  id: number
  name: string
  systemPrompt: string
  createdAt: string
  updatedAt: string
  skillIds: number[]
  handoutIds: number[]
}

// 学生视图的智能体：仅身份与更新时间，不含提示词与绑定。
export interface StudentAgent {
  id: number
  name: string
  updatedAt: string
}

// 流式提问过程中前端收到的事件。
export type TutorStreamEvent =
  | {
      type: 'meta'
      citations: Citation[]
      skills: TutorSkillRef[]
      skillResources?: SkillResourceStatus[]
    }
  | { type: 'delta'; text: string }
  | { type: 'done'; messageId: number }
  | { type: 'error'; error: string }

// ---------- MCP 服务器与工具 ----------

// MCP 服务器发现的一个远端工具（主机侧缓存清单里的一项）。
export interface MCPTool {
  name: string
  description: string
  inputSchema?: unknown
}

// 教师登记的一台本班 MCP 服务器；响应只回 hasAuth 布尔，永不含令牌。
export interface MCPServer {
  id: number
  name: string
  baseUrl: string
  hasAuth: boolean
  connected: boolean
  tools: MCPTool[]
  lastError?: string
}

// 登记 MCP 服务器的请求体（令牌仅在此请求体出现，不进入任何响应类型）。
export interface CreateMCPServerRequest {
  name: string
  baseUrl: string
  authToken: string
}

// 修改 MCP 服务器请求体；authToken 缺省/null 表示保留原令牌。
export interface UpdateMCPServerRequest {
  name: string
  baseUrl: string
  authToken?: string | null
}

// 一次远端工具调用的审计记录；serverId 可为空（服务器已删除）。
export interface MCPCall {
  id: number
  serverId?: number
  serverName: string
  toolRef: string
  status: 'success' | 'denied' | 'error'
  error?: string
  durationMs: number
  userId?: number
  username?: string
  conversationId?: number
  createdAt: string
}
