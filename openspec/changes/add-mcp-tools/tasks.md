## 1. 自建 MCP 服务（mcp-server/）

- [x] 1.1 新建 `mcp-server/` Go 模块与 `main.go`：监听 `:8090`（可由 `MCP_PORT` 覆盖），注册 `POST /mcp` 与 `GET /health`；验证：在 `mcp-server` 目录执行 `go build ./...` 通过
- [x] 1.2 实现 JSON-RPC 2.0 分发与方法：`initialize`（协议版本 + serverInfo）、`tools/list`（name/description/inputSchema）、`tools/call`（`content[].text` + `isError`）；非法 JSON 返回 -32700、未知方法返回 -32601、参数缺失返回 -32602，响应恒为 JSON；验证：单测覆盖四个分支
- [x] 1.3 实现 Bearer 鉴权：除 `/health` 外校验 `Authorization: Bearer <MCP_AUTH_TOKEN>`，缺失或错误返回 401 且不进入工具逻辑；启动时校验令牌非空；验证：单测覆盖无令牌 401、错误令牌 401、正确令牌放行
- [x] 1.4 实现两个内置工具 `formula_hint` 与 `unit_convert`，均接受必填字符串参数 `query`：识别到主题词/数值时返回公式分步提示或换算结果文本，识别不到时返回用法说明文本（仍为成功）；验证：单测分别覆盖命中与未命中两条路径，且工具名符合 `^[a-z][a-z0-9_]*$`
- [x] 1.5 编写 `mcp-server/Dockerfile`（多阶段构建，暴露 8090）；验证：`docker build -f mcp-server/Dockerfile -t campusclaw-mcp:test .` 成功并可本地 `docker run` 后请求 `/health` 返回 200

## 2. 部署与配置

- [x] 2.1 `docker-compose.yml` 新增 `mcp-demo` 服务（build 自 `mcp-server/`，接入 `ccnet`，不发布宿主机端口，healthcheck 请求 `/health`，环境变量 `MCP_AUTH_TOKEN` + `MCP_PORT`），并给 `backend` 注入 `MCP_AUTH_TOKEN`（仅用于播种）与 `MCP_TIMEOUT_SECONDS`；验证：`docker compose config` 通过，`docker compose up -d --build` 后 `campusclaw-mcp` 为 healthy
- [x] 2.2 `.env` 与 `.env.example` 增补 `MCP_AUTH_TOKEN`（随机长串）与 `MCP_TIMEOUT_SECONDS`（默认 10）；验证：`.env` 未被 Git 跟踪（`git check-ignore .env`），`.env.example` 只含占位说明
- [x] 2.3 网络可达性验证：从 `campusclaw-backend` 容器内对 `http://mcp-demo:8090` 完成 `initialize` 握手成功，宿主机无对应端口可连；验证：两条命令的实际输出记录到实施说明中

## 3. 数据库与播种

- [x] 3.1 `backend/internal/db/schema.sql` 与 `deploy/mysql/01_schema.sql` 同步新增 `mcp_servers`（含 `class_id`、`name`、`base_url`、`auth_token`、`connected`、`tool_cache JSON`、`last_error`、时间戳，`UNIQUE (class_id, name)`）与 `mcp_calls`（含 `class_id`、`user_id`、`server_id`（FK `ON DELETE SET NULL`）、`server_name`、`conversation_id`、`tool_ref`、`status ENUM('success','denied','error')`、`error`、`duration_ms`、`created_at`）；验证：两处文件内容一致（除注释头），在既有库上重复执行不报错且表存在
- [x] 3.2 播种预置服务器：为各班级插入 `campus-tools`（`base_url = http://mcp-demo:8090`，`auth_token` 取 `MCP_AUTH_TOKEN`，`connected = 0`、清单为空；环境变量缺失时只写无令牌行），沿用幂等「存在即跳过」风格；验证：重启后端后数据库中有该行，且 `GET /api/tutor/mcp/servers` 只返回 `hasAuth` 布尔而非令牌
- [x] 3.3 `config` 新增 `MCP_TIMEOUT_SECONDS`（默认 10，下限 1，非法值报错）与 `MCP_AUTH_TOKEN`（可空）字段；验证：config 单测覆盖默认值、下限校验与非法值报错

## 4. 主机侧 MCP 客户端与仓储

- [x] 4.1 新增 `internal/mcp` 客户端：`Initialize`、`ListTools`、`CallTool`，携带 Bearer 令牌、受 `MCP_TIMEOUT_SECONDS` 约束、校验 JSON-RPC `id` 与 `error` 字段、把非 2xx/超时/解析失败归一为可读错误；验证：以 `httptest` 假 MCP 服务做单测，覆盖成功、401、超时、非法响应四种情况
- [x] 4.2 `internal/tutor` 仓储新增 MCP 服务器读写：按 `class_id` 隔离的增/改/删/查（重名冲突映射为错误哨兵）、连接状态与清单缓存更新、断开清空；以及审计写入与按班级查询（`server_id` 为 NULL 的历史行仍可读 `server_name`）；验证：仓储测试覆盖重名、跨班不可见、断开清空、审计三态与跨班查询
- [x] 4.3 实现技能工具栏解析器：解析行首「工具」行（逗号分隔，支持 `,`/`，`/`、`）与可选「适用条件」行；`knowledge_search` 判为内置引用，`name@server` 严格校验两段命名；验证：单测覆盖多引用、空行、非法引用（含连字符工具名、缺少 `@`、多段 `@`）、仅 `knowledge_search`、无「工具」行

## 5. 后端路由与角色校验

- [x] 5.1 `internal/server` 新增路由并全部套 `requireAuth` + `requireTeacher`：`GET/POST /api/tutor/mcp/servers`、`PUT/DELETE /api/tutor/mcp/servers/{id}`、`POST /api/tutor/mcp/servers/{id}/connect`、`POST /api/tutor/mcp/servers/{id}/disconnect`、`GET /api/tutor/mcp/calls`；验证：handlers 测试覆盖教师成功、学生 403、未登录 401、跨班与不存在 404
- [x] 5.2 登记/修改响应只回 `hasAuth`：响应结构不含 `authToken`/`token` 键，且列表接口同样只回布尔；验证：测试断言响应 JSON 中不存在令牌键、也不出现令牌原文
- [x] 5.3 连接失败路径：不可达 `base_url` 或错误令牌返回 502 与可读提示，服务器保持 `connected = 0` 且清单为空；验证：handlers 测试用不可达地址断言 502 与数据库状态
- [x] 5.4 技能保存接入引用校验：在线新建与上传 md/txt 两条路径共用校验（服务器存在 → 已连接 → 工具在缓存清单），失败返回 400 并指明具体引用；验证：测试覆盖合法成功、工具不在清单、服务器未连接、服务器不存在四种情况，以及 `knowledge_search` 直接通过

## 6. 编排接入（答案生成前的工具阶段）

- [x] 6.1 `AskStream` 在 `emit(meta)` 之前插入工具阶段：解析本轮已启用技能的工具引用、按「适用条件」判定是否调用、去重后并发调用各 MCP 引用并收集状态；验证：service 单测覆盖去重只调一次、未声明适用条件即调用、声明后未命中关键词不调用
- [x] 6.2 工具结果以数据块拼入送模型的用户消息（标注为数据而非指令），系统提示词不变，且落库消息内容仍为原始问题；验证：单测断言送入模型的 user 内容含定界数据块与工具引用标注，且 `messages.content` 等于原始问题
- [x] 6.3 `Event` 与 `sseMetaPayload` 新增 `toolCalls` 字段（工具引用、服务器名、状态、失败原因）并在 meta 事件中推送；验证：SSE handlers 测试断言 meta 的 `toolCalls` 含预期引用与状态，且不含令牌与内部地址
- [x] 6.4 审计写入：成功/拒绝/出错三态各写一行（含班级、用户、会话、引用、服务器名、时间、耗时）；引用服务器在本班已被删除时记为 `denied` 且不写审计；验证：测试断言数据库行数与状态，并断言已删除服务器场景无审计行
- [x] 6.5 失败与超时不阻断：远端 5xx、超时、非法响应只置 `error` 状态，回答继续生成并正常推送 delta/done；验证：以假服务返回 500 与延迟超时两种情形做单测，断言仍收到 done 事件

## 7. 前端（教师侧入口）

- [x] 7.1 `api.ts` / `types.ts` 新增 MCP 服务器类型与接口封装（列表、登记、修改、删除、连接、断开、审计查询），类型中不含令牌字段；验证：`npm run build` 通过
- [x] 7.2 「助手设置」标签页新增 MCP 服务器管理区：登记表单（名称、Base URL、令牌）、修改/删除、连接/断开按钮、已发现工具名清单、最近调用记录；验证：浏览器走查完成「登记 `campus-tools` → 连接 → 看到 `formula_hint` 与 `unit_convert`」
- [x] 7.3 「技能管理」标签页展示每条技能解析出的工具引用，保存被拒时显示可读拒绝原因；验证：浏览器走查新建含非法引用的技能，界面显示具体引用与原因且技能未写入
- [x] 7.4 学生视图不出现任何 MCP 入口；验证：学生账号登录后走查首页无 MCP 控件，直接调用 MCP 管理接口返回 403

## 8. 测试与验收

- [x] 8.1 后端与 MCP 服务单测：`go vet ./...` 与 `go test ./...` 在 `backend` 与 `mcp-server` 两个模块均通过；验证：命令退出码为 0 且无失败用例
- [x] 8.2 前端构建：`npm run build` 通过；验证：命令退出码为 0
- [x] 8.3 新增 `deploy/tests/e2e-mcp-matrix.sh`（沿用既有 `e2e-*-matrix.sh` 风格）覆盖 DoD：登记后响应只有 `hasAuth`、同班重名 400、连接并发现返回工具清单、错误地址 502、断开后清单清空、非法绑定 400、命中技能时 meta 含该引用且回答体现工具结果、未连接服务器调用记为 `denied`、已删除服务器调用 `denied` 且无审计行、审计三态各一条、学生调用管理接口 403；验证：脚本从容器内执行并全部 PASS
- [x] 8.4 `apitest/` 补齐新接口用例（`cases/api/tutor/mcp/...`，每接口一脚本）：核对状态码之外，还要比对 `mcp_servers` / `mcp_calls` 表里的实际行与响应一致（含令牌永不回传）；验证：`python apitest/run_all.py` 全部通过
- [x] 8.5 `openspec validate add-mcp-tools --strict` 退出码为 0；验证：命令输出无错误提示
