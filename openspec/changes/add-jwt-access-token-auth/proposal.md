# Proposal: 登录认证改用 JWT Access Token

## Why

当前系统使用服务端会话 Cookie + HttpOnly Cookie 实现登录认证，后端通过查库或内存存储会话令牌来验证身份。随着系统未来可能需要扩展到移动端或多端接入，JWT（JSON Web Token）access token 方案更适合无状态认证、水平扩展和微服务集成。本次变更旨在**替换**现有的会话 Cookie 认证为 JWT access token 模式，移除对服务端会话状态的依赖。

## What Changes

- **BREAKING**: 移除所有基于服务端会话 Cookie（`HttpOnly` Cookie）的认证方式；后端不再创建、存储或验证会话令牌。
- **BREAKING**: 所有受保护 API 不再接受 Cookie，统一改为通过 `Authorization: Bearer <token>` 请求头传递 JWT access token。
- **登录接口** `POST /api/sessions` 成功响应体中返回 JWT access token（含签名算法、过期时间），不再发放会话 Cookie；同时保留「用户名或密码错误」的统一提示。
- **退出接口** `POST /api/sessions`（当前实现为登出）改为**无操作**（客户端直接丢弃 token），或移除该接口；服务端不维护黑名单，access token 到期自然失效。
- **下载票据接口** `POST /api/materials/{id}/download-ticket` 仍需鉴权，但改为在请求头中校验 JWT；票据本身仍是单次有效、60 秒过期。
- **前端**：登录成功后，将 access token 存于内存（如 `localStorage` 或 `sessionStorage`，建议 `localStorage` 以支持刷新页面不丢）；每次 API 请求由统一封装在 `api.ts` 的函数注入 `Authorization` 头；退出时清除本地存储的 token。
- **后端中间件**：替换现有 `middleware.go` 中的会话校验逻辑，改为解析 `Authorization: Bearer` 头、验证 JWT 签名与过期时间，提取 `class_id`/`user_id` 等信息并注入请求上下文。
- **密钥管理**：JWT 签名密钥通过环境变量注入，不硬编码；使用 HS256 对称加密（HS256 足够当前规模，未来可平滑切换 RS256）。
- **Token 有效期**：access token 有效期设为 2 小时（可根据后续用户体验调整），过期后前端收到 401 应跳转至登录页。

## Capabilities

### Modified Capabilities

- `classroom-knowledge-base`: 修改所有与登录认证相关的需求——移除对「服务端会话 Cookie」和「HttpOnly Cookie」的强制要求，替换为「JWT access token」认证机制；明确前端存储方式、请求头传递方式、token 过期处理等场景。

## Impact

- **后端代码**：`backend/internal/auth/`（token.go、middleware 等）、`backend/internal/server/sessions.go`、`backend/internal/server/middleware.go`、`backend/internal/server/server.go`、`backend/cmd/server/main.go`。
- **前端代码**：`frontend/src/api.ts`、`frontend/src/pages/LoginPage.tsx`、`frontend/src/pages/HomePage.tsx`、`frontend/src/pages/DownloadPage.tsx`。
- **API 契约**：所有受保护端点的认证方式从 Cookie 变为 Header；客户端需携带 `Authorization: Bearer`。
- **安全模型**：JWT 无状态，服务端不保存会话，简化了水平扩展；但需要更严格的 token 有效期管理和 HTTPS（尽管本项目为本地开发环境，仍需注意）。
- **测试**：E2E 鉴权矩阵测试脚本需要更新，改为在请求头中携带 token 而非 Cookie。
- **兼容性**：**BREAKING**，现有基于 Cookie 的客户端（包括当前前端版本）将无法直接工作，需要同步升级。
