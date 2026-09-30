## Why

当前系统已支持语义检索（返回相关片段），但用户需要进一步的能力：输入一个问题，系统直接给出基于本班材料生成的自然语言回答，并附带可溯源的引用来源。这是从「人找材料」到「材料找人」的体验升级。

## What Changes

- 新增**知识问答接口**：用户提交自然语言问题，后端执行「向量化 → 向量库召回 → 大模型生成回答」两步 LLM 调用，返回结构化回答与来源列表。
- 第一次 LLM 调用：使用现有 Embedding 服务将问题向量化，在 Qdrant 中按 `class_id` 强制过滤召回 Top-K 片段。
- 第二次 LLM 调用：使用 Chat Completions 接口，将问题与召回片段作为上下文，要求模型基于片段内容回答；若片段不足以回答，模型必须明确说明「根据已有材料无法回答」。
- 回答结果 MUST 包含来源引用：每条引用包含 `document_id`、文件名、文件类型、精确位置（PDF 页码 / Markdown 章节路径 / TXT 行号区间）与相关度评分。
- 前端在现有检索面板旁新增「知识问答」入口：输入问题后展示回答文本与来源卡片，点击来源卡片可打开对应材料预览。
- 新增 `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL` 环境变量配置 Chat Completions 端点；Embedding 配置复用现有变量。
- 班级数据隔离延续：问答召回的片段 MUST 全部来自当前用户所属班级，生成回答时 MUST NOT 使用外班材料。

## Capabilities

### New Capabilities

（无）

### Modified Capabilities

- `classroom-knowledge-base`: 在现有知识库能力上叠加「知识问答」新要求；delta 以 ADDED Requirements 形式追加，不修改既有认证/检索/上传/下载要求的行为契约。

## Impact

- **配置**：`.env.example` 新增 `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL`；`docker-compose.yml` backend 环境变量透传。
- **后端（Go 标准库）**：新增 `internal/llm`（Chat Completions 客户端）、`internal/qa`（问答编排）包；`internal/server` 新增 `POST /api/qa` 路由；`internal/config` 新增 LLM 配置加载与校验。
- **数据库**：无新增表；问答记录可暂不持久化（后续如需历史记录再扩展）。
- **前端（React）**：新增问答输入组件与回答展示组件，复用现有材料预览与来源徽标；API 模块新增 `askQuestion` 调用。
- **测试**：`internal/llm` 客户端单测（httptest 假服务）、`internal/qa` 编排单测（mock embedder/vector/llm）、handler 跨班隔离单测；E2E 脚本新增问答矩阵（真实 LLM 网关或 mock）。
- **不受影响**：JWT 认证、班级数据隔离、材料上传/下载/预览、语义检索接口、Nginx 反向代理、Qdrant 内部网络约束。
