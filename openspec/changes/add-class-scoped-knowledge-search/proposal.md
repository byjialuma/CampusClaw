## Why

班级知识库目前只能按列表浏览，材料增多后学生难以快速定位"集合的运算规则在讲义哪里"这类问题；且多班共用一套系统，检索能力必须延续既有的班级数据边界——学生只能搜到本班材料，回答/结果必须能指回具体文档与位置，否则不可用于教学场景。

## What Changes

- 新增**班级限定的语义检索**：用户用自然语言提问，后端对本班已入库材料做向量相似度检索，返回最相关的文本片段。
- 检索身份与过滤全部在服务端完成：`class_id` 只取自登录会话，请求中任何 `class_id` 参数一律忽略；向量检索以 `class_id` 为强制过滤条件，不同班级的 chunk 不可能混合召回。
- 新增**文档索引管线**：材料文件经「解析 → Chunk 切分 → Embedding 向量化 → 写入向量库」；教师新上传材料在现有上传成功后自动异步入库，服务重启时回填种子材料与未完成索引。
- 新增 **Qdrant 向量数据库**容器（仅 Compose 内部网络可达，不发布宿主端口）；Go 后端通过其 REST API（net/http，不引入 Web 框架/gRPC 客户端）访问。
- Embedding 使用**国内云厂商 OpenAI 兼容接口**（可配置 base_url/model/维度，密钥走环境变量）；同时提供仅供测试/离线环境使用的 stub 向量化器，E2E 不依赖外网。
- 每个 chunk 保存溯源 metadata：`document_id`、`class_id`、`chunk_id`、原始文件名、文件类型，以及按文件类型区分的位置：**PDF 页码、Markdown 章节路径、TXT 行号区间**。
- 检索接口对每条命中返回片段文本与 **Citation 来源**（文件名 + 页码/章节/行号）；不存在无来源的结果。
- 前端在现有布局上新增检索入口与结果区：展示片段列表与引用徽标，点击引用可打开对应材料预览；不改动上传、列表、预览、下载等既有功能。
- 新增跨班权限测试（含篡改请求参数、向量库过滤断言）与端到端矩阵。

**范围裁剪（已与用户确认）**：本期**只检索、不生成**——不接入大模型生成式回答，接口只返回相关片段与其来源；生成式问答（模型基于片段作答并带引用）留待后续变更。

## Capabilities

### New Capabilities

（无）

### Modified Capabilities

- `classroom-knowledge-base`: 在现有知识库能力上叠加「班级限定语义检索、文档分块向量索引、结果来源溯源、检索结果前端展示」四组新要求；delta 全部以 ADDED Requirements 形式追加，不修改既有认证/上传/下载要求的行为契约。

## Impact

- **部署**：`docker-compose.yml` 新增 `qdrant` 服务（内部网络、持久化卷、healthcheck，backend 依赖其健康）；`.env.example` 新增 `EMBEDDING_PROVIDER/BASE_URL/API_KEY/MODEL/DIM`、`QDRANT_URL`、`SEARCH_TOP_K` 等配置。
- **后端（Go 标准库）**：新增 `internal/embedding`（OpenAI 兼容客户端 + stub）、`internal/ingest`（txt/md/pdf 解析与切块，PDF 纯 Go 库按页提取文本）、`internal/vector`（Qdrant REST 客户端）、`internal/search`（检索编排）包；`internal/config`、`cmd/server/main.go`、`internal/server` 路由与 MySQL schema 新增一张索引状态表；`/health` 增加 Qdrant 依赖探测。
- **数据库**：新增材料索引状态表（记录每份 handout 的 pending/ready/failed、chunk 数、错误信息、索引时间）；chunk 正文与向量存 Qdrant，不存 MySQL。
- **前端（React）**：新增检索输入与结果组件，复用现有材料预览；API 模块新增 search 调用。
- **测试**：解析/切块、embedding 客户端（httptest 假服务）、Qdrant 客户端（断言检索请求必带 class_id 过滤）、handler 跨班隔离单测；E2E 脚本新增语义检索与跨班召回矩阵（stub 向量化器 + 真实 Qdrant 容器）。
- **不受影响**：登录认证、Session/Cookie、角色 403、材料上传入库与下载票据、六类核心表结构、Nginx 唯一入口。
