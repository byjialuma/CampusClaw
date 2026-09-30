# 设计：add-tutor-agent（解题助手智能体）

## 1. 总体结构

复用既有组件：JWT 认证（`internal/auth`）、Embedding 与向量检索（`internal/embedding`、`internal/vector`）、MySQL 存储模式（`internal/store`）、LLM 网关配置（`cfg.LLM`）。新增：

```
internal/tutor/            # 编排服务：提示词/技能加载 → 历史组装 → 检索 → Prompt → 流式生成
  service.go               # TutorService.AskStream(...)  编排 + SSE 事件回调
  service_test.go
  repository.go            # 助手配置/会话/消息的 MySQL 读写（实现 internal/server 所需接口）
  repository_test.go
internal/llm/stream.go     # 流式 Chat Completions 客户端（stream=true），增量回调
internal/server/tutor.go   # 路由 handler：会话 CRUD、SSE 提问、提示词/技能管理
deploy/tests/e2e-tutor-matrix.sh
```

前端新增：`AssistantPanel`（左栏入口：新会话 + 历史会话列表）、`TutorView`（右栏：消息流增量渲染 + 仅含文件名的紧凑引用跳转卡片 + 追问输入）、教师侧 `AssistantSettings`（提示词编辑 + 解题引导开关）。

## 2. 数据库变更（幂等）

```sql
ALTER TABLE assistants ADD COLUMN system_prompt TEXT NULL;          -- 教师编写的提示词
ALTER TABLE skills     ADD COLUMN enabled    TINYINT(1) NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS conversations (
  id             BIGINT NOT NULL AUTO_INCREMENT,
  class_id       BIGINT NOT NULL,
  user_id        BIGINT NOT NULL,          -- 所属学生（创建者）
  title          VARCHAR(255) NOT NULL,    -- 首问截断生成
  created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_conversations_user (user_id, id),
  CONSTRAINT fk_conversations_user  FOREIGN KEY (user_id)  REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_conversations_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS messages (
  id              BIGINT NOT NULL AUTO_INCREMENT,
  conversation_id BIGINT NOT NULL,
  role            ENUM('user','assistant') NOT NULL,
  content         MEDIUMTEXT NOT NULL,      -- assistant 轮为完整回答文本
  citations       JSON NULL,                -- assistant 轮为引用数组；user 轮为 NULL
  prompt_used     TEXT NULL,                -- 该轮实际使用的提示词快照
  skill_enabled   TINYINT(1) NULL,          -- 该轮解题引导是否启用
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_messages_conversation (conversation_id, id),
  CONSTRAINT fk_messages_conversation FOREIGN KEY (conversation_id) REFERENCES conversations (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

`db.EnsureSchema` 增加对应幂等逻辑：`ALTER` 前查 `INFORMATION_SCHEMA.COLUMNS` 判断列是否存在，避免重复执行报错。会话归属校验在 SQL 层带 `user_id` 条件（`WHERE id = ? AND user_id = ?`），他人会话一律按不存在处理（404）。

## 3. 种子数据

`internal/seed` 为每个班级创建一个助手（name=解题助手），并挂一个内置技能「解题引导」：

- `system_prompt` 播种一条合理的默认角色提示词（如「你是本班解题助教，结合班级材料引导学生自主解题，用中文，简洁准确」），教师可随时改写——保证功能开箱可用，同时满足「由教师编写并保存」的管理权约束。
- 「解题引导」技能默认 `enabled = 1`，教师可停用。

## 4. 流式 LLM 客户端（internal/llm/stream.go）

新增 `StreamAsk(ctx, messages []chatMessage, onDelta func(string) error) (string, error)`：

- 请求体与现有 `chatRequest` 相同，追加 `"stream": true`。
- 响应 `Content-Type` 为 `text/event-stream` 时，按行解析 `data: {...}` 帧，累加 `choices[0].delta.content`，每收到非空增量即回调 `onDelta` 并由调用方决定推送；`data: [DONE]` 结束。
- 上游未按 SSE 返回（网关不支持流式时可能整包返回）：降级为整体读取，取 `choices[0].message.content` 作为单次增量回调，SSE 对外契约不变。
- 错误处理沿用 `client.go` 模式：先读 body 再判状态码；中途断流返回错误，由编排层决定已积累文本的处理。

## 5. Tutor 编排（internal/tutor/service.go）

```go
type TutorService struct {
    PromptRepo PromptRepo    // 加载助手提示词与技能状态
    History    HistoryRepo   // 读写会话消息
    Embedder   embedding.Embedder
    Vectors    vectorSearcher  // 复用 vector.Client.Search(queryVec, classID, topK)
    LLM        StreamLLM
    TopK       int
}

func (s *TutorService) AskStream(ctx context.Context, in AskInput) (*AskOutput, error)
```

`AskInput{ConversationID, UserID, ClassID, Question}`；`AskOutput{Citations, SkillEnabled, Answer(完整文本), MessageID}`；通过 `in.OnEvent(Event)` 回调推送 SSE 事件（事件类型见 §6）。

编排步骤（对应规格「一次提问走完」）：

1. **检索**：`Embedder.Embed(question)` → `Vectors.Search(vec, classID, TopK)` → 过滤 `score <= 0`。班级只来自 JWT claims（调用前由 handler 从 `auth.CurrentUser` 取出，编排层不接触请求参数）。
2. **加载提示词**：按 classID 查 `assistants.system_prompt`；为空（教师未改过且播种失败）时用内置兜底提示词。
3. **加载技能**：查「解题引导」的 `enabled`。启用时向 system 附加技能指令：「学生正在解题，请给出解题思路与步骤引导，引用材料中的相关内容（含材料标题与位置），不要直接给出该题的最终答案」。停用时**不附加**该指令——回答风格由提示词单独决定，与开启时形成明显差异。
4. **组装材料上下文**：命中时按 `[文件名 定位] 片段` 列出分片；**无命中时不跳过生成**（助手需保持对话连贯），而是在 system 中注明「知识库中未找到与本题相关的内容，回答时必须明确说明'资料中未找到'，不得引用或编造任何材料出处」，`citations` 为空数组。
5. **组装消息序列**：`system`（提示词 + 技能指令 + 材料上下文）→ 会话历史（`messages` 表按时间升序取 user/assistant 轮，assistant 轮仅带回答文本不带引用）→ 本次提问（user）。
6. **流式生成**：`LLM.StreamAsk(...)`，`onDelta` 透传为 SSE `delta` 事件；结束后将完整回答 + 引用 + 提示词快照 + 技能状态持久化为 assistant 消息。
7. **标题**：会话首问时以问题前 30 字生成 `conversations.title`。

**关键决策**：无命中仍调用 LLM（区别于知识问答接口的短路固定句），因为助手是多轮对话角色，需承接追问；防编造通过 system 注记 + citations 强制为空实现，与规格「无命中时说明资料中未找到、不编造出处」一致。两套接口（`/api/qa` 与 `/api/tutor/*`）行为各自独立，互不影响。

## 6. SSE 协议与路由

后端响应头：`Content-Type: text/event-stream`、`Cache-Control: no-store`、`X-Accel-Buffering: no`（Nginx 默认尊重该头关闭代理缓冲，无需改 nginx.conf；实施时以 E2E 验证逐段到达）。每个事件写后立即 `http.Flusher.Flush()`。

事件序列：

```
event: meta    data: {"citations":[...],"skillEnabled":true}     # 生成前先推引用（载荷结构不变，仍含 locator 定位字段；前端仅渲染只显示文件名的小型跳转卡片，不展示位置文案/片段/相关度）
event: delta   data: {"text":"..."}                               # 逐段文本，可多次
event: done    data: {"messageId":123}                            # 持久化完成后
event: error   data: {"error":"助手暂不可用，请稍后再试"}          # 任一环节失败，此后关闭流
```

Embedding/检索失败发生在 `meta` 之前 → 直接 HTTP 503 JSON（与现有错误风格一致）；生成中途失败 → 已推 `meta` 则走 `error` 事件。

路由（`internal/server/tutor.go`，全部经 `requireAuth`）：

| 路由 | 权限 | 说明 |
|---|---|---|
| `POST /api/tutor/conversations` | 登录 | 创建会话（user_id、class_id 取自 claims） |
| `GET /api/tutor/conversations` | 登录 | 本人会话列表（`WHERE user_id = ?`） |
| `GET /api/tutor/conversations/{id}/messages` | 登录 + 属主 | 他人会话返回 404 |
| `POST /api/tutor/conversations/{id}/messages` | 登录 + 属主 | SSE 流式提问（body: `{question}`，≤500 字校验同知识问答） |
| `GET /api/tutor/assistant` | 教师 | 读取本班助手提示词与技能状态 |
| `PUT /api/tutor/assistant/prompt` | `requireTeacher` | 保存提示词（学生 403） |
| `PUT /api/tutor/assistant/skills/{id}` | `requireTeacher` | 启用/停用解题引导（学生 403） |

前端不用 `EventSource`（无法携带 `Authorization` 头），改用 `fetch` + `ReadableStream` 手工解析 SSE 帧；`api.ts` 新增 `askTutor(...)`（返回事件迭代器）、会话/消息/配置三组函数；中断用 `AbortController`。

**客户端中断处理**：请求 context 取消时停止上游读取与推送；已积累文本非空则照常持久化为该轮回答（引用已随 `meta` 送达并已含在持久化中），文本为空则不落 assistant 消息（该轮视为失败）。

## 7. 前端视图

- HomePage 增加第四种视图 `tutor`；左栏 `AssistantPanel`：「新会话」按钮 + 历史会话列表（title + 时间），点击切换加载消息。
- 右栏 `TutorView`：消息气泡流（user 右对齐、assistant 左对齐），当前回答区随 `delta` 事件增量渲染（追加文本节点即可，不引入 Markdown 渲染依赖，与知识问答纯文本风格一致）；`meta` 事件到达即在消息下方渲染引用区——渲染前先按材料去重：以 `documentId` 为键（缺失时退化为文件名），同一材料的多个分片命中只保留第一条（检索结果按相关度降序，首条即最相关命中）；每份材料渲染一张紧凑小型跳转卡片，卡片内仅显示文件名（单行截断），不渲染 locatorLabel 位置文案、`snippet` 片段与相关度；卡片可复用 `locator-badge`/`result-item` 的小尺寸变体，点击走 `onOpenSource`，用保留条目的 locator 打开预览并定位到首个命中位置（当前轮与历史消息中的引用渲染方式一致，历史数据载荷中的重复条目同样去重、locatorLabel/snippet 忽略不展示）；底部追问输入框 + 发送按钮，流式期间禁用并显示「思考中…」。
- 教师登录时，右栏顶部显示 `AssistantSettings` 卡片：提示词 `textarea`（保存调 PUT prompt）+「解题引导」开关（调 PUT skills）。学生不渲染该卡片（服务端 403 是最终防线）。

## 8. 测试设计

| 层 | 用例要点 |
|---|---|
| `internal/llm` | SSE 帧解析（多 delta、[DONE]）、上游非流式降级、非 200、中途断流、context 取消 |
| `internal/tutor` | 编排顺序与组装：提示词加载（空则兜底）、技能开/关 system 指令差异、命中映射 citations、无命中注记+空引用、历史按序带入、无命中仍调用 LLM、失败回调 |
| `internal/server` | 401（全部路由）、学生改提示词/开关技能 403、他人会话 404、提问入参校验 400、SSE 事件序列断言（`httptest.ResponseRecorder` 实现 Flusher，可读完整 body 断言 meta 在 delta 之前、done 在最后）、流式后消息已持久化 |
| 仓储 | EnsureSchema 幂等（重复执行不报错）、会话属主过滤、引用 JSON 序列化往返 |
| E2E `e2e-tutor-matrix.sh` | 健康检查、登录取 token、创建会话、SSE 提问（解析 meta/delta/done 顺序）、引用全部属本班、追问（两轮后 `GET messages` 断言轮次与持久化）、学生 403、伪造 class_id 无效、刷新后历史可读 |

技能开/关导致的「回答方式明显不同」属模型行为，无法在 E2E 中确定性断言，由 `internal/tutor` 单测在 system 消息构造层断言（开启含引导指令与「不给最终答案」约束，关闭不含），E2E 只断言开关接口的 200/403 与状态持久化。

## 9. 配置与部署

- 无新增环境变量：LLM 网关、Embedding、TopK 全部沿用现有配置。
- `docker-compose.yml` 不新增容器；backend 镜像重建即生效。
- Nginx 不改配置（依赖后端 `X-Accel-Buffering: no` 头关闭缓冲）；E2E 用 curl 以非缓冲模式验证分段到达，若实测被缓冲则退而在 nginx.conf 的 `/api/` location 显式加 `proxy_buffering off`（任务清单中列为验证项）。

## 10. 安全边界

- 班级来源唯一：claims → handler → 编排层参数，请求体中的班级字段一律忽略。
- 会话属主校验在 SQL 条件中完成，杜绝水平越权。
- 提示词/技能变更仅教师，服务端强制；学生前端无控件。
- API Key 仅服务端持有；SSE 错误事件与 503 均不含内部地址。
- 持久化内容为模型输出与检索结果，无用户敏感信息新增暴露面；SSE 响应 `Cache-Control: no-store`。
