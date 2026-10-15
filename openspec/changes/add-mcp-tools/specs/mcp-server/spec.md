## Purpose

定义本仓自建 MCP 工具服务的对外契约：以 HTTP JSON-RPC 2.0 端点对外提供工具能力，用 Bearer 令牌鉴权，支持主机侧完成握手、列出工具与调用工具，供 CampusClaw 登记、发现并经技能触发调用。

## ADDED Requirements

### Requirement: MCP 服务以 HTTP JSON-RPC 2.0 暴露工具能力

服务 MUST 提供 `POST /mcp` 端点，按 JSON-RPC 2.0 协议接受请求并返回响应，且 MUST 至少实现 `initialize`、`tools/list`、`tools/call` 三个方法。`initialize` MUST 返回协议版本与服务器标识；`tools/list` MUST 返回服务当前暴露的全部工具及其名称、说明与入参 schema；`tools/call` MUST 按工具名执行并返回工具结果。服务 MUST 另提供 `GET /health` 健康检查端点，返回 200 且无需鉴权。请求体不是合法 JSON 时，服务 MUST 返回 JSON-RPC 解析错误；方法不存在时，服务 MUST 返回 JSON-RPC 方法未找到错误（-32601）；错误响应 MUST 是可读的 JSON-RPC 结构，MUST NOT 返回 HTML 或空响应体。

#### Scenario: 握手返回协议版本与服务器信息

- **WHEN** 客户端以正确令牌向 `POST /mcp` 发送 `initialize` 请求
- **THEN** 响应为 JSON-RPC 2.0 结果，包含协议版本与服务器名称、版本信息

#### Scenario: 列出工具返回完整清单

- **WHEN** 客户端以正确令牌发起 `tools/list`
- **THEN** 响应结果中的 `tools` 数组包含服务暴露的全部工具，每项含名称、说明与入参 schema

#### Scenario: 调用工具返回文本结果

- **WHEN** 客户端以正确令牌发起 `tools/call`，工具名与入参均有效
- **THEN** 响应结果为工具执行产出的文本内容，且带有表示成功的标记

#### Scenario: 非法请求返回可读的 JSON-RPC 错误

- **WHEN** 客户端提交无法解析为 JSON 的请求体，或调用未实现的方法
- **THEN** 服务返回对应 JSON-RPC 错误码与可读错误信息，响应体为 JSON，且不暴露内部堆栈

#### Scenario: 健康检查无需鉴权

- **WHEN** 任何人访问 `GET /health`
- **THEN** 返回 HTTP 200

### Requirement: MCP 服务仅接受持有正确 Bearer 令牌的调用

除健康检查外的全部端点 MUST 校验 `Authorization: Bearer <令牌>`：令牌正确时放行，缺失或错误时 MUST 返回 HTTP 401 且 MUST NOT 执行任何工具逻辑。令牌 MUST 只从服务端环境变量读取，MUST NOT 出现在代码、镜像层、日志或任何响应体中。

#### Scenario: 无令牌被拒绝

- **WHEN** 请求未携带 `Authorization` 头访问 `POST /mcp`
- **THEN** 服务返回 HTTP 401，且不返回任何工具结果

#### Scenario: 错误令牌被拒绝

- **WHEN** 请求携带与配置不符的 Bearer 令牌访问 `POST /mcp`
- **THEN** 服务返回 HTTP 401，且不返回任何工具结果

#### Scenario: 正确令牌放行

- **WHEN** 请求携带与环境变量一致的 Bearer 令牌访问 `POST /mcp`
- **THEN** 服务正常处理请求并返回 JSON-RPC 结果

### Requirement: 内置工具可通过 tools/list 发现并可按名调用

服务 MUST 暴露两个内置工具：`formula_hint`（按入参中的查询文本给出公式与其分步提示）与 `unit_convert`（按入参中的查询文本给出单位换算结果）。工具名 MUST 满足主机侧引用规则——以小写字母开头、仅含小写字母、数字与下划线、不含连字符。两个工具 MUST 各有一个必填的字符串入参承载查询文本，且 MUST 出现在 `tools/list` 返回值中。工具收到无法解析的查询文本时，MUST 返回可读的说明文本（说明用法与可识别的输入示例），MUST NOT 返回 HTTP 5xx 或空结果。

#### Scenario: 公式提示工具给出分步提示

- **WHEN** 调用 `formula_hint` 且查询文本包含该工具可识别的公式主题词
- **THEN** 返回文本包含对应公式与分步解题提示

#### Scenario: 单位换算工具给出换算结果

- **WHEN** 调用 `unit_convert` 且查询文本包含可识别的数值与单位换算请求
- **THEN** 返回文本包含换算后的数值与单位

#### Scenario: 无法解析的查询返回用法说明

- **WHEN** 调用任一内置工具但查询文本不含任何可识别的主题词或数值
- **THEN** 返回文本为可读的用法说明，响应仍为成功状态而非 5xx

#### Scenario: 两个工具均可被列出与调用

- **WHEN** 客户端执行 `tools/list` 后，以清单中的工具名逐一执行 `tools/call`
- **THEN** 两个工具名都能被列出，且各自都能返回文本结果

### Requirement: MCP 服务仅在内部网络可达

MCP 服务 MUST 与主应用处于同一容器网络并可由主应用按服务名访问，MUST NOT 向宿主机发布端口，MUST NOT 直接对最终用户暴露。

#### Scenario: 宿主无法直连而主应用可访问

- **WHEN** 从宿主机尝试直接访问 MCP 服务端口，与从主应用容器内按服务名访问该服务
- **THEN** 宿主机侧无监听端口可连通，主应用容器内可正常完成握手与工具调用
