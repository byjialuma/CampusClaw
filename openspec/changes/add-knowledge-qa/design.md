## 总体设计

知识问答功能复用现有语义检索的向量化 + Qdrant 召回能力，在此基础上新增一个 Chat Completions 客户端和一个问答编排服务。后端保持「向量化 → 召回 → 大模型生成」三步在同一请求内同步完成，前端复用现有组件模式（输入区 + 结果展示 + 来源徽标）。

## 后端包结构

```
internal/
  config/config.go              ← 新增 LLMConfig 字段（BaseURL/APIKey/Model/Timeout）
  llm/
    client.go                   ← Chat Completions 客户端（net/http，不引入 SDK）
    client_test.go              ← httptest 假服务覆盖成功/失败/超时/非 JSON
  qa/
    service.go                  ← 问答编排：Embed → Search → LLM → Answer
    service_test.go             ← 三依赖全 mock：embedder stub、vector mock、llm fake
  server/
    qa.go                       ← POST /api/qa 处理器
    server.go                   ← API 结构体新增 QASvc 字段
  server/handlers_test.go       ← 新增 /api/qa 401/400/跨班/LLM 故障场景
```

## LLM 客户端设计

### 接口

```go
// LLM 生成自然语言回答。
type LLM interface {
    Ask(ctx context.Context, prompt string) (string, error)
}
```

### openAIChatClient

- 复用与 `embedding/openai.go` 相同的 OpenAI 兼容协议模式：POST `{baseURL}/chat/completions`，`Authorization: Bearer {apiKey}`，JSON 请求体。
- 请求体：
  ```json
  {
    "model": "course-chat",
    "messages": [
      {"role": "system", "content": "你是一个班级知识库助教。你只能基于下面提供的材料片段回答问题。如果片段不足以回答，请明确说明'根据已有材料无法回答该问题'。回答必须基于中文。"},
      {"role": "user", "content": "问题：{question}\n\n材料片段：\n1. [文件A.txt 第1-5行] ...\n2. [文件B.pdf 第2页] ..."}
    ],
    "temperature": 0.3,
    "max_tokens": 1024
  }
  ```
- 响应解析：取 `choices[0].message.content` 为回答文本。
- 错误处理：非 200 时解析 `error.message`；JSON 解析失败、choices 为空均返回错误。
- 超时：30 秒（可配置 `LLM_TIMEOUT_SECONDS`，默认 30）。

### Prompt 构造策略

- 每条片段格式：`[{fileName} {locator}] {snippet}`
- locator 按文件类型：PDF → `第 X 页`，Markdown → `章节: X > Y`，TXT → `第 X-Y 行`
- 片段按相似度排序，最多 5 条（`SEARCH_TOP_K`）。
- 若召回结果为空（无切片），不构造 Prompt、不调用 LLM，直接返回固定句「根据已有材料无法回答该问题」与空引用列表。

## QA 服务编排设计

### 接口

```go
type QAService interface {
    Ask(ctx context.Context, question string, classID int64) (*QAResult, error)
}

type QAResult struct {
    Answer    string       `json:"answer"`
    Citations []Citation   `json:"citations"`
}

type Citation struct {
    DocumentID int64       `json:"documentId"`
    FileName   string      `json:"fileName"`
    FileType   string      `json:"fileType"`
    Snippet    string      `json:"snippet"`
    Locator    model.Locator `json:"locator"`
    Score      float32     `json:"score"`
}
```

### 编排流程

1. **校验**：空/超长问题提前返回错误（与检索接口共用同一校验函数）。
2. **向量化**：调用 `Embedder.Embed(ctx, []string{question})`。
3. **召回**：调用 `vectorSearcher.Search(ctx, vec, classID, topK)`。
4. **过滤**：score <= 0 的命中剔除（与检索一致）。
5. **判断无切片**：若过滤后分片为空，跳过步骤 6-7，直接返回固定句「根据已有材料无法回答该问题」与空引用列表。
6. **生成 Prompt**：将过滤后的片段构造为 LLM prompt。
7. **生成回答**：调用 `LLM.Ask(ctx, prompt)`。
8. **构造响应**：LLM 返回文本 + 引用列表（从检索结果映射，每条引用含 `documentId`、`fileName`、`fileType`、`snippet`（分片文本）、`locator`、`score`）。

### 错误处理

| 阶段 | 错误 | 行为 |
|---|---|---|
| Embedding | 失败 | 返回 503，文案「知识库问答暂不可用，Embedding 服务异常」 |
| Search | 失败 | 返回 503，文案「知识库问答暂不可用，Search 服务异常」 |
| LLM | 失败 | 返回 503，文案「知识库问答暂不可用，LLM 服务异常」 |
| 无召回 | 无片段 | 不调用 LLM，直接返回固定句「根据已有材料无法回答该问题」与空引用列表 |

## 配置设计

### 新增环境变量

| 变量 | 说明 | 默认值 | 必填 |
|---|---|---|---|
| `LLM_BASE_URL` | Chat Completions 端点基础 URL | — | ✅ |
| `LLM_API_KEY` | API Key | — | ✅ |
| `LLM_MODEL` | 模型名称 | — | ✅ |
| `LLM_TIMEOUT_SECONDS` | 请求超时（秒） | 30 | ❌ |

### config.go 变更

- `Config` 新增 `LLM LLMConfig`
- `LLMConfig{BaseURL, APIKey, Model, Timeout time.Duration}`
- `Load()` 中加载并校验（`LLM_BASE_URL/API_KEY/MODEL` 必填；`Timeout` 默认 30 秒且 >=5 秒）
- `LLM_BASE_URL` 末尾 `/` 去除（与 embedding 一致）

## 路由设计

- `POST /api/qa` —— 受 `requireAuth` 保护
  - 请求体：`{"question": "..."}`
  - 响应体：`{"answer": "...", "citations": [...]}`
  - 400：空/超长问题
  - 401：无有效 token
  - 503：Embedding/Search/LLM 任一失败

## 前端设计

### 新增组件

- `QAPanel`：问答输入面板（复用 `SearchPanel` 样式，按钮文案改为「提问」）
- `QAResults`：问答结果展示（回答文本 + 引用卡片列表）
  - 回答文本区：`className="qa-answer"`，`<p>` 包裹，保留换行
  - 引用卡片：复用 `SearchResults` 中的 `result-item` 样式，显示文件名 + 分片文本内容（snippet）+ locator 徽标
  - 点击徽标：调用 `handleOpenSource` 打开材料预览（复用现有逻辑）
- `HomePage`：在左栏新增「知识问答」标签页切换（与「知识库检索」并列）

### API 层

- `api.ts` 新增 `askQuestion(question: string): Promise<QAResult>`
- 类型新增 `QAResult` / `Citation`（复用后端命名）
- 401 统一处理（已存在）

### 状态管理

```
view: 'preview' | 'results' | 'qa'
qaQuery: string
qaAnswer: string
qaCitations: Citation[]
qaLoading: boolean
qaError: string
```

## 测试设计

### 单元测试

| 包 | 测试 | 覆盖 |
|---|---|---|
| `internal/llm` | `TestAskSuccess`、`TestAskNon200`、`TestAskInvalidJSON`、`TestAskTimeout` | httptest 假服务 |
| `internal/qa` | `TestAskWithHits`、`TestAskNoHits`（无切片时不调用 LLM，返回固定句+空引用）、`TestAskEmbedFail`、`TestAskSearchFail`、`TestAskLLMFail` | mock embedder/vector/llm |
| `internal/server` | `TestQAUnauthorized`、`TestQAEmptyQuestion`、`TestQACrossClass`、`TestQALLMUnavailable` | fakeJWT + fakeQA |
| `internal/config` | `TestLoad_MissingLLMBaseURL`、`TestLoad_MissingLLMAPIKey`、`TestLoad_LLMBadTimeout` | — |

### E2E 测试

- `e2e-qa-matrix.sh`：
  - A 班教师提问 → 返回回答 + 引用（引用全部属于 A 班，每条引用含 fileName + snippet + locator）
  - B 班学生提问同一问题 → 回答不同（或无法回答）
  - 伪造 class_id 参数无效
  - 未认证 401
  - 空问题 400

## 部署设计

### docker-compose.yml

- backend 环境变量新增：
  ```yaml
  LLM_BASE_URL: ${LLM_BASE_URL}
  LLM_API_KEY: ${LLM_API_KEY}
  LLM_MODEL: ${LLM_MODEL}
  LLM_TIMEOUT_SECONDS: ${LLM_TIMEOUT_SECONDS:-30}
  ```

### .env.example

```
# 大模型对话配置（知识问答使用）
LLM_BASE_URL=https://ai-gateway.devops.hello1023.com/v1
LLM_API_KEY=replace-with-your-llm-api-key
LLM_MODEL=course-chat
LLM_TIMEOUT_SECONDS=30
```

## 安全与边界

- API Key 仅存在于后端环境变量，前端不可见
- 问答流程中的 `class_id` 仅取自 JWT claims，不读请求参数
- 召回片段 score <= 0 时剔除，避免引入无关内容
- LLM prompt 中明确约束「只能基于提供的材料回答」，降低幻觉风险
- 超时 30 秒防止 LLM 网关慢响应拖垮服务
- 错误响应不泄露内部服务地址（延续检索接口的 `searchUnavailableMessage` 模式）
