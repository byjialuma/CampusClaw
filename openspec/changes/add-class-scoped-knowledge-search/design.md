## Context

CampusClaw 现运行链路为 浏览器 → Nginx(:8080 唯一入口) → Go(net/http，禁 Web 框架) → MySQL 8.0；材料（handouts）已带 `class_id` 服务端强制边界，上传落盘 `/app/data/materials/<yyyymm>/<随机名>.<ext>`，登录态为服务端 Session + `cc_session` Cookie。本期在不改动既有认证/上传/下载契约的前提下，叠加语义检索：新增 Qdrant 容器、Embedding 云调用、文档解析切块与溯源元数据。用户已确认：向量库选 Qdrant；Embedding 用国内云 OpenAI 兼容接口；本期只检索不生成；溯源粒度为 PDF 页码 / Markdown 章节 / TXT 行号。

## Goals / Non-Goals

**Goals:**

- 检索在向量库层面就带 `class_id` 强制过滤，形成「服务端会话 → 查询过滤」双层班级隔离，且可被测试断言。
- 索引全自动化：上传后异步索引、重启回填、失败可观测可重试，不影响既有上传 201 契约。
- 每条命中可定位到文件与位置（页码/章节/行号）。
- 新增依赖最小化：Qdrant 单容器；Go 侧仅用 net/http 调其 REST API 与 Embedding API；PDF 用一个纯 Go 文本提取库。
- E2E 不依赖外网与真实云密钥。

**Non-Goals:**

- LLM 生成式问答（RAG 的"生成"环节）、Prompt 编排、回答内联引用——下一期变更。
- OCR / 扫描件图片理解；docx/pptx 等新文件类型（仍只支持 txt/md/pdf）。
- 跨班/全校联合检索、教师检索审计、检索限流计费。
- Qdrant 集群/高可用、向量数据异地备份（单节点 + 持久化卷即可）。

## Decisions

### 1. Qdrant 单节点 + REST，payload 强过滤 class_id

- compose 新增 `qdrant` 服务（镜像 `qdrant/qdrant` 固定版本），接入 `ccnet`，**无 `ports:`**，挂 `qdrant-data:/qdrant/storage`，配 healthcheck；backend `depends_on: service_healthy` 并在代码中再做就绪轮询（与 MySQL 等待同一模式）。
- 单 collection `chunks`，距离 `Cosine`，向量维度取自配置 `EMBEDDING_DIM`。
- 点结构：
  - `id`：由 `SHA256("v1:"+document_id+":"+chunk_index)` 前 16 字节格式化为确定性 UUID（重索引 upsert 幂等）。
  - `vector`：float32 数组。
  - `payload`：`class_id`(int64)、`document_id`(int64)、`chunk_index`、`file_name`、`file_type`、`text`、`locator`（`{kind:"page", page}` / `{kind:"heading", path}` / `{kind:"lines", start, end}`）。
  - 对 `class_id`、`document_id` 建 payload 索引（keyword）。
- 检索请求**必须**携带 `filter: { must: [ { key: "class_id", match: { value: <会话class_id> } } ] }`，`with_payload=true`，`limit=topK`。这是"不同班级 chunk 不混合召回"的硬保证；单测对发往 Qdrant 的请求体做 JSON 断言。
- Go 客户端（`internal/vector`）只用 net/http 调 REST：`PUT /collections/chunks`（幂等确保集合）、`POST /points/upsert`、`POST /points/scroll`（启动核对）、`POST /points/delete`（按 `document_id` 过滤删除）、`POST /points/query`（或 `/search`，按镜像版本取稳定端点）、`GET /healthz`。
- 备选：Go 官方 gRPC 客户端——被否，引入重型依赖且偏离"标准库 net/http"约定；MySQL 存 BLOB + Go 内存余弦——被否（用户已选 Qdrant，且大班规模下重复全量扫描无扩展空间）。

### 2. Embedding：可配置 OpenAI 兼容客户端 + 测试用 stub

- `internal/embedding` 定义接口 `Embed(ctx, texts []string) ([][]float32, error)`，两个实现：
  - `openai`（默认）：`POST {EMBEDDING_BASE_URL}/embeddings`，头 `Authorization: Bearer ${EMBEDDING_API_KEY}`，体 `{"model": EMBEDDING_MODEL, "input": [...]}`，解析 `data[].embedding`；批量切片（每批 ≤16 条）、20s 超时、对维度与返回条数做校验。国内主流厂商（智谱/通义/火山等）均兼容该形态，换厂商只改环境变量。
  - `stub`：以文本哈希为种子生成确定性单位向量。仅供 E2E/离线/无密钥环境，代码注释标明禁止用于生产语义检索。
- 新增配置（`internal/config`，缺失即 fatal，延续现有风格）：`EMBEDDING_PROVIDER`(openai|stub)、`EMBEDDING_BASE_URL`、`EMBEDDING_API_KEY`、`EMBEDDING_MODEL`、`EMBEDDING_DIM`、`QDRANT_URL`、`SEARCH_TOP_K`(默认 5，上限 10)。`.env.example` 给智谱 embedding-3（2048 维）示例但不含真实密钥。

### 3. 解析与切块（`internal/ingest`）

- 统一输出 `Chunk{Index, Text, Locator}`；目标块约 500 个 rune、相邻块重叠约 80 rune（常量，后续可调）；丢弃纯空白块。
- **TXT**：按行读取，滑动窗口打包；`locator={kind:"lines", start, end}`（1 基行号，覆盖块内文本）。
- **Markdown**：扫描 ATX 标题（`#`~`######`）维护章节栈，得到「一级 > 二级」章节路径；按章节切，超长章节在章节内再开窗（窗口继承该章节路径）；`locator={kind:"heading", path}`。
- **PDF**：引入 `github.com/ledongthuc/pdf`（纯 Go、无 CGO），逐页 `GetPlainText`；以页为最小归属单位，单页超长再开窗且各窗口共用该页码；`locator={kind:"page", page}`（1 基）。整份文档提取不出任何文本（典型扫描件）→ 返回明确索引错误。
- 一个新依赖（PDF 库）不属于 Web 框架，与现有 go-sql-driver/x-crypto 的依赖尺度一致。

### 4. 索引状态机与异步队列

- MySQL 新增 `material_index` 表（不 ALTER 既有 handouts）：
  `handout_id PK、status ENUM('pending','ready','failed')、chunk_count INT、error VARCHAR(512)、indexed_at DATETIME(3)、updated_at`，外键 `ON DELETE CASCADE`。
- 材料列表 SQL 增加 `LEFT JOIN material_index`，JSON 增加 `indexStatus` 字段（对旧契约仅为增量字段）。
- 后端进程内**单工作线程队列**（buffered channel + 1 goroutine）：
  - 教师上传 handler 在 `SaveAndCreate` 成功后写 `pending` 并入队（响应仍是 201，不等待索引）。
  - 种子播种后为新登记材料入队；启动时把 `pending/failed` 全部入队（回填 + 重试），`ready` 不动。
- 作业流程：置 `running` 不需要额外枚举（用 `pending` 表示）→ 读文件 → 解析切块 → 批量 Embed → `DELETE` 该 document_id 旧点（保证切块数变化时无残留）→ `upsert` 确定性 ID 新点 → 置 `ready`+chunk_count；任何环节失败置 `failed`+截断错误信息，不删文件、不删 handouts。

### 5. 检索接口

- `GET /api/search?q=<文本>`，挂 `requireAuth`（教师/学生均可）。
- 校验：trim 后为空 → 400；超过 500 rune → 400。
- 流程：取会话 `class_id`（请求中即使带 class_id 参数也不读取）→ Embed 查询 → Qdrant 带 class_id filter 检索 topK → 映射为：
  `{ results: [ { documentId, chunkId, fileName, fileType, snippet, score, locator } ] }`。
- 错误映射：Qdrant 不可用/超时 → 503「知识库检索暂不可用」；Embedding 失败 → 503 同文案（不区分内部依赖、不回显内部地址）。
- 不设默认分数阈值（避免冷启动零结果），`SEARCH_SCORE_MIN` 预留环境变量默认 0。

### 6. /health 与前端

- `/health` 增加 Qdrant `/healthz` 探测（2s 超时）：MySQL 与 Qdrant 同时正常才 200；Embedding 是外部依赖，不纳入健康判定。
- 前端在左栏顶部新增「知识库检索」卡片（输入框+按钮，学生同样可见）；右栏在「材料预览 / 检索结果」两种视图间切换：结果为分片卡片（正文 + 文件名徽标 + 页码/章节/行号），点击徽标按 `documentId` 在本班列表中定位并打开既有 Preview；无结果显示空态文案。不新增路由、不改动下载票据流程。

### 7. 测试策略

- 单测：切块器三类文件定位准确性；embedding 客户端用 `httptest` 假服务（批处理、鉴权头、维度校验）；Qdrant 客户端用 `httptest` 断言 search 请求体必含 `class_id` must 过滤；检索 handler 用假 searcher 验证「参数 class_id 被忽略、只按会话班级」与 401/400/503 分支。
- schema 一致性测试延续：`01_schema.sql` 与 embed 的 `schema.sql` 逐字节相同。
- E2E（`deploy/tests` 新增 `e2e-search-matrix.sh`，backend 以 `EMBEDDING_PROVIDER=stub` 运行、连真实 Qdrant）：两班种子回填后，A/B 同查询结果互不串班；伪造 class_id 参数无效；未登录 401；空查询 400；停 Qdrant → /health 非 200 且 search 5xx，恢复后 200；宿主 6333/6334 端口不可达。

## Risks / Trade-offs

- [PDF 文本提取质量受限（多栏/公式/扫描件）] → 仅承诺文本型 PDF 按页提取；无文本页/扫描件置 `failed` 并给明确原因；OCR 列入 Non-Goal。
- [Embedding API 抖动/限流/费用] → 索引异步 + 批量 + 超时重试一次；查询路径单次调用、快速失败给 503；密钥仅服务端持有。
- [换 Embedding 模型导致维度/语义空间变化] → 维度随配置建集合；运维须知：换模型需删除 `chunks` 集合并重启（启动回填重建全部索引），写入 README/部署注释（实现时以任务形式跟踪，不新建文档产物则在 design 留痕）。
- [Qdrant 卷丢失后启动大批回填] → 单线程顺序作业 + 每文档状态落库，中断重启可续跑；班级规模（每班数十至数百文档）下可接受。
- [stub 向量无语义区分力] → 仅供测试，E2E 查询设计为对文本中独有词做断言（stub 对相同/近似输入产生相近向量），并在配置注释明确禁止生产启用。
- [新增一个有状态容器] → 用户已确认接受；单节点 + 命名卷持久化，与 MySQL 同属 compose 生命周期管理。

## Migration Plan

1. 发布含新表的 `01_schema.sql`（`CREATE TABLE IF NOT EXISTS`，对已存在卷在线追加，不影响八张旧表）。
2. compose 新增 qdrant 服务与卷；backend 增加环境变量；`up` 时 backend 等 MySQL 与 Qdrant 均就绪后建集合、启动回填。
3. 回填期间材料状态从 pending → ready 逐步可检索；检索接口立即可用（仅召回 ready 分片）。
4. 回滚：移除/停用新前端卡片与新路由即隐藏功能；Qdrant 容器与 `material_index` 表为纯增量，删除后旧功能（登录/上传/列表/预览/下载）不受影响；handouts 与材料文件结构零改动。

## Open Questions

- 检索 topK（默认 5）、块大小（500 rune）、重叠（80 rune）取经验默认值，实现后如效果不佳可仅调常量，不影响规格。
- 具体云厂商最终选型：以环境变量交付，`.env.example` 给智谱示例；如实施期确认其他厂商端点形态有差异，仅影响 embedding 客户端的请求映射，不影响本设计与规格。
