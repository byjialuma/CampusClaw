## Why

CampusClaw 目前的解题助手只会用「本班知识库检索」这一种信息来源：教师能维护提示词与技能，但技能只能写自然语言指令，无法让助手调用任何外部能力。课程要求引入 MCP（Model Context Protocol）：由本仓自建一台 MCP 工具服务，教师把它登记为本班服务器、连接后发现工具清单，再把 `工具名@服务器名` 写进技能；学生提问命中该技能时，服务端带令牌真实调用远端工具，把返回结果作为数据交给模型，并留下调用审计。这样「技能」从纯提示词升级为可执行的工具绑定，且主机侧（登记、鉴权、发现、越权拦截）全部落在本系统。

## What Changes

- **新增自建 MCP 服务** `mcp-server/`（与主应用同仓，由 `docker-compose.yml` 一并构建启动）：提供 `POST /mcp`（JSON-RPC 2.0）与 `GET /health`，实现 `initialize`、`tools/list`、`tools/call`；Bearer 令牌只从环境变量 `MCP_AUTH_TOKEN` 读取，无令牌或令牌错误返回 401，非法请求返回可读的 JSON-RPC 错误。内置两个可调用工具 `formula_hint`、`unit_convert`（工具名小写字母开头、无连字符）。
- **新增教师侧 MCP 服务器登记**：教师填写名称、Base URL、Bearer 令牌，登记为本班服务器（同班同名 400）。令牌只存服务端表；任何接口响应只回 `hasAuth` 布尔值，永不回传令牌原文。
- **新增连接与工具发现**：教师对某台服务器点「连接」——服务端先握手再执行 `tools/list`，把工具清单缓存入库并置为已连接；「断开」置为未连接并清空缓存。连接失败返回 502 与可读提示，且不影响既有检索与流式对话主路径。
- **技能工具栏支持 MCP 引用**：技能正文中的「工具」行除内置 `knowledge_search` 外，接受 `工具名@服务器名` 引用；保存/上传时逐条校验（服务器存在、已连接、工具在缓存清单内），任一不成立即拒绝保存并给出可读提示。
- **提问触发鉴权调用**：学生提问命中带 MCP 工具的技能时，服务端按技能声明的工具引用带令牌调用远端 `tools/call`；调用结果作为**数据**拼进用户消息（不当作系统指令），远端超时/报错不阻断回答生成，仅记录为 `error`。
- **可观测**：SSE `meta` 事件新增 `toolCalls`（工具引用 + 状态）；每次真实调用（成功/拒绝/出错）写入审计表，含工具引用、服务器名、状态、用户与时间；未连接或越权调用记为 `denied` 且不静默成功。
- **教师管理界面**：新增 MCP 服务器管理区（登记/修改/删除、连接/断开、查看已发现工具、查看最近调用审计），技能管理区展示每条技能解析出的工具绑定。
- 沿用既有要求不变：班级只从 JWT claims 读取、跨班一律 404、Bearer 鉴权、SSE 流式、技能启用/停用、内置技能删除保护；知识库检索行为不变。

## Capabilities

### New Capabilities

- `mcp-server`: 自建 MCP 工具服务的对外契约——HTTP JSON-RPC 2.0 端点、Bearer 令牌鉴权、`initialize`/`tools/list`/`tools/call` 三个方法与两个内置工具及其返回契约。

### Modified Capabilities

- `tutor-agent`: 新增 MCP 服务器登记与令牌保管、连接与工具发现（含断开清缓存）、技能工具栏的 MCP 引用解析与保存前校验、提问时的鉴权工具调用与结果数据化注入、SSE meta 的 `toolCalls`、调用审计，以及教师侧 MCP 管理界面。

## Impact

- **新增目录/服务**：`mcp-server/`（Go + 标准库 net/http，Dockerfile 多阶段构建）；`docker-compose.yml` 新增 `mcp-demo` 服务（`http://mcp-demo:8090`，仅接入 `ccnet` 内部网络、不发布宿主机端口，健康检查 `GET /health`）；`.env` 新增 `MCP_AUTH_TOKEN`（令牌，不入 Git、不入镜像层、不进前端）。
- **数据库**：新增 `mcp_servers` 表（班级归属、名称、Base URL、令牌、连接状态、工具清单缓存、最近错误）与 `mcp_calls` 审计表（工具引用、服务器名、状态、用户、时间、耗时）；`backend/internal/db/schema.sql` 与 `deploy/mysql/01_schema.sql` 同步，迁移幂等。
- **后端（Go net/http）**：新增 `internal/mcp`（JSON-RPC 客户端：握手、列工具、调用工具，含超时与错误映射）与 `internal/tutor` 内的 MCP 登记/连接/发现/审计仓储；`internal/server` 新增 `/api/tutor/mcp/...` 路由（教师专属，学生 403、跨班 404）；`tutor.Service.AskStream` 在生成前插入工具调用阶段，`Event` 携带 `ToolCalls`。
- **前端（React）**：教师「助手设置」标签页新增 MCP 服务器管理区（登记表单、连接/断开、工具清单、最近审计）；「技能管理」标签页展示每条技能解析出的工具引用并在保存时呈现校验失败原因；学生侧无任何 MCP 入口（仅间接触发调用）。
- **配置**：后端新增 `MCP_TIMEOUT_SECONDS`（默认 10，工具调用超时，防止远端拖垮 SSE）；MCP 服务新增 `MCP_AUTH_TOKEN`（必需）与 `MCP_PORT`（默认 8090）。
- **外部依赖**：无新增第三方库；MCP 服务与主机侧均为标准库实现。
