# Tasks: 知识问答功能

## 1. 配置与基础设施

- [x] 1.1 修改 `backend/internal/config/config.go`，新增 `LLMConfig{BaseURL, APIKey, Model, Timeout}` 字段；从环境变量 `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL`、`LLM_TIMEOUT_SECONDS` 读取；`LLM_BASE_URL/API_KEY/MODEL` 必填；`LLM_TIMEOUT_SECONDS` 默认 30，校验 >= 5。验证：`go test ./internal/config -v` 通过，覆盖缺失 LLM 配置场景。
- [x] 1.2 修改 `docker-compose.yml`，在 backend 服务中新增 `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL`、`LLM_TIMEOUT_SECONDS` 环境变量透传；更新 `.env.example` 添加示例。验证：`docker compose config` 解析无误。

## 2. LLM 客户端（后端）

- [x] 2.1 新建 `backend/internal/llm/client.go`，实现 `LLM` 接口：`Ask(ctx context.Context, prompt string) (string, error)`。使用 net/http POST `{baseURL}/chat/completions`，请求体含 model/messages/temperature/max_tokens；响应取 `choices[0].message.content`。验证：`go test ./internal/llm -v` 通过。
- [x] 2.2 新建 `backend/internal/llm/client_test.go`，使用 httptest 假服务覆盖：成功返回、非 200 错误、JSON 解析失败、choices 为空、请求超时。验证：`go test ./internal/llm -v` 通过。

## 3. QA 编排服务（后端）

- [x] 3.1 新建 `backend/internal/qa/service.go`，实现 `QAService` 接口：`Ask(ctx context.Context, question string, classID int64) (*QAResult, error)`。编排流程：Embed → Search → Prompt 构造 → LLM.Ask → 组装响应。验证：`go test ./internal/qa -v` 通过。
- [x] 3.2 新建 `backend/internal/qa/service_test.go`，mock 三依赖覆盖：正常问答（有召回）、无召回时仍调用 LLM、Embed 失败、Search 失败、LLM 失败。验证：`go test ./internal/qa -v` 通过。
- [x] 3.3 在 `qa/service.go` 中实现 Prompt 构造函数：按文件类型格式化 locator（PDF 页码 / Markdown 章节 / TXT 行号），拼接片段为 `[{fileName} {locator}] {snippet}`，按 score 排序，最多取 topK 条。验证：单元测试断言 Prompt 格式正确。

## 4. 后端接口与路由

- [x] 4.1 修改 `backend/internal/server/server.go`：`API` 结构体新增 `QASvc` 字段；`NewHandler` 新增路由 `POST /api/qa` 并包 `requireAuth`。验证：编译通过。
- [x] 4.2 新建 `backend/internal/server/qa.go`，实现 `qaHandler`：解析 `{"question"}` JSON → 校验（空/超长 400）→ 调用 `QASvc.Ask(ctx, question, u.ClassID)` → 返回 `{"answer", "citations"}`；LLM/Embed/Search 失败统一 503 文案「知识库问答暂不可用」。验证：`go test ./internal/server -v` 通过。
- [x] 4.3 修改 `backend/cmd/server/main.go`：创建 `llm.Client` 与 `qa.Service` 实例并注入 `API`。验证：编译通过，服务可启动。

## 5. 前端 API 层与类型

- [x] 5.1 修改 `frontend/src/types.ts`：新增 `Citation` 与 `QAResult` 接口（与后端 JSON 字段一致）。验证：`tsc --noEmit` 通过。
- [x] 5.2 修改 `frontend/src/api.ts`：新增 `askQuestion(question: string): Promise<QAResult>`，自动携带 Bearer token，401 时清除 token 跳登录。验证：`tsc --noEmit` 通过。

## 6. 前端页面与组件

- [x] 6.1 新建 `frontend/src/components/QAPanel.tsx`：问答输入面板（输入框 + 提问按钮），复用 `SearchPanel` 样式；提交时调用 `onAsk(question)`。验证：构建通过。
- [x] 6.2 新建 `frontend/src/components/QAResults.tsx`：展示回答文本段落与引用卡片列表；引用卡片复用 `result-item` 样式，点击徽标调用 `onOpenSource`。验证：构建通过。
- [x] 6.3 修改 `frontend/src/pages/HomePage.tsx`：左栏新增「知识问答」标签页切换（与「知识库检索」并列）；新增 `qa` 视图状态；`handleAsk` 调用 `askQuestion` 并更新状态；右栏 `view === 'qa'` 时渲染 `QAResults`。验证：构建通过。
- [x] 6.4 修改 `frontend/src/styles.css`：为 `.qa-answer`、`.qa-citations` 等新增样式（保持北大红主题）。验证：构建通过。

## 7. 测试更新

- [x] 7.1 修改 `backend/internal/server/handlers_test.go`：新增 `TestQAUnauthorized`（无 token 401）、`TestQAEmptyQuestion`（空问题 400）、`TestQACrossClass`（A 班用户问答结果引用全部属于 A 班）、`TestQALLMUnavailable`（LLM 失败 503）。验证：`go test ./internal/server -v` 通过。
- [x] 7.2 新建 `deploy/tests/e2e-qa-matrix.sh`：A 班教师提问返回回答 + 引用（引用全部属于 A 班）；B 班学生提问同一问题回答不同；伪造 class_id 无效；未认证 401；空问题 400。验证：全新栈执行脚本全部 PASS。

## 8. 端到端验证

- [x] 8.1 全新启动验证：`docker compose down -v && docker compose up --build -d`，等待健康检查通过；teacherA 登录后提交问题，返回回答与引用，点击引用可打开对应材料预览。验证：浏览器手动走查或 E2E 脚本。
- [x] 8.2 跨班隔离验证：A 班用户问答结果引用全部属于 A 班材料；B 班学生问答结果引用全部属于 B 班材料。验证：E2E 脚本。
- [x] 8.3 故障演练：停止 LLM 网关（或断开外网），问答接口返回 503 友好文案；恢复后问答正常。验证：手动测试或脚本。
- [x] 8.4 运行 `openspec validate add-knowledge-qa --strict`，退出码 0。

## 9. 清理

- [x] 9.1 确认 `internal/llm`、`internal/qa` 包无未使用代码；`grep -r "TODO\|FIXME" backend/internal/llm/ backend/internal/qa/` 为空。验证：无残留。
- [x] 9.2 更新 `openspec/changes/add-knowledge-qa/tasks.md`，勾选所有完成任务。
