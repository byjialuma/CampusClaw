## 1. 基础设施与配置

- [x] 1.1 在 `docker-compose.yml` 新增 `qdrant` 服务（固定版本镜像、接入 `ccnet`、**无 ports 发布**、挂 `qdrant-data` 卷、healthcheck 探测 6333），backend 增加 `depends_on: qdrant service_healthy` 与 `QDRANT_URL`/`EMBEDDING_*` 环境变量；verify：`docker compose config` 校验通过，且 qdrant 服务无 `ports:` 段
- [x] 1.2 `.env.example` 与本地 `.env` 增加 `EMBEDDING_PROVIDER`（openai|stub）、`EMBEDDING_BASE_URL`、`EMBEDDING_API_KEY`、`EMBEDDING_MODEL`、`EMBEDDING_DIM`、`QDRANT_URL`、`SEARCH_TOP_K`（智谱示例值，不含真实密钥）；verify：grep 仓库确认无真实云 API 密钥，缺失关键变量时后端启动报错（沿用 config 校验风格）
- [x] 1.3 扩展 `internal/config`：加载并校验上述变量（provider=openai 时 base_url/key/model/dim 必填，stub 时仅需 dim）；verify：容器内 `go test ./internal/config` 覆盖缺失变量 fatal、stub 免密钥两个分支

## 2. Schema 与状态表

- [x] 2.1 在 `deploy/mysql/01_schema.sql` 追加第九张表 `material_index`（handout_id PK/FK ON DELETE CASCADE、status enum、chunk_count、error、indexed_at、updated_at）；同步更新文件头表数量注释；verify：临时 MySQL 8 容器执行脚本无错，`SHOW TABLES` 含九表
- [x] 2.2 将同一份 SQL 复制到 `backend/internal/db/schema.sql` 供 `//go:embed`；verify：schema 一致性测试（逐字节比对）通过
- [x] 2.3 新增 `internal/store/index_state.go`：状态 upsert（pending/ready/failed）、按状态列出 handout_id、查单份状态；材料列表 SQL 增加 `LEFT JOIN material_index`，`model.Material` 与列表 JSON 增加 `indexStatus`（缺状态视为 pending 之外的空值处理需与前端约定为 "pending"）；verify：`go test` 覆盖状态流转与列表字段；现有 handlers 测试不回归

## 3. Embedding 客户端

- [x] 3.1 定义 `internal/embedding` 接口与配置驱动的工厂：`Embed(ctx, []string) ([][]float32, error)`，含批量切片（≤16/批）、20s 超时、返回条数/维度校验；verify：接口可被检索与索引包引用，`go vet` 通过
- [x] 3.2 实现 openai 兼容客户端（POST `{BASE_URL}/embeddings`、Bearer 鉴权头、`{model,input}` 请求体、解析 `data[].embedding`）；用 `httptest` 假服务验证鉴权头、批量拆分、维度不符报错；verify：`go test ./internal/embedding` 通过
- [x] 3.3 实现 stub 向量化器（文本哈希种子的确定性单位向量），注释标明仅限测试/离线；verify：同一文本两次向量相同、不同文本高概率不同，单测断言

## 4. Qdrant 客户端

- [x] 4.1 新增 `internal/vector`：net/http REST 客户端，实现集合幂等确保（PUT /collections/chunks，Cosine，维度取配置，class_id/document_id payload 索引）、健康探测 GET /healthz（2s 超时）；verify：`httptest` 假服务断言集合创建请求体含 distance/size/payload schema
- [x] 4.2 实现 upsert 点（确定性 UUID = SHA256(document_id:chunk_index) 格式化）、按 document_id 过滤删除、scroll 清点；verify：单测断言点 id 确定性（同输入重建 id 不变）与删除过滤体
- [x] 4.3 实现 Search(ctx, vector, classID, topK)：请求体**必须**含 `filter.must class_id=classID`，返回 payload+score；verify：单测对请求 JSON 做断言——不带 class_id 过滤的请求无法产生；响应映射为内部结果结构

## 5. 文档解析与切块

- [x] 5.1 新增 `internal/ingest`：定义 Chunk 与 Locator 类型、500 rune/80 overlap 常量；实现 TXT 切块（locator 记录 1 基起止行号，区间覆盖正文）；verify：单测对多行样例验证行号边界、重叠、空白块丢弃
- [x] 5.2 实现 Markdown 切块：ATX 标题栈生成章节路径（如「第一章 > 集合的概念」），按章节切、长章节开窗且继承路径；verify：单测覆盖多级标题、无标题文档、超长章节
- [x] 5.3 引入 `github.com/ledongthuc/pdf`，实现 PDF 逐页文本提取（1 基页码 locator，超长页开窗共享页码），整份无文本返回明确错误；verify：用现有种子 PDF 与一份多页文本 PDF 单测验证页码归属，构造扫描件/空 PDF 断言失败原因
- [x] 5.4 实现按文件类型分派的 Parse(name, data) 入口与端到端管线函数（解析→切块，不含向量化）；verify：非法/空输入返回错误，三类文件切块总数与 locator 非空

## 6. 索引编排与生命周期

- [x] 6.1 实现 `internal/ingest`（或 `internal/indexer`）作业服务：读 handouts 文件 → Parse → Embed 批量 → 删旧点 → upsert → 置 ready+chunk_count；失败置 failed+截断错误；全程不动 handouts/材料文件；verify：用 fake embedder 与 fake Qdrant（httptest）的单测覆盖成功、embedding 失败、qdrant 失败三条路径与状态落库
- [x] 6.2 实现进程内单 worker 队列（channel+goroutine）：上传 handler 在 SaveAndCreate 成功后写 pending 并入队，201 响应不等待索引；verify：handler 测试断言上传后状态为 pending 且作业被入队（注入同步执行的 fake worker）
- [x] 6.3 启动流程接入：main.go 在 schema/seed 后启动「Qdrant 就绪轮询 → 确保集合 → 回填 pending/failed（含种子材料）」；ready 不重复索引；回填在单 worker 上顺序执行；verify：空 Qdrant + 已有 MySQL 启动后种子材料全部 ready（集成/E2E 验证），二次启动 chunk 数不增长
- [x] 6.4 删除/重建幂等：重索引同文档后 Qdrant 中该 document_id 点数等于最新切块数，无残留；verify：单测/脚本对同文档连续索引两次并 scroll 清点

## 7. 检索 API

- [x] 7.1 新增 `GET /api/search`（requireAuth，教师学生可用）：trim 后空查询或 >500 rune 返回 400；class_id 只取会话，不读取任何请求班级参数；verify：handler 单测（fake searcher）覆盖 401、400、参数 class_id 被忽略
- [x] 7.2 检索编排：Embed 查询 → Qdrant Search（带 class_id 过滤，topK 取配置）→ 返回 `{results:[{documentId,chunkId,fileName,fileType,snippet,score,locator}]}`，无命中返回空数组 200；verify：单测断言响应每条结果含文件+locator，且不会出现无来源条目
- [x] 7.3 错误映射：Qdrant/Embedding 故障返回 503 固定中文文案，不回显内部 host/端口；verify：单测模拟下游故障断言状态码与响应体不含 `http://`/端口
- [x] 7.4 扩展 `/health`：MySQL 与 Qdrant 双探测，任一不可用返回 503；verify：健康检查单测覆盖双正常/MySQL 挂/Qdrant 挂三分支

## 8. 前端检索体验

- [x] 8.1 `api.ts` 增加 `searchMaterials(q)`（GET /api/search，credentials include，401 沿用跳登录）与类型 SearchResult/Locator；verify：`npm run build`（tsc 严格模式）零错误
- [x] 8.2 左栏顶部新增「知识库检索」卡片（输入框+提交按钮，学生可见，无任何班级选择控件）；verify：浏览器检查学生/教师视图均有入口
- [x] 8.3 右栏新增检索结果视图：分片卡片含正文、文件名、相关度、页码/章节/行号徽标；空结果显示空态文案；与材料预览视图可切换；verify：浏览器中用可命中的关键词检索，卡片与徽标正确展示
- [x] 8.4 点击引用徽标按 documentId 在本班列表定位并打开既有 Preview（PDF 卡片显示「第 N 页」提示）；verify：浏览器点击跳转预览且对列表中不存在的外班 documentId 不发请求/不报错
- [x] 8.5 503/400 错误的内联提示与北大红主题样式，不改动顶栏/上传/列表/下载既有组件；verify：浏览器模拟空查询与停 Qdrant 场景显示友好提示，构建零错误

## 9. 端到端与隔离验证

- [x] 9.1 新增 `deploy/tests/e2e-search-matrix.sh`：backend 以 `EMBEDDING_PROVIDER=stub` 连真实 Qdrant 启动；断言未登录 401、空查询 400、两班回填 ready、A/B 同一查询结果 document_id 集合互不相交、伪造 classId/class_id 参数无效、宿主 6333/6334 端口不可达；verify：容器内运行脚本全部断言通过
- [x] 9.2 故障演练：`docker compose stop qdrant` 后 `/health` 返回 5xx、`/api/search` 返回 5xx 且不含内部地址；`start qdrant` 后两者恢复 200；verify：手工 curl 记录状态码
- [x] 9.3 全新启动验证：`docker compose down -v && up --build -d`（stub provider）后无需人工干预，两班种子材料自动 ready 且可检索；再跑既有 `e2e-auth-matrix.sh` 确认 28 项鉴权矩阵零回归；verify：两套 E2E 脚本均 PASS
- [x] 9.4 回归核对：上传/列表/预览/下载票据/退出全链路浏览器走查一遍，确认新卡片与新表不改变既有行为；verify：浏览器走查记录与旧 E2E 全绿

## 10. 变更校验与交付

- [x] 10.1 运行 `openspec validate add-class-scoped-knowledge-search --strict`；verify：退出码 0，无格式/需求/场景错误
- [x] 10.2 对照 spec.md 六组需求逐条验收走查（重点：篡改 class_id 无效、跨班零召回、每条结果有 Citation、索引失败不破坏上传、Qdrant 不发布端口）；verify：每条场景记录实际结果并全部通过
