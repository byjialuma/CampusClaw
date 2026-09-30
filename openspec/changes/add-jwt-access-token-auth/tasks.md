# Tasks: JWT Access Token 认证改造

## 1. 配置与基础设施

- [x] 1.1 修改 `backend/internal/config/config.go`，新增 `JWTSecret` 和 `JWTExpiryHours` 字段；从环境变量 `JWT_SECRET` 和 `JWT_EXPIRY_HOURS` 读取；`JWT_SECRET` 为必填项，长度至少 32 字节；`JWT_EXPIRY_HOURS` 默认 2。验证：单元测试覆盖缺失/过短密钥的报错场景。
- [x] 1.2 修改 `docker-compose.yml`，在 backend 服务中新增 `JWT_SECRET` 环境变量（从 `.env` 读取）；更新 `.env.example` 添加 `JWT_SECRET` 和 `JWT_EXPIRY_HOURS` 示例。验证：`docker compose config` 解析无误。
- [x] 1.3 从 `config.go` 中移除 `SessionTTL` 字段及相关环境变量 `SESSION_TTL_SECONDS` 的读取。验证：编译通过，无残留引用。

## 2. JWT 核心实现（后端）

- [x] 2.1 新建 `backend/internal/auth/jwt.go`，实现 `JWTService` 接口：`Sign(user *model.User) (string, time.Time, error)` 和 `Verify(token string) (*model.User, error)`。使用 HS256，payload 包含 `user_id`、`class_id`、`role`、`display_name`、`class_name`、`exp`、`iat`。验证：单元测试覆盖签名生成、验签成功、过期 token 拒绝、篡改签名拒绝、格式错误拒绝。
- [x] 2.2 修改 `backend/internal/server/server.go`：在 `API` 结构体中新增 `JWT JWTService` 字段；将 `Users` 字段类型从 `userService` 改为仅保留 `Authenticate` 的接口（移除 `CreateSession`/`UserForToken`/`DeleteSession` 依赖）。验证：编译通过。
- [x] 2.3 修改 `backend/cmd/server/main.go`：创建 `JWTService` 实例并注入 `API`；`auth.Repository` 仅保留 `Authenticate` 能力，不再注入会话相关依赖。验证：编译通过，服务可启动。

## 3. 后端接口改造

- [x] 3.1 修改 `backend/internal/server/sessions.go` `login`：认证成功后调用 `JWT.Sign` 生成 token，响应体改为 `{"user": {...}, "accessToken": "...", "expiresAt": "..."}`；不再调用 `setSessionCookie`。验证：单元测试断言响应包含 token 且无 Set-Cookie 头。
- [x] 3.2 修改 `backend/internal/server/sessions.go` `logout`：改为直接返回 204 No Content，不操作 Cookie，不调用 `DeleteSession`。验证：单元测试断言响应 204。
- [x] 3.3 修改 `backend/internal/server/middleware.go` `requireAuth`：从 `Authorization` 头提取 `Bearer <token>`，调用 `JWT.Verify` 验签；验签失败（缺失、格式错误、签名错误、过期）统一返回 401「未登录或会话已失效」。验证：单元测试覆盖无头、非 Bearer、过期、篡改场景。
- [x] 3.4 修改 `backend/internal/server/download.go` `createTicket`：从 JWT 解析的 `class_id` 和 `user_id` 创建票据，不再从会话中取。验证：单元测试断言票据创建使用 JWT claims。
- [x] 3.5 确认 `backend/internal/server/search.go`、`materials.go` 等所有使用 `auth.CurrentUser` 的处理器无需修改（上下文注入方式不变，仅来源从会话变为 JWT）。验证：编译通过，现有测试通过。

## 4. 前端 API 层改造

- [x] 4.1 修改 `frontend/src/api.ts` `request` 函数：从 `localStorage.getItem('cc_access_token')` 读取 token，若存在则在请求头中注入 `Authorization: Bearer ${token}`；移除 `credentials: 'include'`（改为 `credentials: 'same-origin'` 或省略）。验证：构建通过，TypeScript 类型检查无错。
- [x] 4.2 修改 `frontend/src/api.ts` `login`：响应类型改为 `{ user: Me, accessToken: string, expiresAt: string }`；成功后调用 `localStorage.setItem('cc_access_token', accessToken)`；返回 `user` 对象。验证：构建通过。
- [x] 4.3 修改 `frontend/src/api.ts` `logout`：移除对 `DELETE /api/sessions` 的调用（或保留但不再依赖），改为直接 `localStorage.removeItem('cc_access_token')`。验证：构建通过。
- [x] 4.4 修改 `frontend/src/api.ts` `fetchMe`：若 localStorage 无 token，直接返回 `null` 不发请求；若有 token，调用 `/api/me` 并在 401 时清除 localStorage token。验证：构建通过。
- [x] 4.5 修改 `frontend/src/api.ts` `fetchMaterials`/`searchMaterials`/`fetchContent`/`createTicket`：401 响应时清除 localStorage token 并跳转登录页（统一在 `request` 函数中处理 401 跳转逻辑，避免重复代码）。验证：构建通过。
- [x] 4.6 修改 `frontend/src/api.ts` `downloadByTicket`：请求头中注入 `Authorization: Bearer`；保持 `cache: 'no-store'`。验证：构建通过。

## 5. 前端页面改造

- [x] 5.1 修改 `frontend/src/pages/LoginPage.tsx`：登录成功后 `localStorage.setItem('cc_access_token', accessToken)`，然后 `navigate(next)`。验证：构建通过。
- [x] 5.2 修改 `frontend/src/pages/HomePage.tsx`：`useEffect` 中先检查 localStorage 是否有 token，无则直接跳登录；有则调用 `fetchMe` 验证并获取用户信息。验证：构建通过。
- [x] 5.3 修改 `frontend/src/pages/DownloadPage.tsx`：`useEffect` 中检查 localStorage 是否有 token，无则跳登录；`downloadByTicket` 调用已自动携带 token。验证：构建通过。
- [x] 5.4 修改 `frontend/src/components/TopBar.tsx`：退出按钮点击时调用 `logout`（清除 localStorage token），然后 `window.location.replace('/login')`。验证：构建通过。

## 6. 测试更新

- [x] 6.1 新建 `backend/internal/auth/jwt_test.go`：覆盖签名/验签/过期/篡改/空 token/错误算法场景。验证：`go test ./internal/auth -v` 通过。
- [x] 6.2 修改 `backend/internal/server/handlers_test.go`：更新登录测试断言响应体含 `accessToken`；更新受保护接口测试，使用 `Authorization: Bearer` 头替代 Cookie；新增过期 token、篡改 token、无 token 的 401 场景。验证：`go test ./internal/server -v` 通过。
- [x] 6.3 修改 `deploy/tests/e2e-auth-matrix.sh`：所有需要认证的请求从 Cookie 改为 `Authorization: Bearer` 头；登录请求断言响应体含 `accessToken` 并提取到变量供后续请求使用；新增「过期 token 被拒绝」「篡改 token 被拒绝」场景。验证：全新栈 `docker compose up -d --build` 后执行脚本，全部 PASS。
- [x] 6.4 修改 `deploy/tests/e2e-search-matrix.sh`：所有需要认证的请求使用 `Authorization: Bearer` 头。验证：执行脚本，全部 PASS。
- [x] 6.5 确认 `deploy/tests/e2e-failure-drill.sh` 中涉及登录的部分已适配 JWT。验证：执行脚本，全部 PASS。

## 7. 端到端验证

- [x] 7.1 全新启动验证：`docker compose down -v && docker compose up --build -d`，等待健康检查通过；用 teacherA 登录获取 token；携带 token 访问材料列表/检索/内容预览/票据下载，全部 200；学生 studentB1 登录后检索「Sets」有结果、检索「集合」为空。验证：浏览器手动走查或 E2E 脚本。
- [x] 7.2 跨班隔离验证：A 班用户 token 访问 B 班材料内容/下载/检索，全部 403。验证：E2E 脚本。
- [x] 7.3 过期 token 验证：手动设置 `JWT_EXPIRY_HOURS=0.001`（约 3.6 秒），登录后等待过期，再调用受保护接口应返回 401，前端自动跳登录。验证：手动测试或临时脚本。
- [x] 7.4 运行 `openspec validate add-jwt-access-token-auth --strict`，退出码 0。

## 8. 清理

- [x] 8.1 确认 `backend/internal/auth/repository.go` 中 `CreateSession`/`UserForToken`/`DeleteSession` 不再被任何代码引用，可安全移除（或保留但标记 deprecated）。验证：`grep -r "CreateSession\|UserForToken\|DeleteSession" backend/` 仅出现在 `auth/repository.go` 和测试文件中。
- [x] 8.2 确认数据库 `sessions` 表不再被使用；如确认无引用，在后续变更中删除该表（本期保留以减少迁移风险）。验证：`grep -r "sessions" backend/internal/` 确认无 SQL 查询。
- [x] 8.3 更新 `openspec/changes/add-jwt-access-token-auth/tasks.md`，勾选所有完成任务。
