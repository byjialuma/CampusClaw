## Why

当前「技能」只是数据库里的一段纯文本：工具声明和适用条件混写在正文中，既不能携带参考资料、模板等资源，也不符合教师熟悉的 Skill 目录写法。教师需要按 `SKILL.md` + 资源的目录形式编写与分发技能，同时在保持现有确定性路由的前提下，让资源按需加载而不是一次性全塞进提示词。

## What Changes

- **技能包上传**：教师上传 zip，内含 `SKILL.md` 与可选的 `references/`、`assets/`、`scripts/`；服务端解压校验（拒绝路径穿越、符号链接、超限文件）后落盘到按班级隔离的目录。
- **`SKILL.md` 解析**：读取 YAML frontmatter（`name`、`description`、`适用条件`、`allowed-tools`、`resources`）与正文；frontmatter 优先，正文的「工具：」行保留为兼容写法。
- **数据模型扩展**：`skills` 表新增 `slug`、`frontmatter`、`dir_path`、`content_hash`，`description` 取自 frontmatter。
- **存量技能迁移**：升级后首次启动自动把现有技能（含内置「解题引导」）转换为标准技能包——生成带 `name`、`description`、`适用条件`、`allowed-tools` frontmatter 的 `SKILL.md` 并落盘，回填包目录与内容哈希；迁移幂等、逐条容错，保留正文、启停状态、内置标记与智能体绑定，未成功转换的技能继续按纯文本工作。
- **列表与详情**：技能列表返回包元数据与资源清单；新增资源读取接口，鉴权、班级隔离、路径穿越防护缺一不可。
- **运行时加载（路线 B）**：候选集与关键词路由不变，一轮至多命中一条技能；命中后读取 `SKILL.md` 正文拼入 system prompt，frontmatter `resources` 声明的资源在大小上限内作为数据块拼入本轮用户消息；工具阶段仍是代码一次性并发调用，模型不选工具。
- **完整示例技能包**：随变更提供一份可直接上传的示例技能包（「勾股定理解题引导」，与一元二次、单位换算均不重叠），目录含 `SKILL.md`、`references/`、`assets/`、`scripts/`，frontmatter 覆盖 `name`、`description`、`适用条件`、`allowed-tools`、`resources` 全部字段；同一份源文件同时作为接口测试 fixture 与端到端验收对象，验证上传、列表展示、路由命中、正文与资源加载、工具调用、脚本不执行的完整链路。
- **脚本不执行**：`scripts/` 仅作展示与下载，不提供本地执行能力；需要脚本能力时封装为 MCP 工具。

无破坏性变更：旧接口与存量纯文本技能继续可用。

## Capabilities

### New Capabilities

- `skill-package`: 技能包的目录结构与 `SKILL.md` 契约、上传解析与校验、班级隔离存储、资源清单与读取、运行时按需加载、以及纯文本技能的兼容与迁移。

### Modified Capabilities

（无。本仓库 `openspec/specs/` 尚无已归档能力，技能行为通过新增 `skill-package` 能力描述。）

## Impact

- **后端**：`internal/tutor`（Skill 模型、技能包解析与仓储、存量技能回填）、`internal/server/tutor.go`（上传/详情/资源读取）、`internal/server/mcp.go`（工具引用校验接入 frontmatter）、`internal/db`（表结构与幂等迁移）、启动流程（回填器调用）。
- **前端**：`SkillManagement`（zip 上传、包信息与资源展示）、`api.ts`、`types.ts`。
- **接口**：新增技能包上传与资源读取接口；`GET /api/tutor/assistant` 的 `skills[]` 增加包字段，旧字段保留。
- **存储**：复用 `/app/data` 卷，新增 `/app/data/skills/<class_id>/<skill_id>/` 子目录。
- **文档与测试**：`openapi.json` 补新接口；`apitest` 增加上传、越权、路径穿越与运行时加载用例；示例技能包源文件位于 `apitest/fixtures/skill-pythagorean-guide/`，接口用例与端到端验收共用。
- **依赖**：无新增第三方依赖，zip 解析使用 Go 标准库。
