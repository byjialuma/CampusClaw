## Context

现状：技能是 `skills` 表的一行（`name`、`description`、`content`、`source`、`builtin`、`enabled`），归属本班助手，经 `agent_skills` 绑定到智能体。运行时由 `AgentConfig` 取出「绑定且启用」的技能，`ParseSkillTools` 解析正文里的「工具：」「适用条件：」行，`RouteSkill` 按关键词计分选一条，`buildSystem` 拼接正文，`runToolPhase` 并发执行工具。

约束：

- 后端仅用 Go 标准库与 `net/http`，不引入 Web 框架；新增能力应尽量不引入第三方依赖。
- `backend/internal/db/schema.sql` 必须与 `deploy/mysql/01_schema.sql` 逐字节一致，迁移需幂等。
- 文件落盘复用 `DATA_DIR=/app/data` 卷（当前材料在 `/app/data/materials`）。
- 全班数据隔离在服务端完成，班级一律取自 JWT claims。
- 一次提问只调用一次 Chat Completions，模型不参与工具选择。

## Goals / Non-Goals

**Goals:**

- 支持 `SKILL.md` + `references/`、`assets/`、`scripts/` 的目录化技能包上传、解析、存储与展示。
- 运行时按需加载：命中后加载正文，`resources` 声明的资源作为数据块进入本轮用户消息。
- 保持现有路由与工具阶段的确定性语义，不改变「代码决定调用」的架构。
- 存量技能（含内置「解题引导」）在升级后统一转换为标准技能包，行为与绑定关系不变。

**Non-Goals:**

- 不做路线 A 的模型自主调用（不引入 `tools` 循环、多步编排、模型选参数）。
- 不执行 `scripts/` 中的代码，也不提供脚本运行沙箱。
- 不做技能包内容的在线编辑（仍只支持启停、删除、重新上传）。
- 不做技能组合、跨轮技能记忆。

## Decisions

### D1：以 zip 作为上传载体

浏览器无法可靠地按目录上传，选择 zip。限制：压缩包 ≤ 512KB，解压后总大小 ≤ 2MB，文件数 ≤ 200，单文件 ≤ 512KB。解压前先 `zip.OpenReader` 读取条目元数据，超限直接拒绝。

备选：单独上传 `SKILL.md` 再逐个上传资源——接口多、事务边界复杂，放弃。

### D2：文件系统存资源，数据库存解析结果

技能包目录为 `<DATA_DIR>/skills/<class_id>/<skill_id>/`。用 `skill_id` 而非名称做目录名，避免中文名、重名、改名带来的路径问题；`name` 归一化后的哈希作为 `slug` 存库，用于同班唯一性校验。

`skills` 表新增：`slug VARCHAR(64) NULL`、`frontmatter JSON NULL`、`dir_path VARCHAR(255) NULL`、`content_hash CHAR(64) NULL`。`content` 继续保存正文，保证既有读取路径与历史快照不受影响；`dir_path` 为空仅作为迁移失败或回滚时的纯文本兜底。

备选：资源整体存 MySQL BLOB——备份与体积都更差，放弃。

### D3：手写最小 frontmatter 解析器，不引入 YAML 依赖

只支持本变更需要的子集：

- `key: value` 标量（值两侧空白裁剪）
- `key: [a, b, c]` 行内数组
- `key:` 后接 `- item` 块数组

支持键：`name`、`description`、`适用条件`、`allowed-tools`、`resources`。未知键忽略但保留在 `frontmatter` JSON 中。非法语法返回带行号的错误。

备选：`gopkg.in/yaml.v3`——功能完整，但与项目「标准库优先」的约束冲突，且本场景语法面很小。

### D4：命中后按声明加载资源，不做懒加载

frontmatter 的 `resources` 是显式白名单：命中技能后按顺序读取，单文件 ≤ 16KB、合计 ≤ 32KB、最多 5 个；超出部分截断并在 SSE `meta` 中标记。未声明资源不读取，避免“命中即全量”。

备选：正文写 `skill_file:path` 由运行时解析——更接近渐进披露，但引入二次解析和条件分支，与路线 B 的“一次调完再生成”不符，本变更不做。

### D5：工具声明双读，校验沿用既有实现

`allowed-tools` 与正文「工具：」行合并去重；前者优先。解析后复用 `validateSkillToolRefs` 的三段校验（服务器存在 → 已连接 → 工具在缓存清单），失败在保存入口返回 400。运行时仍由 `runToolPhase` 并发调用，`knowledge_search` 保持内置语义。

### D6：接口形态

- `POST /api/tutor/assistant/skills/package`：multipart 字段 `file`，仅教师。
- `GET /api/tutor/assistant/skills/{id}`：返回包详情与资源清单（教师）。
- `GET /api/tutor/assistant/skills/{id}/resources?path=...`：读取单个资源（教师，班级校验，路径限制在包内）。
- `GET /api/tutor/assistant`：`skills[]` 增加 `slug`、`description`、`resources` 字段，旧字段保留。

资源读取统一 `Content-Type: text/plain; charset=utf-8`（二进制资源用 `application/octet-stream`），并带 `Cache-Control: no-store`。

### D7：运行时接入点

- `Skill` 结构增加 `DirPath`、`Resources`，`AgentConfig` 查询同步取新列；`dir_path` 为空时正文来自 `content`。
- 命中判定仍在 `RouteSkill`，候选集构造不变。
- `buildSystem` 用包正文替代 `content`（二者对包技能一致）。
- 新增 `loadSkillResources(skill)`：仅在命中且 `resources` 非空时读取，结果拼进本轮用户消息的数据块；读取失败不阻断回答，只在 `meta` 标记。
- 回答落库的技能快照仍只存 `id + name`，不存资源内容。

### D8：脚本只展示不执行

`scripts/` 计入资源清单，读取接口可下载，但没有任何执行入口；服务端进程也不加载其中文件。文档与界面需明确标注“脚本不会被执行”。

### D9：存量技能在启动时回填为包

时机：DB 迁移完成后、HTTP 服务开始监听前执行一次回填，只处理 `dir_path` 为空的行，班级经 `skills.assistant_id → assistants.class_id` 取得。

`SKILL.md` 合成规则：

- `name`：沿用现有 `skills.name`。
- `description`：优先现有 `skills.description`；为空时取正文首个非空行（去掉 Markdown 标题符号与「工具」「适用条件」行）截断到 120 字符；仍为空时用「<名称> 技能」。
- `适用条件`、`allowed-tools`：用既有的 `ParseSkillTools` 从正文解析后写入 frontmatter。
- `resources`：空数组。
- 正文：`skills.content` 原样写入，不删除「工具：」「适用条件：」行——运行时合并去重，保证路由关键词与迁移前完全一致。

写盘与回填：目录为 `<DATA_DIR>/skills/<class_id>/<skill_id>/`，先写临时文件再 rename；成功后回填 `slug`、`frontmatter`、`dir_path`、`content_hash`。`slug` 取规范化名称的哈希，同班冲突时追加 `-<id>`。

容错与幂等：逐条处理，单条失败记 warn 后继续，`dir_path` 保持空、运行时回退读 `content`；重复启动只处理未回填行，已回填但 `SKILL.md` 丢失时重写文件。

备选：读取时懒转换——并发写与首问延迟不可控；独立一次性脚本——部署多一步且容易漏执行。均放弃。

### D10：随变更提供完整示例技能包

为了让「格式是否正确」「运行时是否真能加载」有可执行的判据，而不是只有文字契约，本变更附带一份完整示例技能包「勾股定理解题引导」。它与内置「解题引导」、既有的一元二次与单位换算用例主题均不重叠，同时命中预置 `formula_hint` 工具支持的「勾股 / 直角三角形」主题，保证工具链路能真实验证。源文件放在 `apitest/fixtures/skill-pythagorean-guide/`，接口测试直接读取该目录打包上传，浏览器验收也用同一份源文件，避免示例与用例各写一份而漂移。

目录结构：

```
skill-pythagorean-guide/
├── SKILL.md
├── references/
│   ├── pythagorean-theorem.md
│   └── common-mistakes.md
├── assets/
│   └── answer-template.md
└── scripts/
    └── check_triangle.py
```

`SKILL.md` 全文：

```markdown
---
name: 勾股定理解题引导
description: 面向直角三角形边长与勾股定理应用题提供分步引导，只给思路与下一步提示，不直接给出最终结果
适用条件: [勾股定理, 直角三角形, 斜边, 直角边]
allowed-tools:
  - formula_hint@campus-tools
  - knowledge_search
resources:
  - references/pythagorean-theorem.md
  - references/common-mistakes.md
  - assets/answer-template.md
---

# 勾股定理解题引导

## 何时使用

学生提出与勾股定理、直角三角形边长求解、斜边或直角边计算有关的问题时使用。

## 引导流程

1. 先请学生确认三角形是直角三角形，并指出直角、斜边 c 与两条直角边 a、b。
2. 引导学生判断已知量与未知量：已知两直角边求斜边，还是已知斜边与一直角边求另一直角边。
3. 只提示应使用的变形（c = √(a²+b²) 或 a = √(c²-b²)），不代入数值给出结果。
4. 学生完成后追问是否符合「斜边最长、两边之和大于第三边」，让学生自行检查。
5. 应用题先引导学生画图并标出已知长度，再回到上述步骤。

## 约束

- 不写出最终边长的数值结果或完整解题过程。
- 引用检索材料时用 [n] 标注来源；没有材料时不编造出处。
- `scripts/` 中的脚本仅作示例展示，不执行。
```

资源文件均为纯文本：`references/pythagorean-theorem.md` 说明勾股定理及两种变形，`references/common-mistakes.md` 列出斜边判定、开平方遗漏与单位不一致等常见错误，`assets/answer-template.md` 给出引导式回答的句式骨架，`scripts/check_triangle.py` 仅用于验证「脚本只展示不执行」。

可验证性：

- 格式：`SKILL.md` 覆盖 frontmatter 全部五个键（行内数组与块数组两种写法都出现），目录覆盖三类资源。
- 词法路由：`适用条件` 使用学生提问中会出现的字面词（「勾股定理」「直角三角形」「斜边」），提问「勾股定理中已知两条直角边，斜边怎么求？」必然命中。
- 工具：`formula_hint@campus-tools` 为仓库预置并已连接的演示 MCP 工具，且其主题词覆盖「勾股 / 直角三角形」，上传后工具引用校验与真实调用均可通过。
- 链路：上传 → 列表/详情 → 绑定智能体 → 学生提问 → 正文与资源进入上下文 → `formula_hint` 调用成功 → 回答不含最终结果 → 脚本未执行，全部可在接口测试与浏览器中观察。

## Risks / Trade-offs

- [手写 frontmatter 解析器覆盖不全] → 只承诺已文档化的子集，遇到不支持语法直接报错而不是静默忽略；单测覆盖边界。
- [zip 炸弹与路径穿越] → 先读条目元数据做体积/数量限制；解压时逐条 `filepath.Clean` + 前缀校验，拒绝符号链接与绝对路径；失败清理临时目录。
- [资源导致上下文膨胀] → 单文件/总量/数量三重上限，超限截断并在状态中可见。
- [同名技能包冲突] → `slug` 同班唯一，冲突返回 409 而非覆盖。
- [历史快照只存 id+name] → 删除技能包后历史仍可展示名称，但无法回看资源；属既有行为，不在本变更扩展。
- [批量回填放大启动耗时] → 只处理 `dir_path` 为空的行，单条仅一次小块写入；逐条容错，失败不阻断启动。
- [迁移只增列不删列] → 回滚时保留新增列与目录，停用上传入口即可恢复旧行为。

## Migration Plan

1. `schema.sql` 与 `deploy/mysql/01_schema.sql` 同步新增四列，`ALTER TABLE` 走既有幂等添加逻辑（忽略列已存在）。
2. 后端启动时在 DB 迁移之后执行存量回填：为每个 `dir_path` 为空的技能生成包目录与 `SKILL.md` 并回填字段；单条失败只记录日志，技能继续按纯文本工作，下次启动自动重试。
3. 部署顺序：数据库列变更 → 后端（含回填） → 前端。回滚只需回退镜像，新增列与已生成目录对旧代码无影响，`content` 未被改写。
4. 目录 `<DATA_DIR>/skills/` 首次上传或回填时按需创建，权限沿用容器内运行用户。

## Open Questions

- 资源是否需要支持二进制类型（如 PDF 模板）？当前按文本处理，二进制以 `application/octet-stream` 下载，不进入上下文。可在后续按需扩展。
