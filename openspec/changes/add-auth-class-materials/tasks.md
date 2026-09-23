## 1. 工程骨架与部署基础设施

- [x] 1.1 创建仓库目录结构（`frontend/`、`backend/`、`deploy/nginx/`、`deploy/mysql/`、`deploy/seed/materials/`、数据卷挂载点），并确认各目录与占位文件存在
- [x] 1.2 编写 `.env.example`，包含 `MYSQL_DATABASE/USER/PASSWORD`、`SEED_TEACHER_A_USERNAME/PASSWORD`、`SEED_STUDENT_A1_USERNAME/PASSWORD`、`SEED_STUDENT_B1_USERNAME/PASSWORD`、`SESSION_TTL_SECONDS`；verify：文件中无任何真实密码，所有种子密码项均存在
- [x] 1.3 初始化 `backend/` Go module（`go mod init campusclaw`，仅引入 `golang.org/x/crypto/bcrypt` 与 MySQL 驱动，不引入 Web 框架）；verify：`go build ./...` 通过且 `go list -m all` 中无 gin/echo 等框架
- [x] 1.4 编写 `backend/Dockerfile`（多阶段构建，产物拷贝种子材料目录）与 `frontend/Dockerfile`（node 构建 → nginx 静态产物）；verify：两个镜像均能 `docker build` 成功
- [x] 1.5 编写 `docker-compose.yml`：mysql、backend、nginx 三服务接入同一内部网络，仅 nginx 发布宿主端口（8080:80），backend 配 healthcheck、nginx `depends_on: service_healthy`；verify：compose 文件通过 `docker compose config` 校验，且 mysql/backend 均无 `ports:` 发布

## 2. 数据库 Schema、连接与健康检查

- [x] 2.1 编写 `deploy/mysql/01_schema.sql`：创建 classes、users、handouts、assignments、assistants、skills、sessions、download_tickets 八张表（外键、`handouts(class_id, created_at)` 索引、role 枚举）；verify：在临时 MySQL 8 容器中执行该脚本无报错，`SHOW TABLES` 含全部八张表
- [x] 2.2 后端以同一份 SQL（`//go:embed`）实现启动时幂等建表兜底与 MySQL 就绪轮询（最长 60 秒）；verify：断开数据库启动后端时进程等待重试而非崩溃，数据库就绪后自动继续
- [x] 2.3 实现 `GET /health`：2 秒超时 `db.Ping`，成功 200 `{"status":"ok","db":"up"}`，失败 503；verify：`go test ./...` 中健康检查单测覆盖 ping 成功/失败两种分支；手工 curl 分别得到 200 与停库后的 503

## 3. 会话、密码与登录退出

- [x] 3.1 在 `internal/auth` 实现 bcrypt 密码哈希/校验与 `crypto/rand` 32 字节会话 token 生成；verify：单测验证哈希不可逆、正确密码通过、错误密码拒绝、两次生成的 token 不同
- [x] 3.2 实现 `POST /api/sessions`：校验用户名密码，失败统一返回 401「用户名或密码错误」（用户不存在与密码错误响应一致），成功创建 sessions 行并下发 `HttpOnly; Path=/; SameSite=Lax` 的 `cc_session` Cookie；verify：单测 + curl 验证错误密码/不存在用户均为 401 且响应体相同，正确登录响应头含 HttpOnly Cookie
- [x] 3.3 实现 `requireAuth` 中间件（查会话表、校验过期、把 user_id/role/class_id 注入上下文）与 `GET /api/me`；verify：无 Cookie/无效 Cookie/过期会话访问受保护接口均为 401，有效会话 `/api/me` 返回班级、姓名、角色
- [x] 3.4 实现 `DELETE /api/sessions` 退出：删除服务端会话行并过期化 Cookie；verify：退出后用原 Cookie 调 `/api/me` 返回 401

## 4. 角色权限与服务端班级隔离

- [x] 4.1 实现 `requireTeacher` 中间件：非 teacher 角色返回 403 `{"error":"学生无权限上传文件"}`；verify：单测分别以教师/学生会话断言 200 放行与 403 拒绝
- [x] 4.2 实现材料数据访问层，所有查询强制以会话 `class_id` 为绑定参数，且任何写操作不接受请求体中的 class_id/user_id；verify：代码审查确认无裸拼 SQL；单测验证不同班级会话生成的查询条件不同
- [x] 4.3 实现跨班访问判定：记录存在但班级不匹配返回 403，记录不存在返回 404；verify：单测覆盖同班 200、跨班 403、不存在 404 三个分支

## 5. 教师材料上传入库

- [x] 5.1 实现 `POST /api/materials`（`requireAuth`+`requireTeacher`，multipart 字段 `file`，32MB 上限）；verify：学生会话（含绕过页面直接 curl 构造 multipart）稳定返回 403 且库表与磁盘无新增
- [x] 5.2 实现文件校验：扩展名白名单 txt/md/pdf，PDF 校验 `%PDF-` 魔数，txt/md 校验 UTF-8 文本且不含 NUL；verify：curl 上传 docx/exe/png/伪扩展名 pdf 均返回 400 且不留文件，txt/md/pdf 上传返回 201
- [x] 5.3 实现安全落盘与入库事务：存储键 `/app/data/materials/<yyyymm>/<random32>.<ext>`，原始文件名仅入 `handouts.original_name`，写库失败回滚删除文件；verify：单测/手工验证磁盘文件名与原始名无关、并发上传不冲突、断库模拟后无孤儿文件
- [x] 5.4 实现 `GET /api/materials` 本班材料列表（按 created_at 倒序，字段含 id、原始文件名、类型、大小、上传时间、上传者）；verify：教师上传成功后用同班会话再调列表能立即查到该记录（名称与上传文件名完全一致），外班会话列表中不存在该 id

## 6. 内容预览与一次性票据下载

- [x] 6.1 实现 `GET /api/materials/{id}/content`：同班返回正确 Content-Type 与 `X-File-Name` 的字节流，跨班 403、未登录 401；verify：curl 对 txt/md/pdf 分别断言 Content-Type，A 班会话取 B 班 id 返回 403
- [x] 6.2 实现 `POST /api/materials/{id}/download-ticket`：Cookie 鉴权 + 同班校验，生成 60 秒一次性票据落库；verify：跨班请求 403；成功响应含 ticket，重复请求产生不同票据
- [x] 6.3 实现 `GET /api/download?ticket=`：事务内校验并标记 `used_at`，成功以 `Content-Disposition: attachment; filename*=UTF-8''...` 回送文件；verify：同一票据第二次使用（模拟刷新）返回 401/410；过期票据被拒；下载文件名等于原始文件名
- [x] 6.4 启动时清理过期 download_tickets；verify：向表中插入过期行并重启后端，确认过期行被清除

## 7. 预置种子数据

- [x] 7.1 在 `deploy/seed/materials/` 放入两班可区分的预置讲义（A 班 txt+md、B 班含一个 pdf，标题如《A班·数学第一章·集合讲义》《B班·数学第一章·集合讲义》）；verify：文件存在且 PDF 以 `%PDF-` 开头
- [x] 7.2 实现后端启动幂等种子：upsert 班级 A/B、教师 A、学生 A1/B1（bcrypt 哈希取自环境变量）、两班讲义、每班一条 assignment/assistant 及关联 skill；verify：用空库启动后各表行数符合预期；再次重启行数不增长、文件不重复登记；缺失必需环境变量时启动失败并提示缺项
- [x] 7.3 验证环境变量控口令：修改 `.env` 中种子密码重新 `up`（新卷），用新密码登录成功、旧密码 401；verify：grep 仓库（含 SQL、Dockerfile、前端）确认无硬编码种子密码

## 8. 前端工程（React 18 + TypeScript + Vite）

- [x] 8.1 用 Vite react-ts 模板初始化 `frontend/`，仅加入 `react-router-dom` 与 `marked`；verify：`npm run build`（`tsc && vite build`）零错误零类型告警
- [x] 8.2 建立北大红主题（CSS 变量 `--pku-red:#94070A` 及同系配色）与全局布局：顶栏左上角显示「班级 · 姓名 · 身份徽标」、右上角退出按钮，左栏上传区+材料列表、右栏内容区；verify：浏览器截图/人工检查布局位置与主色调符合要求
- [x] 8.3 实现登录页与路由守卫：未登录访问任意受保护路由重定向 `/login?next=...`，登录成功跳回；所有请求 `credentials:'include'`，全局 401 拦截跳登录；verify：清空 Cookie 直接访问主页面被引导到登录页；登录后回到原页面
- [x] 8.4 实现 `/api/me` 身份渲染与退出按钮（调用 `DELETE /api/sessions` 后回登录页）；verify：左上角班级/姓名/身份与种子账号一致，退出后再访问受保护页回到登录页
- [x] 8.5 实现左栏上传表单（教师/学生均可见可用）与材料列表（名称取原始文件名，含查看/下载操作）；verify：教师上传后列表实时新增一条；学生提交后页面就地展示「学生无权限上传文件」且列表无变化
- [x] 8.6 实现右栏内容预览：txt 文本、md 经 marked 渲染（关闭原始 HTML 透传）、pdf 用 blob URL 注入 iframe；verify：三类文件分别点击后右栏正确展示
- [x] 8.7 实现 `/download?ticket=` 新标签下载页：挂载即凭票取文件并触发浏览器保存，任何失败（票据失效/用过/刷新）`location.replace('/login')`；verify：点击下载在新标签完成保存；在下载页按 F5 刷新后跳转登录页且不下载文件

## 9. Nginx 反代与端到端联调

- [x] 9.1 编写 `deploy/nginx/nginx.conf`：`/api/` 与 `/health` 反代 backend:8080，其余 `try_files` 回退 index.html；verify：`nginx -t` 通过，前端路由深链刷新不 404
- [x] 9.2 端到端验证鉴权矩阵（curl 脚本保存于仓库测试目录）：未登录 401、学生上传 403、教师上传 201、A 班列表不含 B 班材料、A 班访问 B 班 content/download-ticket 为 403、上传后本班列表可查；verify：脚本全部断言通过
- [x] 9.3 端到端验证网络边界：从宿主机直连 backend 端口与 MySQL 3306 均连接失败，仅 `http://localhost:8080` 可用；verify：端口探测命令均失败，浏览器全链路（登录→列表→预览→下载→退出）走通
- [x] 9.4 验证一键启动与健康检查：`docker compose down -v && docker compose up --build -d` 后等待健康，`curl http://localhost:8080/health` 返回 200；verify：全新环境从零启动一次成功，停止 mysql 容器后 `/health` 返回 5xx，恢复后回到 200

## 10. 变更校验与交付收尾

- [x] 10.1 运行 `openspec validate add-auth-class-materials --strict`；verify：命令退出码为 0，无格式/需求/场景校验错误
- [x] 10.2 对照 spec.md 逐条场景做验收走查（含跨班被拒、学生上传 403、上传后列表可查、下载页刷新跳登录四条关键场景）；verify：每条场景记录实际结果并全部通过
