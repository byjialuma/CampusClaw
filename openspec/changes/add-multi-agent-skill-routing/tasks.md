## 1. 数据模型与迁移

- [x] 1.1 在 `backend/internal/db/schema.sql` 新增 `agents`、`agent_skills`、`agent_materials` 建表，并给 `conversations` 增加 `agent_id` 列（验证：容器内重建库后 `SHOW TABLES` 出现三张新表，`DESC conversations` 含 `agent_id`）
- [x] 1.2 在 `backend/internal/db` 实现幂等迁移：`agents`/`agent_skills`/`agent_materials` 建表、`conversations.agent_id` 加可空列、回填、再收紧为 `NOT NULL` 与 FK/索引（验证：连续执行迁移两次均无报错，第二次不改变任何行数）
- [x] 1.3 回填存量数据：每个 `assistants` 行生成同班默认智能体（沿用 `name` 与 `system_prompt`），把该班级全部技能写入 `agent_skills`、全部讲义写入 `agent_materials`，并按班级回填 `conversations.agent_id`（验证：抽查一个存量班级，`agents` 恰 1 行、`agent_skills` 数等于该班技能数、`agent_materials` 数等于该班讲义数、该班会话 `agent_id` 均非空）

## 2. 仓储层（backend/internal/tutor）

- [x] 2.1 新增智能体仓储：`CreateAgent`/`UpdateAgent`/`DeleteAgent`/`ListAgents`/`GetAgent`，班级一律取参数（来自 JWT）；同班重名返回可映射为 400 的哨兵错误（验证：仓储单测覆盖创建、改名、重名冲突、跨班读取返回不存在）
- [x] 2.2 新增绑定仓储：`ReplaceAgentSkills`、`ReplaceAgentMaterials`（全量替换、幂等）与读取当前绑定（验证：单测断言替换后集合精确等于入参，重复调用不产生重复行）
- [x] 2.3 新增 `AgentConfig` 读取：按 `agent_id` 返回提示词、绑定且 `enabled=1` 的技能（按 `skills.id` 升序）与绑定的 `handout_id` 集合（验证：单测覆盖有绑定/无绑定/仅停用技能三类返回）
- [x] 2.4 `Conversation` 增加 `AgentID` 字段；创建会话写入 `agent_id`，读取会话解析其智能体（验证：单测断言新建会话落库 `agent_id` 与传入一致、读取回显一致）
- [x] 2.5 `searchClass` 改造为接受绑定 `handout_id` 白名单与相似度下限：绑定集合为空时直接返回空命中；非空时按 `class_id` 检索后过滤 `handout_id ∈ 白名单` 并丢弃 `Score <= SEARCH_MIN_SCORE` 的分片（验证：单测断言未绑定材料的分片不出现、低于阈值分片被丢弃）

## 3. 路由、提示组装与工具阶段（backend/internal/tutor）

- [x] 3.1 `backend/internal/config` 新增 `SEARCH_MIN_SCORE` 环境变量（默认 `0.0`，取值区间 `[0,1]`，越界回落到默认值）（验证：config 单测覆盖默认值、合法值、越界回落）
- [x] 3.2 实现词法路由纯函数 `RouteSkill(skills, question)`：按「适用条件关键词在问题文本中的命中个数」计分，取最高分；同分取 `skill.id` 较小者；最高分为 0 时不命中；未声明适用条件的技能得分为 0（验证：table-driven 单测覆盖最高分、同分、零命中、未声明条件、未绑定技能不入选）
- [x] 3.3 改造 `buildSystem(prompt, hitSkill, citations)`：只并入本条命中技能内容；`citations` 非空时追加材料片段并要求以 `[n]` 标出处；`citations` 为空或智能体未绑定材料时追加反编造护栏（明示不得编造出处、不得使用 `[n]`），移除既有 `noHitNote` 固定文案注入（验证：单测断言命中/落空两个分支的 system 内容差异，且落空分支不含固定文案）
- [x] 3.4 `mapCitations` 增加稳定排序（`Score desc, DocumentID asc, 首个命中序号 asc`）与按 `DocumentID` 去重，并写入 `index`（自 1 递增）字段（验证：单测断言同一输入多次调用得到同一顺序与 `index` 序列）
- [x] 3.5 改造 `AskStream` 编排：会话 → 智能体（提示词/绑定技能/绑定材料）→ 词法路由 → 绑定材料内检索 → 工具阶段（仅命中技能）→ meta（`skills` 为单元素或空列表、`citations` 带 `index`）→ LLM 流式 → 落库（验证：`service_test.go` 覆盖命中一条技能、零命中、检索落空仍作答、未绑定材料仍作答四类用例）
- [x] 3.6 `runToolPhase` 入参改为「至多一条命中技能」：零命中时立即返回空 `toolCalls` 且不解析引用、不调用远端、不写审计；命中时沿用既有工具调用与三态审计（`success`/`denied`/`error`）与超时控制（验证：单测断言零命中不产生审计行、命中产生审计行）

## 4. 服务端接口与路由注册（backend/internal/server）

- [x] 4.1 新增并注册智能体接口：`GET /api/tutor/agents`（`requireAuth`，班级限定）、`POST /api/tutor/agents`、`PUT /api/tutor/agents/{id}`、`DELETE /api/tutor/agents/{id}`（`requireTeacher`）（验证：server 单测覆盖 200/201、重名 400、未登录 401、学生 403、跨班 404）
- [x] 4.2 新增并注册绑定接口：`PUT /api/tutor/agents/{id}/skills`（`{"skillIds":[...]}`）与 `PUT /api/tutor/agents/{id}/materials`（`{"handoutIds":[...]}`），全量替换并逐项校验本班归属（验证：server 单测覆盖替换成功、含跨班 id 时 404 且绑定不变、学生 403）
- [x] 4.3 `GET /api/tutor/agents` 按角色裁剪响应：教师含 `systemPrompt` 与绑定清单，学生仅含 `id`/`name`/`updatedAt`（验证：单测断言学生响应不含 `systemPrompt` 字段）
- [x] 4.4 调整既有助手接口：退役 `PUT /api/tutor/assistant/prompt`，`GET /api/tutor/assistant` 保留为班级技能库视图且响应移除提示词字段（验证：单测与路由表断言该 PUT 路径不再注册、GET 响应无提示词字段）
- [x] 4.5 `POST /api/tutor/conversations` 接受必填 `agentId` 并校验属于 JWT 班级（验证：单测覆盖 201 成功、缺失/非法 400、跨班或不存在 404 且未创建会话）

## 5. 前端（frontend/src）

- [x] 5.1 `api.ts`/`types.ts` 增补智能体与绑定类型与接口封装（含学生/教师视图差异字段）（验证：`npm run build` 通过，类型无报错）
- [x] 5.2 教师端把「助手设置」标签升级为「智能体管理」，新增组件承载智能体增删改、提示词编辑与技能/材料绑定（验证：浏览器走查完成新建智能体 → 填提示词 → 绑定两条技能与一份材料 → 列表回显绑定）
- [x] 5.3 学生端左侧导航新增本班智能体列表并默认选中教师最近更新的智能体；新建会话携带所选 `agentId`；无智能体时禁用输入并提示（验证：浏览器走查默认选中为最近更新者、切换后新建会话绑定切换后智能体、既有会话绑定不变）
- [x] 5.4 `TutorView.tsx` 渲染回答文本中的 `[n]` 并与引用列表序号对齐，引用卡片仍仅显示文件名（验证：浏览器走查命中时回答出现 `[n]` 且与卡片序号一致；落空时回答无编号、引用区为空）

## 6. 测试与验收

- [x] 6.1 在 `apitest/` 为每个新增/调整接口编写独立脚本并核对数据库落库数据（验证：`python apitest/run_all.py` 全部用例通过）
- [x] 6.2 更新 `deploy/tests/e2e-mcp-matrix.sh`（或新增同规格脚本）覆盖：单技能最高分命中、同分取小 id、零命中不选技能、未绑定技能不参与、未命中技能不调用工具且无审计、阈值过滤、「检索落空仍按提示词作答且引用为空」（验证：容器内经 Nginx 执行得到 `FAIL=0`）
- [x] 6.3 两模块 `go vet` 与 `go test`（`backend`、`mcp-server`）在 `golang:1.23-alpine` 容器内执行（验证：退出码 0）
- [x] 6.4 前端 `npm run build`（验证：退出码 0）
- [x] 6.5 `npx --yes openspec validate "add-multi-agent-skill-routing" --strict`（验证：输出 `is valid` 且退出码 0）
