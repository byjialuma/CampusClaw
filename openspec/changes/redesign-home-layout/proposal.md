## Why

当前首页左侧把知识库检索、知识问答、解题助手入口、上传、材料列表堆叠在一起，教师与学生看到的界面完全相同，缺少角色化导航。用户明确要求：去掉独立的检索页和问答页，按角色重构布局——教师看到学生名单、本班资料、助手设置、技能管理四个页面；学生看到解题助手为主页、资料库为辅、历史会话在侧栏下方。现有后端逻辑（RAG 检索、SSE 流式、技能编排、材料管理）全部保留，仅重构前端布局并新增一个学生列表接口。

## What Changes

- **BREAKING** 移除前端独立的知识库检索页（`SearchPanel`/`SearchResults`）与知识问答页（`QAPanel`/`QAResults`）；后端 `/api/search`、`/api/qa` 路由保留（解题助手内部 RAG 仍复用 Embedding 与向量检索），但不再暴露独立 UI。
- 新增后端接口 `GET /api/class/students`（仅教师），返回本班学生列表（id、用户名、显示名、创建时间）。
- 教师端首页改为四标签页布局：(1) 学生名单、(2) 本班资料（列表 + 上传 + 预览）、(3) 助手设置（提示词 + 已有技能开关 + 已有材料参与开关）、(4) 技能管理（新建 / 上传 / 删除技能）。
- 学生端首页改为：左侧导航栏（解题助手、资料库入口）+ 下方历史会话列表；右侧默认为解题助手对话页（欢迎语"解题助手，我帮你"，聊天框待输入），点资料库切换到本班资料列表。
- 既有要求全部不变：JWT 鉴权、班级隔离、角色 403/404、上传双校验、下载票据、SSE 流式、技能编排、builtin 删除保护、跨班 404 等。

## Capabilities

### New Capabilities

- `home-navigation`: 角色化首页导航——教师四标签页（学生/资料/助手设置/技能管理）、学生侧栏导航（解题助手/资料库）+ 历史会话列表，定义页面切换契约与角色可见性边界。

### Modified Capabilities

- `tutor-agent`: TutorView 布局变更——教师端"对话/设置"标签页拆为三页（对话、助手设置、技能管理），由首页标签页驱动；学生端右侧默认为对话页，欢迎语与空态调整。
- `classroom-knowledge-base`: 移除独立检索页与问答页的前端入口；新增 `GET /api/class/students` 教师接口。

## Impact

- **前端**（React）：`HomePage` 重写为角色化布局；`SearchPanel`/`QAPanel`/`SearchResults`/`QAResults` 组件删除；新增 `StudentList` 组件与 `MaterialView`（资料列表+预览合并页）；`AssistantSettings` 拆为 `AssistantSettings`（提示词 + 开关区）与 `SkillManagement`（技能 CRUD）；`TutorView` 精简为纯对话视图（去掉内嵌标签页，标签页由外层 HomePage 驱动）；`AssistantPanel` 保留为学生侧栏的历史会话区。
- **后端**（Go）：新增 `GET /api/class/students` 路由与 handler（教师角色校验，按 `class_id` 查 `users` 表 `role='student'`）；`/api/search`、`/api/qa` 路由保留不删。
- **数据库**：无变更（`users` 表已有 `class_id` + `role` 字段）。
- **部署**：无新容器；Nginx 配置不变。
