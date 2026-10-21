## 1. 数据模型与迁移

- [x] 1.1 在 `backend/internal/db/schema.sql` 与 `deploy/mysql/01_schema.sql` 的 `skills` 表同步新增 `slug`、`frontmatter`、`dir_path`、`content_hash` 四列，验证 `go test ./internal/db -run TestSchemaSync` 通过（两文件逐字节一致）
- [x] 1.2 在 `backend/internal/db/db.go` 增加幂等迁移（列已存在时忽略），验证重复启动后端无报错且 `DESCRIBE skills` 包含新列
- [x] 1.3 实现启动回填器：对 `dir_path` 为空的存量技能合成 frontmatter 与 `SKILL.md`、按技能 id 落盘、回填 `slug`/`frontmatter`/`dir_path`/`content_hash`；验证测试库中每个存量技能（含内置「解题引导」）都生成包目录且四个字段非空
- [x] 1.4 回填幂等与容错：重复执行不产生变更；模拟某条目录写入失败时启动继续、其余技能成功、失败技能 `dir_path` 保持空并按纯文本工作；验证单测或集成测试通过
- [x] 1.5 回填细节规则：`description` 为空时取正文首个非空行截断到 120 字符，同班 `slug` 冲突追加 `-<id>`；验证单测覆盖空描述、超长首行、同名两条三类输入

## 2. 技能包解析与校验

- [x] 2.1 新增 `backend/internal/tutor/skillpack.go`：实现 `SKILL.md` 的 frontmatter（`key: value`、行内数组、块数组）与正文解析，遇到不支持的语法返回带行号的错误；验证单元测试覆盖完整包、仅正文、缺字段、非法数组四类输入
- [x] 2.2 实现 zip 展开校验：仅接受 zip，限制压缩包/解压总量/文件数/单文件体积，拒绝绝对路径、`..`、符号链接；验证单元测试对每类越界输入返回可读错误且不落盘
- [x] 2.3 实现 `slug` 生成与同班唯一性校验（`name` 归一化后哈希）；验证同班重名返回冲突、跨班同名可共存
- [x] 2.4 实现工具声明合并：`allowed-tools` 与正文「工具：」行合并去重、前者优先；验证单元测试覆盖两者同时存在、仅其一时行为正确

## 3. 存储与接口

- [x] 3.1 实现技能包落盘：目录 `<DATA_DIR>/skills/<class_id>/<skill_id>/`，写入失败回滚已写文件；验证单元测试断言路径包含关系与失败清理
- [x] 3.2 扩展 `Skill` 结构与仓储：`AgentConfig`、`AssistantConfig` 读取新列；新增按 id 读取包详情与资源清单的查询；验证 `go test ./internal/tutor` 通过
- [x] 3.3 新增路由与处理器：`POST /api/tutor/assistant/skills/package`、`GET /api/tutor/assistant/skills/{id}`、`GET /api/tutor/assistant/skills/{id}/resources`，全部仅教师；验证 `curl` 分别返回 201/200/200，学生调用返回 403
- [x] 3.4 资源读取做班级隔离与包内路径限制，跨班或越界返回 404；验证 apitest 跨班与 `../` 用例均失败为预期状态码
- [x] 3.5 上传时接入 `validateSkillToolRefs` 对合并后的引用做校验；验证引用未连接服务器时返回 400 且错误含具体引用

## 4. 运行时按需加载

- [x] 4.1 `RouteSkill` 与候选集构造保持不变，包技能的正文来源切换为 `SKILL.md` 正文（`dir_path` 为空时仍用 `content`）；验证既有路由单测不改断言仍通过
- [x] 4.2 命中后按 `resources` 白名单加载资源：单文件 ≤16KB、合计 ≤32KB、最多 5 个，拼入本轮用户消息数据块；验证单测覆盖正常、超限截断、读取失败不阻断三种情形
- [x] 4.3 未命中时不加载任何正文与资源；验证 SSE `meta` 中无技能命中记录，且请求体不含包内容
- [x] 4.4 SSE `meta` 增加资源加载状态（已加载路径、是否截断、失败原因），验证学生端提问后 meta 字段可见

## 5. 前端

- [x] 5.1 `frontend/src/types.ts` 与 `api.ts` 增加技能包字段、上传与资源读取调用；验证 `npm run build` 通过
- [x] 5.2 `SkillManagement` 增加 zip 上传入口、包描述与资源清单展示，并明确标注“脚本不会被执行”；验证浏览器可完成上传并看到资源条目

## 6. 文档、测试与验收

- [x] 6.1 `frontend/public/openapi.json` 补 3 个新接口与技能包字段说明；验证 JSON 可解析且 Swagger 页面可见新端点
- [x] 6.2 `apitest/` 增加用例：合法包上传、缺 `SKILL.md`、路径穿越、学生 403、跨班 404、命中后资源进入上下文；验证 `run_all.py` 全部通过
- [x] 6.3 落地完整示例技能包 `apitest/fixtures/skill-pythagorean-guide/`：`SKILL.md`（frontmatter 含 `name`、`description`、`适用条件`、`allowed-tools`、`resources`）、`references/pythagorean-theorem.md`、`references/common-mistakes.md`、`assets/answer-template.md`、`scripts/check_triangle.py`，内容与 design.md D10 一致；验证接口用例从该目录现场打包上传、用例间共用同一 fixture 且可重复执行
- [x] 6.4 回归：`go vet ./...`、`go test ./...`、`npm run build`、`docker compose up -d` 后 `/health` 返回 200；验证命令退出码均为 0
- [x] 6.5 `openspec validate add-skill-package-format --strict` 通过；验证 HTML 报告位于 `apitest/reports/` 且 `git check-ignore` 命中
- [x] 6.6 部署后核对存量迁移：`docker exec campusclaw-backend ls /app/data/skills/<class_id>/` 可见每个技能目录与 `SKILL.md`，`GET /api/tutor/assistant` 每个技能都有描述；沿用 e2e 用例验证原提问仍命中同一技能（按 id 对比迁移前）
- [x] 6.7 示例技能包端到端验收：连接预置 `campus-tools` → 上传 `apitest/fixtures/skill-pythagorean-guide/` → 列表与详情可见 3 条声明资源及 `scripts/check_triangle.py` 条目 → 绑定到智能体 → 学生提问「勾股定理中已知两条直角边，斜边怎么求？」；验证 SSE `meta` 记录命中该技能、资源已加载且无截断、`formula_hint@campus-tools` 状态为 success，审计表出现 ok 记录，回答未写出最终数值结果，`scripts/check_triangle.py` 未被读取进上下文；测后清理技能与绑定
- [x] 6.8 示例技能包界面验收：教师在技能管理中上传同一份示例技能包；验证列表显示名称与描述、资源清单分 references/assets/scripts 三类展示，且页面明确标注“脚本不会被执行”；学生端提问后材料卡片或状态与 6.7 一致（教师端界面验收已通过；学生端行为由 6.7 集成用例覆盖，浏览器子代理因会话超时未能完成学生端截图）
