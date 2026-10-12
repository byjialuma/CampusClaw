## Context

当前 HomePage 以 `view` 状态机在 `preview/results/qa/tutor` 四个视图间切换，左侧面板在普通模式（检索+问答+上传+材料列表）与助手模式（会话列表）间互斥切换。教师与学生看到完全相同的左侧面板。TutorView 刚加了「对话/设置」内嵌标签页，但整体布局仍显拥挤。

本次变更重构 HomePage 为角色化布局，删除检索页与问答页，拆分教师助手设置与技能管理为独立标签页。后端仅新增一个学生名单接口，其余 API 全部保留。

## Goals / Non-Goals

**Goals:**
- 教师端：四标签页（学生/资料/助手设置/技能管理）清晰切换，每页独占全高
- 学生端：左侧导航（解题助手/资料库）+ 下方历史会话，右侧默认对话页
- 删除 SearchPanel/QAPanel/SearchResults/QAResults 四个组件及对应状态
- TutorView 精简为纯对话视图，标签页由外层 HomePage 驱动

**Non-Goals:**
- 不改后端 RAG/SSE/技能编排/材料管理逻辑
- 不改数据库表结构
- 不改 Nginx/Docker 配置
- 不新增认证或权限模型（沿用现有 JWT + requireTeacher 中间件）

## Decisions

### 1. HomePage 角色化布局

教师端：`view` 状态机改为 `'students' | 'materials' | 'settings' | 'skills'`，默认 `students`。标题栏放四个标签按钮 + 退出登录。左侧面板不再切换——教师不需要会话列表（会话是学生功能）。右侧按 `view` 渲染对应组件。

学生端：`view` 状态机改为 `'tutor' | 'materials'`，默认 `tutor`。左侧固定显示导航栏（解题助手/资料库两个按钮）+ 下方历史会话列表（复用 AssistantPanel）。右侧按 `view` 渲染 TutorView 或 MaterialList+Preview。

### 2. 组件拆分

| 新组件 | 职责 | 来源 |
|---|---|---|
| `StudentList` | 教师学生名单表（fetch /api/class/students） | 新建 |
| `MaterialView` | 资料列表+预览合并页（复用 MaterialList + Preview） | 从 HomePage 拆出 |
| `AssistantSettings` | 精简为提示词+技能开关区（去掉技能 CRUD） | 修改现有 |
| `SkillManagement` | 技能 CRUD 页（列表+新建+上传+删除+开关） | 从 AssistantSettings 拆出 |

TutorView 去掉 `tab` 状态和标签按钮，仅保留对话消息区 + 输入框 + 引用卡片。欢迎语「解题助手，我帮你」在无活跃会话时显示。

### 3. 新增后端接口

`GET /api/class/students` → `requireAuth + requireTeacher` → 从 JWT claims 取 `class_id` → `SELECT id, username, display_name, created_at FROM users WHERE class_id = ? AND role = 'student' ORDER BY created_at` → JSON 数组返回。

### 4. 删除的组件与状态

- 删除 `SearchPanel`、`QAPanel`、`SearchResults`、`QAResults` 四个组件
- 删除 HomePage 中 `searching/searchQuery/searchResults/searchError/qaLoading/qaQuestion/qaAnswer/qaCitations/qaError` 等状态
- 保留 `handleSearch`/`handleAsk` 函数引用（如 tutor 引用跳转仍需 `handleOpenSource`）
- `/api/search`、`/api/qa` 路由保留不删

### 5. CSS 调整

- 新增 `.nav-tabs`（教师标签页条）与 `.nav-sidebar`（学生侧栏导航）样式
- `.tutor-view` 去掉 `max-height` 限制（不再内嵌设置区）
- 欢迎语区域 `.tutor-welcome` 居中大字
- 保持北大红主色调

## Risks / Trade-offs

- **检索/问答 API 保留但无 UI 入口** → 用户可能困惑"为什么 API 还在但页面没了"。接受这个 trade-off：解题助手内部 RAG 复用 Embedding 与向量检索，未来可能恢复独立检索页。
- **教师无法看到学生历史会话** → 本次不引入教师查看学生会话的功能（隐私考虑）。如果需要，后续单独开 change。
- **MaterialView 合并列表与预览** → 小屏幕上可能拥挤。用 flex 布局，列表宽 300px + 预览自适应。
