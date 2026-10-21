## Purpose

让教师以业界通用的目录形式（`SKILL.md` + 资源文件）编写和分发技能，并在保持确定性关键词路由的前提下，让技能正文与声明的参考资料按需进入本轮回答上下文。

## ADDED Requirements

### Requirement: 技能包目录结构

系统 SHALL 接受以目录形式组织的技能包：根目录必须包含 `SKILL.md`，可选包含 `references/`、`assets/`、`scripts/` 子目录及其中的文件。`SKILL.md` MUST 由一个可选的 YAML frontmatter 与 Markdown 正文组成；frontmatter 至少包含 `name` 与 `description`，可选 `适用条件`、`allowed-tools`、`resources`。

#### Scenario: 合法技能包被接受

- **WHEN** 教师上传一个根目录含 `SKILL.md`、且 frontmatter 声明了 `name` 与 `description` 的压缩包
- **THEN** 系统解析成功，技能列表可看到该技能包的名称与描述

#### Scenario: 缺少 SKILL.md 被拒绝

- **WHEN** 上传的压缩包根目录没有 `SKILL.md`
- **THEN** 系统拒绝创建，并返回可读错误说明缺少 `SKILL.md`

#### Scenario: frontmatter 缺少必填字段被拒绝

- **WHEN** `SKILL.md` 的 frontmatter 缺少 `name` 或 `description`
- **THEN** 系统拒绝创建，并指出缺失的字段

### Requirement: 技能包上传与校验

系统 SHALL 仅接受 zip 格式的技能包，并在解压前完成体积与文件数限制校验，在解压时拒绝绝对路径、包含 `..` 的路径、符号链接与超限文件。技能包内所有文本文件 MUST 为 UTF-8。`name` 与 `description` 有长度上限，`name` 归一化后生成的标识在本班范围内 MUST 唯一。

#### Scenario: 路径穿越被拒绝

- **WHEN** 压缩包内存在 `../evil.md` 或绝对路径条目
- **THEN** 系统拒绝整个技能包，且不向磁盘写入任何文件

#### Scenario: 超限体积被拒绝

- **WHEN** 压缩包解压后总大小或文件数超过配置上限
- **THEN** 系统拒绝创建并返回可读错误，已写入的临时文件被清理

#### Scenario: 同班重名冲突

- **WHEN** 教师上传的技能包与本班已有技能包的标识相同
- **THEN** 系统返回冲突错误，不覆盖已有技能包

### Requirement: 班级隔离与角色权限

技能包的创建、删除与资源读取 MUST 仅限教师；班级 MUST 取自 JWT claims。跨班读取资源 MUST 返回 404，学生调用管理类接口 MUST 返回 403。技能包目录不得通过静态文件服务直接暴露。

#### Scenario: 学生上传被拒绝

- **WHEN** 学生调用技能包上传接口
- **THEN** 系统返回 403，且不创建任何技能包

#### Scenario: 跨班读取资源被拒绝

- **WHEN** 教师 A 请求教师 B 班级技能包中的资源
- **THEN** 系统返回 404，响应不含该资源内容

### Requirement: 资源清单与资源读取

系统 SHALL 在技能详情中返回资源清单，至少包含相对路径、类型（references / assets / scripts）与字节数。资源读取 MUST 只允许技能包内的相对路径；单次读取 MUST 有大小上限；对不存在或无权限的路径 MUST 返回 404。

#### Scenario: 资源清单可见

- **WHEN** 教师查看一个含 `references/formulas.md` 的技能包详情
- **THEN** 资源清单中出现 `references/formulas.md` 及其字节数

#### Scenario: 非法路径被拒绝

- **WHEN** 读取资源时路径包含 `..` 或指向技能包目录之外
- **THEN** 系统返回 400 或 404，且不返回任何文件内容

### Requirement: 运行时按需加载

一轮提问的候选集 MUST 仍是「绑定到当前智能体且已启用」的技能，且 MUST 按既有「适用条件」关键词计分规则选出至多一条技能。命中后，系统 SHALL 读取该技能包 `SKILL.md` 的正文并拼入系统提示词；frontmatter `resources` 声明的资源 SHALL 在总量上限内作为数据块拼入本轮用户消息。未命中时 MUST NOT 加载任何技能包正文或资源。

#### Scenario: 未命中不加载技能包

- **WHEN** 学生提问与所有候选技能的适用条件均无字面命中
- **THEN** 本轮请求不包含任何技能包正文与资源，且不产生技能命中记录

#### Scenario: 命中后加载正文与声明资源

- **WHEN** 提问命中某技能包，且其 frontmatter 声明了 `resources: [references/formulas.md]`
- **THEN** 本轮系统提示词包含该 `SKILL.md` 正文，用户消息数据块包含 `references/formulas.md` 的内容

#### Scenario: 资源总大小超限

- **WHEN** 命中技能包声明的资源总量超过配置上限
- **THEN** 系统仅加载不超过上限的部分，并在响应状态中标记资源被截断，回答主链路不中断

### Requirement: 工具声明兼容与校验

工具引用 MUST 支持两种声明位置：frontmatter 的 `allowed-tools` 优先，正文的「工具：」行作为兼容写法；合并后按引用去重。工具引用的校验规则 MUST 与既有规则一致：服务器在本班存在、已连接、且工具在缓存清单中；内置引用 `knowledge_search` 无需校验。命中技能后，其声明的工具 MUST 在生成回答前由服务端一次性并发执行，模型 MUST NOT 自行选择工具或提供参数。

#### Scenario: frontmatter 声明工具被校验

- **WHEN** 技能包在 `allowed-tools` 中声明了未连接服务器上的工具
- **THEN** 系统拒绝创建该技能包，并指出具体引用与原因

#### Scenario: 命中后工具先执行

- **WHEN** 命中技能包的 `allowed-tools` 含 `formula_hint@campus-tools`
- **THEN** 服务端在生成回答前完成该工具调用，并把结果作为数据拼入用户消息

### Requirement: 存量技能迁移为技能包

系统 SHALL 在升级后的首次启动时，把尚无包目录的存量技能（含内置「解题引导」）转换为标准技能包：以现有名称、描述与正文生成含 `name`、`description`、`适用条件`、`allowed-tools` frontmatter 的 `SKILL.md`，写入按班级与技能隔离的目录，并回填包目录、标识与内容哈希。迁移 MUST 保留名称、正文、启停状态、内置标记与智能体绑定关系；MUST 幂等，重复执行不改变已转换结果；MUST 逐条容错，单条失败不阻断启动，该技能继续按纯文本正常工作。

#### Scenario: 存量技能被转换为包

- **WHEN** 升级后的后端首次启动，且本班存在尚无包目录的技能
- **THEN** 该技能生成包含 `SKILL.md` 的包目录，包目录、标识与内容哈希被回填，技能列表可看到描述

#### Scenario: 迁移幂等

- **WHEN** 后端在迁移完成后再次启动
- **THEN** 不再重复生成包目录，已回填字段与 `SKILL.md` 内容保持不变

#### Scenario: 单条迁移失败不阻断

- **WHEN** 某个技能目录写入失败
- **THEN** 启动继续完成，其余技能正常转换；失败技能保持无包目录并按纯文本工作，原因被记录

#### Scenario: 内置技能同样转换

- **WHEN** 内置「解题引导」技能尚无包目录
- **THEN** 它同样被转换为技能包，且内置标记与不可删除语义保持不变

#### Scenario: 迁移后行为不变

- **WHEN** 迁移前某技能已启用并绑定到智能体，且提问命中其适用条件
- **THEN** 迁移后同一提问仍命中该技能，工具调用与回答行为与迁移前一致

### Requirement: 完整示例技能包

仓库 SHALL 提供一份完整示例技能包「勾股定理解题引导」，其根目录包含 `SKILL.md`，并包含 `references/`、`assets/`、`scripts/` 各至少一个文件；`SKILL.md` 的 frontmatter MUST 覆盖 `name`、`description`、`适用条件`、`allowed-tools`、`resources` 五个键。示例技能包的主题 MUST 与既有的一元二次、单位换算用例不重叠。示例技能包源文件 MUST 与接口测试共用同一份 fixture，且 MUST 走通上传、列表与详情展示、路由命中、正文与资源加载、工具调用、脚本不执行的完整链路。

#### Scenario: 示例技能包上传成功

- **WHEN** 教师上传示例技能包压缩件
- **THEN** 系统返回创建成功，技能列表可见其名称与描述，详情资源清单包含 `references/`、`assets/`、`scripts/` 下的条目

#### Scenario: 示例技能包命中并加载

- **WHEN** 学生提问「勾股定理中已知两条直角边，斜边怎么求？」且示例技能包已绑定并启用
- **THEN** 本轮系统提示词包含示例 `SKILL.md` 正文，用户消息数据块包含其 `resources` 声明的文件内容，响应状态标记资源已加载

#### Scenario: 示例技能包工具调用

- **WHEN** 示例技能包声明 `formula_hint@campus-tools` 且该服务器已连接
- **THEN** 服务端在生成回答前完成工具调用，响应状态记录 `success`，审计表可见对应记录

#### Scenario: 示例脚本不被执行

- **WHEN** 示例技能包命中且包含 `scripts/check_triangle.py`
- **THEN** 系统不执行该脚本，仅在资源清单中展示，回答链路正常完成

### Requirement: 脚本不执行

`scripts/` 下的文件 MUST 仅作为资源展示与下载，系统 MUST NOT 提供本地执行入口，也 MUST NOT 在回答链路中执行其中的代码。需要脚本能力时 MUST 通过 MCP 工具提供。

#### Scenario: 脚本文件不被执行

- **WHEN** 技能包包含 `scripts/convert.py` 且该技能被命中
- **THEN** 系统不执行该文件，仅在资源清单中展示；回答链路不因此中断
