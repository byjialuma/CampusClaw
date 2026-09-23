## Context

全新空仓库，无存量代码与数据，需要从零搭建一条可在本机用 `docker compose up` 完整跑通的课程演示链路：浏览器 → Nginx → Go → MySQL 8.0。约束（来自 proposal）：前端 React 18 + TypeScript + Vite；后端仅用 Go 标准库 `net/http`，**不引入 Web 框架**；会话使用服务端 Session + HttpOnly Cookie，**不使用 JWT**；只有 Nginx 对宿主机发布端口。spec 中的行为契约（401/403、班级过滤、下载页刷新跳登录等）见 [spec.md](specs/classroom-knowledge-base/spec.md)，本文只回答“如何实现”。

## Goals / Non-Goals

**Goals:**

- 鉴权与授权全部在服务端闭环：前端仅做体验层展示（学生也保留上传入口），任何越权请求在 Go 侧被拒。
- 班级隔离下沉到每一条数据查询，无法被请求参数绕过。
- 上传/查看/下载路径可验收：文件名保真、类型受限、文件不落 Web 静态目录、下载必过鉴权。
- 种子账号口令完全由环境变量控制，镜像与代码中零密码。
- 单机一键启动，重启幂等（重复启动不产生重复种子数据）。

**Non-Goals:**

- 不做刷新令牌、多设备会话管理、登录限速/验证码（保留统一错误文案，不做账号锁定）。
- 不做病毒查杀、Office 宏分析等深度内容扫描；仅做扩展名 + 文件头魔数校验。
- 不做 HTTPS/TLS、生产级镜像加固与日志审计。
- 不做文件秒传、断点续传、在线编辑。

## Decisions

### 1. 总体结构与部署拓扑

```
CampusClaw/
├── docker-compose.yml          # nginx / backend / mysql 三服务
├── .env.example                # 种子账号口令、DB 连接等环境变量样例
├── frontend/                   # React18 + TS + Vite，多阶段构建产出静态资源
├── backend/                    # Go module；cmd/server + internal/*
└── deploy/
    ├── nginx/nginx.conf        # 静态托管 + /api、/health 反代
    ├── mysql/01_schema.sql     # 六表 + sessions/download_tickets 的 DDL
    └── seed/materials/         # 两班预置讲义文件（txt/md/pdf）
```

- Compose 自定义 bridge 网络 `ccnet`；**只有 nginx 发布宿主机端口**（如 `8080:80`）。backend 与 mysql 均不写 `ports:`，外部无法直连。
- Nginx：`/api/` 与 `/health` 反代 `backend:8080`；其余路径 `try_files ... /index.html` 交给 SPA 路由。
- 后端进程监听容器内 8080，仅靠标准库 `net/http` 挂一棵路由树（`http.ServeMux`），中间件以函数包装方式实现（logging → authz → handler）。

备选：把 Go 端口也映射到宿主以便调试——拒绝，验收点明确要求后端不暴露。

### 2. 会话与密码

- 密码哈希：`golang.org/x/crypto/bcrypt`（它不是 Web 框架，不违反“只用 net/http”的约束；备选标准库 `crypto/pbkdf2`（Go 1.24+）可平替，接口隔离在 `internal/auth` 内）。
- 登录成功后生成会话标识：`crypto/rand` 产生 32 字节随机数 → hex 编码，写入 `sessions` 表（`id CHAR(64), user_id, created_at, expires_at, INDEX(user_id)`），并通过响应头下发：
  `Set-Cookie: cc_session=<token>; HttpOnly; Path=/; SameSite=Lax; Max-Age=28800`
  - 不用 `Secure`：课程环境为明文 HTTP；生产上由网关补 TLS 后再加。
  - `SameSite=Lax` 而非 `Strict`：允许下载页顶层导航从新标签打开，同时阻止跨站 POST 携带 Cookie，作为无独立 CSRF token 方案下的主要 CSRF 缓解。
- `requireAuth` 中间件：读 Cookie → 查 `sessions`（未过期）→ 载入用户（id、role、class_id、display_name）放入请求上下文；缺失/过期一律 401 JSON `{error:"unauthorized"}`。
- 登录失败文案统一为「用户名或密码错误」，用户不存在与密码错误走同一路径、同一响应，避免账号枚举。
- 退出 = `DELETE /api/sessions`：删除服务端行 + 过期化 Cookie；旧 token 再用因行已删除自然 401。
- 会话有效期 8 小时，不做滑动续期。不使用 JWT 的理由：可即时吊销（退出即失效）、载荷不暴露、班级/角色变更随下次请求生效。

### 3. 角色与班级隔离（服务端强制）

- 两个独立中间件，判定数据全部来自服务端会话，**绝不信任请求体/查询参数中的 class_id、user_id**：
  - `requireTeacher`：会话用户 role != teacher → 403 `{error:"学生无权限上传文件"}`（学生上传与越权共用 403，学生场景用此确定文案）。
  - 班级过滤：handlers 拿到会话的 `class_id` 后作为 SQL 绑定参数。
- 列表：`SELECT ... FROM handouts WHERE class_id = ? ORDER BY created_at DESC`（参数 = 会话班级）。
- 详情/内容/下载：先按主键取记录，再比对 `record.class_id == session.class_id`：不等 → **403**；记录不存在 → 404。选择 403 而非 404 是为了让“跨班被拒”在验收中可观察（代价：可探测外班材料是否存在，教学场景可接受）。
- 前端不承担安全职责：学生也渲染上传区；列表即使拿到多余数据也不可能，因为服务端根本不返回。

### 4. 上传数据流

`POST /api/materials`（multipart/form-data，字段 `file`；`requireAuth` + `requireTeacher`）：

1. `r.Body = http.MaxBytesReader(w, r.Body, 32<<20)`，超限返回 400。
2. 取 `filename` 并小写化扩展名，白名单仅 `txt|md|pdf`；不在白名单 → 400 `{error:"仅允许 txt、md、pdf 文件"}`。
3. 读前 5 字节做魔数复核：pdf 必须以 `%PDF-` 开头；txt/md 必须可按 UTF-8 文本解码且不含 NUL 字节。扩展名与内容不一致 → 400。
4. 原始文件名仅作为展示名入库（`original_name`），**不参与磁盘路径**。存储键由服务端生成：`/app/data/materials/<yyyymm>/<random32>.<ext>`，目录不被 Nginx 托管、也不挂静态 handler。
5. 单事务：落盘成功后写 `handouts` 行（`class_id` = 会话班级，`uploader_user_id` = 会话用户，`file_type`、`size_bytes`、时间戳）；写库失败则删除已落盘文件。
6. 返回 201 与新材料记录 JSON；前端随即刷新列表（满足“上传后列表可查”）。

### 5. 内容预览与“新页面 + 一次性票据”下载

- 内容：`GET /api/materials/{id}/content`（同班）→ 以 `Content-Type`（text/plain; charset=utf-8、text/markdown、application/pdf）与 `X-File-Name` 头回送字节流，前端以 blob 处理：txt/md 解码为文本（md 用 `marked` 渲染后放入右侧区域），pdf 用 blob URL 注入 `<iframe>` 预览。
- 下载需求要求“新页面打开，且该页面刷新后无法认证身份并跳转登录”。若新页面直接带会话 Cookie 请求，刷新仍会成功，因此引入**一次性下载票据（download ticket）**：
  1. 主页面点击下载 → `POST /api/materials/{id}/download-ticket`（Cookie 鉴权 + 同班校验）→ 生成随机票据写入 `download_tickets(token, handout_id, user_id, class_id, expires_at, used_at)`，TTL 60 秒、`token` 唯一，返回 `{ticket, fileName}`。
  2. `window.open('/download?ticket=...')` 在新标签打开前端下载路由页。
  3. 下载页挂载时调用 `GET /api/download?ticket=...` 换取文件（不依赖 Cookie）：服务端校验票据存在、未使用、未过期且 `class_id` 与持票用户一致，**在同一事务中标记 `used_at`**，然后以 `Content-Disposition: attachment; filename*=UTF-8''<原始名>` 回送文件；前端拿到 blob 后触发浏览器保存。
  4. 刷新下载页 → 票据已消费 → 服务端返回 401/410 → 下载页脚本 `location.replace('/login')`；无票据/票据伪造同理。票据交换不依赖 Cookie，也保证了“下载页本身不持有可反复使用的身份凭证”。
- 备选：新页面直接带 Cookie 访问下载接口——被否，原因如上，无法满足刷新即失权的验收点。

### 6. 数据模型（六类核心结构 + 会话/票据）

| 表 | 关键列 | 说明 |
|---|---|---|
| `classes` | id, name UNIQUE, created_at | 班级（A/B） |
| `users` | id, username UNIQUE, password_hash, display_name, role(`teacher`/`student`), class_id FK, created_at | 用户 |
| `handouts` | id, class_id FK, uploader_user_id FK, original_name, stored_path, file_type, size_bytes, created_at, INDEX(class_id, created_at) | 讲义/知识库材料（上传写入此表） |
| `assignments` | id, class_id FK, title, description, due_at, created_at | 作业（仅种子，不做提交/批改） |
| `assistants` | id, class_id FK, name, description, created_at | 助手（仅种子，不做对话） |
| `skills` | id, assistant_id FK, name, description, created_at | 技能（仅种子，不执行） |
| `sessions` | id PK, user_id FK, created_at, expires_at | 服务端会话 |
| `download_tickets` | token PK, handout_id FK, user_id, class_id, expires_at, used_at | 一次性下载票据 |

外键全部 `ON DELETE CASCADE`；所有班级隔离查询走 `handouts(class_id, created_at)` 索引。

### 7. 启动初始化、种子数据与环境变量

- `deploy/mysql/01_schema.sql`：仅 DDL，由 MySQL 官方镜像 `/docker-entrypoint-initdb.d` 在首次初始化时执行（不含密码、不含用户行）。
- 种子写入放在 **backend 启动阶段**（而非 SQL），因为密码必须是 bcrypt 哈希且来自环境变量：
  1. 启动后轮询 `db.Ping()`（最多 60 秒，等 MySQL 健康）；
  2. 确保 schema 存在（embed 的 schema 与 init SQL 内容一致，做一次幂等建表兜底）；
  3. 幂等 upsert：班级 A/B；教师 A（A 班）、学生 A1（A 班）、学生 B1（B 班）；两班各至少一条标题可区分的讲义（从容器内 `/app/seed/materials/` 拷贝进数据目录并登记 handouts，标题如《A班·数学第一章·集合讲义》《B班·数学第一章·集合讲义》）；每班一个 assistant + 一条 skill、一条 assignment；
  4. 以 username 为幂等键：已存在则不覆盖密码，重复 `up` 不产生重复行/重复文件登记。
- 环境变量（`.env.example` 给样例，compose `environment` 透传）：
  `MYSQL_DATABASE / MYSQL_USER / MYSQL_PASSWORD`、`SEED_TEACHER_A_USERNAME`、`SEED_TEACHER_A_PASSWORD`、`SEED_STUDENT_A1_USERNAME/PASSWORD`、`SEED_STUDENT_B1_USERNAME/PASSWORD`、`SESSION_TTL_SECONDS`。缺少必需变量时启动失败并打印缺项，不用默认密码兜底。

### 8. GET /health

`GET /health` 不经鉴权、经 Nginx 同源暴露：执行 `db.PingContext(2s)`；成功 → 200 `{"status":"ok","db":"up"}`；失败 → 503 `{"status":"degraded","db":"down"}`。compose 中 backend 的 `healthcheck` 即打此端点；nginx 的 `depends_on: backend: condition: service_healthy` 保证入口就绪后再对外。

### 9. 前端结构与北大红视觉

- 路由（react-router）：`/login` 公开；`/` 主页面、`/download` 票据下载页均为 `ProtectedRoute` 之外的半公开页——主页面在挂载时调 `GET /api/me`，401 即重定向 `/login?next=...`；下载页只认票据、任何失败一律 `/login`。
- 所有请求 `credentials: 'include'`。401 统一清场跳登录；403 上传场景就地展示后端文案「学生无权限上传文件」。
- 主页面布局：顶部条（北大红 `#94070A` 背景）左侧班级 + 姓名 + 身份徽标，右侧退出按钮；下方左栏（上传表单 + 材料列表，列表项即 `original_name`，带“查看/下载”操作），右栏（内容区：文本/md 渲染或 pdf iframe）。主题色用 CSS 变量 `--pku-red: #94070A` 及同系浅色描边/悬停态。
- 仅引入 `react-router-dom` 与 `marked` 两个轻量库；不引入重型组件库，保证视觉可按北大红定制。

## Risks / Trade-offs

- [跨班返回 403 会泄露“材料存在”] → 教学验收优先选择可观察的拒绝；如未来改为 404，只需调整一个 handler 分支，spec 需同步。
- [明文 HTTP 下 Cookie 无 Secure] → 仅限本机/内网演示；SameSite=Lax + HttpOnly 收敛风险；上生产由网关切 HTTPS（Non-goal）。
- [一次性票据表会增长] → TTL 60 秒 + 启动时清理过期行；量极小，无需定时任务。
- [仅做扩展名 + 魔数校验，不查杀恶意 PDF] → 与 Non-goal 一致；上传目录不可直出、下载强制 `attachment`，降低误触执行面。
- [`marked` 渲染 md 存在 XSS 面] → 渲染前不使用原始 HTML 透传，配置仅渲染受信教师材料，并对结果做基础转义/关闭原始 HTML。
- [init SQL 与 Go 内嵌 schema 双份漂移] → 以同一份 SQL 文件经 `//go:embed` 打入后端，容器内只放一份，MySQL init 与 Go 兜底读取同源文件。

## Migration Plan

全新部署，无迁移：

1. `cp .env.example .env` 填入种子口令；
2. `docker compose up --build -d`；
3. 等待 backend healthcheck 通过后访问 `http://localhost:8080/`。

回滚/重置：`docker compose down -v`（删除数据卷与种子材料），重新 up 即回到初始态。
