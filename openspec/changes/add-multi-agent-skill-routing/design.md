## Context

现状（详见 proposal.md - Why）：一个班级只有一条 `assistants` 记录，提示词存于 `assistants.system_prompt`；技能经 `skills.assistant_id` 归属该记录；提问时 `tutor.AssistantConfig` 读取本班助手与全部技能，`EnabledSkills()` 返回全部 `enabled=1` 技能并逐条并入 system 提示；检索仅按 `Score <= 0` 过滤（无阈值），`buildSystem` 在无命中时注入固定文案 `noHitNote`；引用由 `mapCitations` 映射为 `Citation`，无 `[n]` 编号；工具阶段 `collectToolRefs` 收集全部已启用技能声明的引用。

约束：Go 标准库 `net/http`（`mux.HandleFunc("METHOD /path")` 路由 + `requireAuth`/`requireTeacher` 中间件）；MySQL 8.0 幂等建表（`CREATE TABLE IF NOT EXISTS`）与幂等迁移；向量检索走 Qdrant，`class_id` 强制过滤；前端 React 18 + TS + Vite。既有能力路径 `tutor-agent`、`home-navigation` 定义于未归档的 in-flight 变更，`openspec/specs/` 尚无归档后的主 spec，故本次 delta 以 ADDED 承载新契约并在正文标注取代关系。

## Goals / Non-Goals

**Goals:**

- 班级内多智能体：每个智能体独立持有提示词，并绑定自己的技能集与材料集。
- 一次提问至多命中一条技能（候选集内词法计分），命中技能决定其内容并入与 MCP 工具调用。
- 检索优先且限定绑定材料；以 `[n]` 标出处；检索落空或无绑定材料时仍按提示词作答。
- 会话固定绑定智能体，会话内不切换；学生默认选中教师最近更新的智能体。
- 存量数据无损迁移，迁移后行为对存量班级不倒退。

**Non-Goals:**

- 不改动独立知识问答（`internal/qa`、`/api/qa`）与其固定文案行为；`/api/search`、`/api/qa` 路由保留。
- 不引入模型侧「可选技能」tool-calling 主动选择（路由在服务端确定性完成）。
- 不做跨会话技能/材料共享语义的变更（技能库仍属班级）。
- 不改变 MCP 服务端与已有 MCP 管理接口的协议与鉴权。

## Decisions

### D1. 引入 `agents` 表承载提示词，技能与材料以绑定表挂到智能体

新增三张表；`assistants` 与 `skills.assistant_id` 保持不变，继续作为「班级技能库」的容器（技能上传/新建/启停路径零改动）。

```sql
CREATE TABLE IF NOT EXISTS agents (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  class_id      BIGINT       NOT NULL,
  name          VARCHAR(120) NOT NULL,
  system_prompt TEXT         NULL,
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_agents_class_name (class_id, name),
  KEY idx_agents_class_updated (class_id, updated_at, id),
  CONSTRAINT fk_agents_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS agent_skills (
  agent_id BIGINT NOT NULL,
  skill_id BIGINT NOT NULL,
  PRIMARY KEY (agent_id, skill_id),
  CONSTRAINT fk_agent_skills_agent FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE CASCADE,
  CONSTRAINT fk_agent_skills_skill FOREIGN KEY (skill_id) REFERENCES skills (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS agent_materials (
  agent_id   BIGINT NOT NULL,
  handout_id BIGINT NOT NULL,
  PRIMARY KEY (agent_id, handout_id),
  CONSTRAINT fk_agent_materials_agent   FOREIGN KEY (agent_id)   REFERENCES agents (id)   ON DELETE CASCADE,
  CONSTRAINT fk_agent_materials_handout FOREIGN KEY (handout_id) REFERENCES handouts (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
```

`conversations` 增加 `agent_id BIGINT NOT NULL` 与 `KEY idx_conversations_agent (agent_id)`，FK `ON DELETE RESTRICT`（智能体被 Session 引用时不允许直接级联删除消息）。

**理由**：绑定是多对多（同一条技能/材料可被多个智能体使用），单列外键无法表达；绑定表 PK 天然去重。`assistants` 保留可让技能库与既有 `skills.assistant_id`、`idx_skills_assistant`、`fk_skills_assistant` 全部零改动，迁移面最小。

**替代方案**：把 `skills.assistant_id` 改为 `agent_id`（需重建技能表与全部外键、改动技能 CRUD 路径，迁移风险高）——否。把提示词同时留在 `assistants` 与 `agents`（双写、双读）——否，会引入两个真值来源。

**权衡**：`assistants.system_prompt` 迁移后不再被读取路径使用（值已复制到默认智能体）。保留该列以免破坏性迁移，后续清理单独立项。

### D2. 词法计分为「适用条件关键词在问题文本中的命中个数」，同分取技能编号较小者

路由在 `internal/tutor` 内以纯函数实现（输入：候选技能集 + 问题文本；输出：命中技能或 nil）。

- 候选集 = `agent_skills` 绑定且 `skills.enabled = 1` 的技能，按 `skills.id` 升序。
- 计分：复用 `ParseSkillTools` 解析出的 `适用条件` 关键词列表；命中数 = 关键词中在问题文本内以子串出现的个数（`strings.Contains`；对 ASCII 关键词做大小写折叠，中文直接子串）。
- 判定：取分数最大者；同分取 `skill.id` 较小者；最大分为 0 → 不命中。
- 未声明 `适用条件` 行的技能得分为 0（不构成命中）——取代既有 `conditionMatched` 的「未声明视为命中」语义。

**理由**：确定性、可解释、可离线单测，无需额外依赖；同分规则消除候选顺序依赖。

**替代方案**：TF-IDF/BM25 加权（对中文短问题收益有限且引入分词器依赖）、向量相似度路由（多一次 LLM 调用且不透明）——否。

### D3. 检索：新增 `SEARCH_MIN_SCORE` 阈值；候选材料限定为该智能体绑定的讲义

`searchClass(ctx, classID, agentID, topK)` 先取该智能体的绑定 `handout_id` 集合：

- 绑定集合为空 → 直接返回空命中（不调用向量检索，或调用后丢弃），等价「该智能体没有绑定材料」。
- 非空 → 以 `class_id` 强制过滤检索，再按 `handout_id ∈ 绑定集合` 过滤，最后丢弃 `Score <= SEARCH_MIN_SCORE` 的分片（保留 `Score > 阈值`）。

阈值经 `internal/config` 注入：`SEARCH_MIN_SCORE`，默认 `0.0`，取值区间 `[0,1]`；默认值与现状「仅丢弃 Score <= 0」一致，便于灰度调优。

**理由**：绑定过滤在服务端 SQL/内存侧完成，杜绝跨班与跨智能体召回；阈值可配置使「相似度下限」可运维。

**权衡**：默认 0 意味着未显式配置时阈值不产生额外过滤；这是刻意的向后兼容选择，e2e 用例须显式配置一个正阈值来验证过滤生效。

### D4. system 提示组装：只并入命中技能；材料片段与出处编号的指令分离

`buildSystem(prompt, hitSkill, citations)`：

- 始终以智能体提示词开头（为空时用内置兜底）。
- 仅当 `hitSkill != nil` 时并入该技能内容（一次提问至多一条）。
- `citations` 非空 → 追加材料片段块，并指令「回答优先依据这些材料，并在引用处用 `[n]` 标出（`n` 为引用列表序号，自 1 起）」，并明确 `[n]` 只可指向列表内条目。
- `citations` 为空 → 追加**反编造护栏**（非固定文案）：说明本轮没有可用材料片段，要求仅依据角色设定作答、不得编造任何文件名或位置、不得使用 `[n]` 编号。**取代** 既有 `noHitNote` 的固定文案注入；回答不再被要求输出「根据已有材料无法回答该问题」。

`[n]` 与引用列表的对齐：`mapCitations` 先按 `(Score desc, DocumentID asc, 首个命中序号 asc)` 稳定排序并按 `DocumentID` 去重，再补齐 `index = 1..n` 写入 `Citation`（新增 `index` JSON 字段，前端可选用）。回答文本中的 `[n]` 由该指令驱动模型产生，与列表序号一致。

**理由**：把「有什么材料」与「如何标注」拆成两条独立指令，落空时既不放行幻觉、也不强制固定文案；稳定排序保证 `[n]` 可复现。

**替代方案**：后端后处理正则替换回答中的 `[n]`（脆弱、易破坏文本）——否。

### D5. 工具阶段只处理命中技能；未命中即不解析、不调用、不审计

`AskStream` 顺序为：解析会话 → 取 `agent_id` → 载入该智能体（提示词 + 绑定技能 + 绑定材料）→ 取历史消息 → 词法路由 → `searchClass`（含绑定过滤与阈值）→ 写 user 消息 → `runToolPhase(hitSkill)` → `buildSystem` → 组 messages → `emit(meta)` → LLM 流式 → 落库 → done。

`runToolPhase` 入参由「技能列表」改为「至多一条命中技能」：命中技能声明的引用被收集与调用；`hitSkill == nil` 时立即返回空 `toolCalls` 且不写审计。内置 `knowledge_search` 语义不变（本班检索始终执行）。其余失败态（未连接 `denied`、工具不在清单 `denied`、服务器已删 `denied` 且无审计、远端失败 `error`）与超时控制（`MCP_TIMEOUT_SECONDS`）完全沿用。

`meta` 事件的 `skills` 字段改为本轮命中技能的单元素列表或空列表；`citations` 携带带 `index` 的稳定序列；`toolCalls` 语义不变。

**理由**：把路由结果作为工具阶段的唯一输入，天然满足「未命中技能不调用其工具、不产生审计」。

### D6. 会话绑定智能体；学生默认选中最近更新的智能体

- `POST /api/tutor/conversations` 请求体新增必填 `agentId`；服务端校验该智能体属于 `JWT` 班级，否则 404；写入 `conversations.agent_id`。
- `AskStream` 与历史读取一律按 `conversation.agent_id` 取智能体，会话内不再支持切换。
- 学生默认选中规则 = `ORDER BY updated_at DESC, id DESC LIMIT 1`（同刻取编号较大者，与 spec 一致）。

### D7. 接口面（新增与调整）

新增（全部 `requireAuth`，班级来自 JWT）：

- `GET /api/tutor/agents`：任意角色可读，班级限定。教师响应含每个智能体的 `name`、`systemPrompt`、绑定的 `skillIds`/`handoutIds`；学生响应仅含 `id`、`name`、`updatedAt`（不回提示词）。
- `POST /api/tutor/agents`、`PUT /api/tutor/agents/{id}`、`DELETE /api/tutor/agents/{id}`：教师专属（`requireTeacher`）。
- `PUT /api/tutor/agents/{id}/skills`（体：`{"skillIds":[...]}`）、`PUT /api/tutor/agents/{id}/materials`（体：`{"handoutIds":[...]}`）：教师专属，全量替换绑定集，幂等；逐项校验属于本班，否则 404。

调整：

- **BREAKING** `PUT /api/tutor/assistant/prompt` 退役，提示词改为按 `PUT /api/tutor/agents/{id}` 维护。
- `GET /api/tutor/assistant` 保留为班级技能库视图，响应不再含提示词字段。
- 技能 CRUD 路径与语义（`/api/tutor/assistant/skills*`）不变。
- `POST /api/tutor/conversations` 请求体新增 `agentId`（**BREAKING**：缺省或非法返回 400/404）。

角色边界：未登录 401；学生调用智能体写接口与绑定接口 403；跨班或不存在智能体 404。

### D8. 迁移方案（幂等）

按顺序执行、每步可重复：

1. 建表：`agents`、`agent_skills`、`agent_materials`（`CREATE TABLE IF NOT EXISTS`）。
2. `conversations` 加列 `agent_id BIGINT NULL`（存在性检查后 `ALTER`）。
3. 回填默认智能体：`INSERT INTO agents (class_id, name, system_prompt) SELECT a.class_id, a.name, a.system_prompt FROM assistants a WHERE NOT EXISTS (SELECT 1 FROM agents g WHERE g.class_id = a.class_id);`
4. 绑定存量技能：`INSERT IGNORE INTO agent_skills (agent_id, skill_id) SELECT g.id, s.id FROM agents g JOIN skills s ON s.assistant_id = a.id ...`（按 `class_id` 对齐）。
5. 绑定班级全部讲义：`INSERT IGNORE INTO agent_materials (agent_id, handout_id) SELECT g.id, h.id FROM agents g JOIN handouts h ON h.class_id = g.class_id;`
6. 回填会话：`UPDATE conversations c JOIN agents g ON g.class_id = c.class_id SET c.agent_id = g.id WHERE c.agent_id IS NULL;`
7. 收紧约束：将 `conversations.agent_id` 置 `NOT NULL` 并加 FK/索引。

**回滚**：迁移不删列、不改 `skills`/`assistants` 结构，回滚只需停止读取 `agents`（代码回退到读取 `assistants.system_prompt` 与全部已启用技能）并删除新增三表与 `conversations.agent_id` 列。

## Risks / Trade-offs

- [词法计分对中文措辞敏感] → 计分口径固定为子串包含并写入 spec；同分取小 id 保证确定性；教师可通过补全 `适用条件` 关键词调优。
- [`SEARCH_MIN_SCORE` 默认 0 等于不生效] → 文档标注默认值；e2e 用例显式配置正阈值验证「低于下限的分片不作为依据」。
- [delta 以 ADDED 呈现替代语义] → 需求正文显式标注取代关系（如「取代此前『全部已启用技能逐条并入』」）；归档时按前置变更先归档以保持主 spec 一致。
- [`assistants.system_prompt` 成为孤儿列] → 迁移一次性回填到默认智能体；任务中加入断言校验读取路径只依赖 `agents`。
- [`conversations.agent_id` 置 `NOT NULL` 需回填后执行] → 拆为「加可空列 → 回填 → 收紧」三步；`ON DELETE RESTRICT` 避免误删消息。
- [绑定材料越权召回] → 绑定写接口逐项校验本班归属（否则 404）；检索 SQL 同时强制 `class_id` 与绑定 `handout_id` 双过滤。
- [学生默认智能体随教师更新而变] → 默认规则 `updated_at DESC, id DESC` 已写入 spec；既有会话绑定不变，避免历史回答语境漂移。

## Open Questions

- `SEARCH_MIN_SCORE` 的生产调优取值（默认 0 保持现状），不影响本设计的接口与实现路径。
- 学生是否需要在界面上看到所绑定智能体的提示词（当前设计不回传），不影响本次实现。
