## 1. 数据库与种子

- [x] 1.1 扩展 `deploy/mysql/01_schema.sql` 与 `internal/db` 幂等建表：`assistants.system_prompt`、`skills.enabled`、新表 `conversations`/`messages`（ALTER 前查 INFORMATION_SCHEMA 保证幂等），容器内执行两次建表验证不报错
- [x] 1.2 扩展 `internal/seed`：每班播种「解题助手」（默认 system_prompt）与「解题引导」技能（enabled=1），重新初始化后 `SELECT` 验证两班各 1 助手 1 技能
- [x] 1.3 新建 `internal/tutor/repository.go`：助手配置读写、会话 CRUD（SQL 层带 `user_id` 属主条件）、消息写入与按会话读取、citations JSON 序列化；仓储测试覆盖属主过滤与 JSON 往返

## 2. 流式 LLM 客户端

- [x] 2.1 `internal/llm/stream.go` 实现 `StreamAsk`：`stream:true`、SSE 帧解析（delta 累加、`[DONE]`）、上游非流式降级、onDelta 增量回调；单测覆盖多 delta、非 200、中途断流、context 取消，`go test ./internal/llm` 全过
- [x] 2.2 复用现有 `chatMessage`/请求头/Bearer 认证，确认与 `client.go` 零重复漂移（必要时抽公共构造函数），`go build ./...` 通过

## 3. Tutor 编排服务

- [x] 3.1 新建 `internal/tutor/service.go`：`AskStream` 编排（检索→提示词→技能→材料上下文→消息序列→流式生成→持久化回调），事件回调 `OnEvent`；单测断言编排顺序、技能开/关 system 指令差异、无命中注记+空引用+仍调 LLM、历史按序带入
- [x] 3.2 命中分片映射 citations（含 snippet/locator/score）、提示词快照与技能状态写入 assistant 消息、会话首问生成 title；单测覆盖映射与持久化入参，`go test ./internal/tutor` 全过

## 4. 后端路由与 SSE

- [x] 4.1 新建 `internal/server/tutor.go`：会话创建/列表/消息读取（属主 404）、SSE 提问（meta/delta/done/error 事件、`X-Accel-Buffering: no`、每事件 Flush）、提示词/技能管理路由（requireTeacher）；server 包单测覆盖 401/403/404/400 与事件顺序断言
- [x] 4.2 客户端中断处理：context 取消停止上游读取，已积累文本非空则持久化、为空不落消息；单测模拟取消验证行为
- [x] 4.3 `internal/server/server.go` 装配 TutorService 与新路由，`go test ./internal/server` 与 `go build ./...` 全过

## 5. 前端助手视图

- [x] 5.1 `types.ts`/`api.ts`：Conversation/TutorMessage 类型，`fetch`+ReadableStream 的 SSE 解析（AbortController 中断），会话/消息/配置 API 封装；401 处理沿用 handle401
- [x] 5.2 新建 `AssistantPanel`（新会话+历史列表）与 `TutorView`（消息流增量渲染、meta 引用卡片先行渲染、追问输入、流式期间禁用）；容器内 `npm run build` 成功
- [x] 5.3 教师侧 `AssistantSettings`（提示词编辑+解题引导开关），学生视图不渲染；`npm run build` 与 tsc 无错误
- [x] 5.4 引用紧凑化：`TutorView` 中当前轮与历史消息的引用统一改为紧凑小型跳转卡片，卡片仅显示文件名（单行截断，不显示位置文案、`snippet` 片段与相关度），点击 `onOpenSource` 用载荷中的 locator 定位原文；meta/持久化载荷不变，locator/snippet 字段忽略不展示；`npm run build` 通过
- [x] 5.5 引用按材料去重：渲染前以 `documentId`（缺失时按文件名）为键合并同一材料的多个分片命中，每份材料只显示一张卡片，点击定位到首个命中分片；当前轮与历史消息一致；`npm run build` 通过

## 6. 种子回填与配置检查

- [x] 6.1 确认 `docker-compose.yml`/`.env.example` 无需新增变量，LLM_* 透传现状可用；Nginx 依赖 `X-Accel-Buffering` 关缓冲，E2E 中验证分段到达，若被缓冲则在 nginx.conf `/api/` location 补 `proxy_buffering off`

## 7. E2E 与端到端验证

- [x] 7.1 新建 `deploy/tests/e2e-tutor-matrix.sh`：健康检查、登录取 token、创建会话、SSE 提问断言 meta→delta→done 顺序、引用全属本班、追问两轮后 `GET messages` 断言持久化轮次、学生 403、伪造 class_id 无效、刷新后历史可读；容器网络内运行 19+ 断言全过
- [x] 7.2 全新启动验证：`docker compose down -v && docker compose build --no-cache && docker compose up -d`，teacherA 保存提示词→studentA1 提问（SSE 逐段呈现+引用卡片可回跳预览）→教师改提示词与开关技能→studentA1 再问观察差异→studentB1 同题验证班级隔离
- [x] 7.3 故障演练：停 LLM 网关配置（改 BaseURL 指向不可达地址重启 backend）验证 error 事件/503 友好文案，恢复后验证正常
- [x] 7.4 `openspec validate add-tutor-agent --strict` 退出码 0

## 8. 清理

- [x] 8.1 全局搜索 TODO/FIXME 残留并清理；确认无调试日志遗留
- [x] 8.2 勾选本任务清单全部条目，整理验收证据（单测计数、E2E 输出、浏览器走查结论）
