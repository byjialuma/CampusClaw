## Context

当前系统使用服务端会话 Cookie + HttpOnly Cookie 实现认证。后端 `auth.Repository` 在 MySQL `sessions` 表中创建/查询/删除会话记录，`server/middleware.go` 从 Cookie 提取会话 ID 并查库验证。前端所有请求通过 `credentials: 'include'` 自动携带 Cookie。需要替换为 JWT access token 模式，移除服务端会话状态。

## Goals / Non-Goals

**Goals:**
- 登录后返回 JWT access token（HS256 签名，2 小时有效期）
- 所有受保护 API 通过 `Authorization: Bearer <token>` 头认证
- 服务端无状态：不存储会话、不查库验证 token，仅验签 + 解析 claims
- JWT payload 包含 `user_id`、`class_id`、`role`、`display_name`、`class_name`，与现有 `model.User` 结构一致
- 前端登录后存 token 到 `localStorage`，每次请求由 `api.ts` 统一注入 `Authorization` 头
- 退出登录：前端清除本地 token，后端无操作
- 下载票据接口仍保持现有票据机制（一次性、60 秒），但鉴权从 Cookie 改为 JWT

**Non-Goals:**
- 不引入 refresh token（2 小时过期后重新登录）
- 不实现 token 撤销/黑名单（依赖短有效期）
- 不修改数据库表结构（sessions 表可保留但不再使用；为减少迁移风险，本次不删表）
- 不修改材料上传/下载/检索的业务逻辑，仅改变认证载体
- 不引入第三方 JWT 库，用 Go 标准库 `crypto/hmac` + `encoding/base64` + `encoding/json` 手写实现

## Decisions

### 1. JWT 签名算法：HS256（HMAC-SHA256）

选择对称加密而非 RS256 非对称加密，理由：
- 当前单实例部署，无多服务间验签需求
- 无密钥分发问题，配置简单
- HS256 性能更好
- 未来如需切换 RS256，只需改 `Sign`/`Verify` 实现，接口不变

**替代方案考虑**：RS256 更适合微服务分布式验签，但当前过度设计。

### 2. JWT 库：手写实现（不引入第三方）

Go 标准库足够实现 JWT：
- `crypto/hmac` + `crypto/sha256` 做签名
- `encoding/base64` 做 Base64URL 编码
- `encoding/json` 序列化 claims
- 约 80 行代码，无外部依赖

**替代方案考虑**：`github.com/golang-jwt/jwt/v5` 是标准选择，但引入新依赖会扩大攻击面；手写实现更透明可控。

### 3. Token 存储：前端 localStorage

- 登录成功后，后端在 JSON 响应体中返回 token
- 前端存入 `localStorage`（key: `cc_access_token`）
- 每次 API 请求从 localStorage 取出，注入 `Authorization: Bearer <token>`

**风险与缓解**：
- **XSS 风险**：localStorage 可被 JS 读取，不如 HttpOnly Cookie 安全。缓解：前端已使用 React（自动转义输出），后端返回的 JSON 不包含 HTML；CSP 头可后续添加。
- **刷新保持**：localStorage 在页面刷新后仍保留，用户体验好。

### 4. 后端认证中间件改造

替换 `requireAuth` 中的 Cookie 提取逻辑：
```go
// 旧：从 Cookie 取会话 ID → 查 MySQL sessions 表 → 返回用户
// 新：从 Authorization 头取 Bearer token → 验签 + 检查过期 → 解析 claims → 返回用户
```

新增 `auth.JWTService` 接口：
```go
type JWTService interface {
    Sign(user *model.User) (token string, expiresAt time.Time, err error)
    Verify(token string) (*model.User, error)
}
```

`API` 结构体中 `Users` 字段类型从 `userService` 改为组合：
```go
type API struct {
    Users      userAuthenticator  // 仅保留 Authenticate
    JWT        JWTService         // 新增：签名与验签
    // ... 其他不变
}
```

### 5. 登录接口契约变更

**旧响应**（仅用户信息，token 在 Cookie 中）：
```json
{"id":1,"username":"teacherA","displayName":"教师A","role":"teacher","classId":1,"className":"A班"}
```

**新响应**（用户信息 + token）：
```json
{
  "user": {"id":1,"username":"teacherA","displayName":"教师A","role":"teacher","classId":1,"className":"A班"},
  "accessToken": "eyJhbGciOiJIUzI1NiIs...",
  "expiresAt": "2026-09-30T12:00:00Z"
}
```

### 6. 下载票据接口的 JWT 鉴权

`POST /api/materials/{id}/download-ticket` 保持现有票据逻辑，但：
- 从 `Authorization` 头提取 JWT，解析出 `user_id` 和 `class_id`
- 票据创建时使用 JWT 中的 `class_id`（而非会话中的）
- 下载页 `DownloadPage` 从 localStorage 读 token，在请求头中携带

### 7. 前端路由守卫

`HomePage` 和 `DownloadPage` 的 `useEffect` 中：
- 检查 localStorage 是否有 token
- 若无，跳 `/login?next=...`
- 若有，调用 `/api/me` 验证 token 有效性（顺便获取用户信息）
- 若 `/api/me` 返回 401，清除 localStorage token 并跳登录

### 8. 配置变更

新增环境变量：
- `JWT_SECRET`：HS256 签名密钥（至少 32 字节随机字符串），**必须**设置，启动时校验
- `JWT_EXPIRY_HOURS`：token 有效期，默认 2

移除环境变量：
- `SESSION_TTL_SECONDS`：不再使用

### 9. 测试策略

- **单元测试**：`auth/jwt_test.go` 测试签名/验签/过期/篡改 token
- **集成测试**：`server/handlers_test.go` 更新登录/登出/受保护接口测试
- **E2E 测试**：`e2e-auth-matrix.sh` 更新为使用 `Authorization: Bearer` 头而非 Cookie

## Risks / Trade-offs

- **XSS 窃取 token** → 前端使用 React 自动转义，后端返回纯 JSON；后续可加 CSP 头 `default-src 'self'`。
- **Token 无法主动撤销** → 有效期仅 2 小时，影响有限；未来如需强制下线可引入 Redis 黑名单。
- **localStorage 被恶意脚本读取** → 与 XSS 风险相同，通过输入过滤和输出编码缓解。
- **手写 JWT 实现引入安全漏洞** → 代码审查 + 单元测试覆盖边界情况（过期、签名错误、格式错误）。
- **前端刷新后丢失登录态** → 已用 localStorage 解决，刷新后 token 仍在。
- **同时存在 Cookie 和 JWT 的过渡期** → 无过渡期，直接替换；旧前端版本将无法工作。

## Migration Plan

1. 后端新增 `auth/jwt.go` 实现 JWT 签名/验签
2. 后端修改 `config.go` 增加 `JWT_SECRET` 和 `JWT_EXPIRY_HOURS` 校验
3. 后端修改 `server.go` 中 `API` 结构体，注入 `JWT` 服务
4. 后端修改 `sessions.go` 登录接口，返回 token 而非 Cookie
5. 后端修改 `middleware.go` `requireAuth`，从 Header 提取并验证 JWT
6. 后端修改 `download.go` 票据创建，从 JWT 取 `class_id`
7. 前端修改 `api.ts`，所有请求加 `Authorization` 头，登录响应存 token
8. 前端修改 `LoginPage.tsx`，登录成功后存 token 到 localStorage
9. 前端修改 `HomePage.tsx`，从 localStorage 读 token，401 时清除并跳登录
10. 前端修改 `DownloadPage.tsx`，从 localStorage 读 token 并在请求头携带
11. 前端修改 `TopBar.tsx` 退出按钮，清除 localStorage token
12. 更新 `docker-compose.yml` 和 `.env.example`，添加 `JWT_SECRET`
13. 更新 E2E 测试脚本，使用 `Authorization: Bearer` 头
14. 运行全部测试（单元 + E2E），验证零回归

## Open Questions

- 是否需要支持「记住我」功能（延长 token 有效期到 7 天）？→ 本期不做，保持 2 小时。
- 是否需要在响应头中返回 `X-Token-Expires-At` 供前端提前刷新？→ 本期不做，401 时重新登录即可。
