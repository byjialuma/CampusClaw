## 1. 后端：学生名单接口

- [x] 1.1 新增 `GET /api/class/students` 路由与 handler（`requireAuth + requireTeacher`，从 JWT claims 取 `class_id`，查 `users` 表 `role='student'`），返回 `[{id, username, displayName, createdAt}]`；验证：`go vet ./... && go test -count=1 ./internal/server/...` 通过
- [x] 1.2 补充 server 单测：教师调用 200 返回本班学生、学生调用 403、未登录 401、跨班不暴露；验证：`go test -count=1 ./internal/server/...` 全绿

## 2. 前端：删除检索页与问答页

- [x] 2.1 删除 `SearchPanel`、`QAPanel`、`SearchResults`、`QAResults` 四个组件文件，移除 HomePage 中对应 import、状态（`searching/searchQuery/searchResults/searchError/qaLoading/qaQuestion/qaAnswer/qaCitations/qaError`）与 `handleSearch`/`handleAsk`；验证：`npm run build`（tsc -b && vite）通过无报错
- [x] 2.2 清理 styles.css 中检索/问答专属样式（`.search-panel`、`.qa-panel` 等，保留 `.search-input` 公共样式）；验证：构建通过且页面无样式回归

## 3. 前端：组件拆分与新建

- [x] 3.1 新建 `StudentList.tsx`：调用 `fetchStudents()`，展示表格（用户名、显示名、加入时间），空态提示；验证：组件渲染无报错
- [x] 3.2 新建 `MaterialView.tsx`：合并 MaterialList + UploadPanel（教师可见上传）+ Preview 为一个 flex 布局页（列表左 300px + 预览右自适应）；验证：构建通过
- [x] 3.3 从 `AssistantSettings.tsx` 拆出 `SkillManagement.tsx`：技能列表（来源徽标/开关/删除/内容展开）+ 上传按钮 + 在线新建表单；AssistantSettings 保留提示词编辑 + 已有技能开关区；验证：两个组件独立渲染无报错
- [x] 3.4 精简 `TutorView.tsx`：去掉 `tab` 状态与标签按钮，仅保留对话消息区 + 输入框 + 引用卡片；无活跃会话时显示欢迎语「解题助手，我帮你」与空态提示；验证：构建通过

## 4. 前端：api.ts 与 types.ts 扩展

- [x] 4.1 `api.ts` 新增 `fetchStudents(): Promise<StudentInfo[]>`（GET /api/class/students）；`types.ts` 新增 `StudentInfo` 类型（id/username/displayName/createdAt）；验证：tsc 通过
- [x] 4.2 `api.ts` 确认 `searchMaterials`/`askQuestion` 函数保留不删（后端路由仍用）；验证：import 无未使用告警

## 5. 前端：HomePage 角色化布局重写

- [x] 5.1 教师端：`view` 状态改为 `'students' | 'materials' | 'settings' | 'skills'`（默认 `students`），标题栏放四个标签按钮 + 退出；右侧按 view 渲染 StudentList / MaterialView / AssistantSettings / SkillManagement；验证：教师登录后看到四标签页切换
- [x] 5.2 学生端：`view` 状态改为 `'tutor' | 'materials'`（默认 `tutor`），左侧固定导航栏（解题助手/资料库两个按钮）+ 下方历史会话列表（复用 AssistantPanel）；右侧按 view 渲染 TutorView 或 MaterialView；验证：学生登录后默认看到解题助手对话页
- [x] 5.3 学生端 MaterialView 隐藏上传按钮（仅教师可上传）；验证：学生查看资料库时不出现上传入口

## 6. 前端：CSS 与美化

- [x] 6.1 新增 `.nav-tabs`（教师标签页条：北大红高亮 active、圆角边框）、`.nav-sidebar`（学生侧栏导航：纵向按钮列表）样式；验证：构建通过
- [x] 6.2 新增 `.tutor-welcome`（欢迎语居中大字）、调整 `.tutor-view` 布局（去掉 `max-height` 限制、`flex: 1 1 auto` 全高）；验证：对话页全高显示
- [x] 6.3 调整 `.right-pane` 在教师模式下不显示 `.preview-card` 外框（标签页直接填充），学生模式保持卡片风格；验证：两种角色布局和谐
- [x] 6.4 `npm run build` 全量通过（tsc -b && vite），无 type error 与无未使用 import

## 7. 测试与校验

- [x] 7.1 后端：`go vet ./... && go test -count=1 ./...` 全绿（含新增学生名单单测）
- [x] 7.2 前端：`npm run build` 通过，页面无 console error
- [x] 7.3 E2E 增补：`e2e-tutor-matrix.sh` 新增学生名单断言（教师 GET 200、学生 GET 403）、首页无检索/问答入口断言；直连 backend + 经 Nginx 双跑通过
- [x] 7.4 浏览器走查：教师四标签页切换全流程（学生名单→资料→助手设置→技能管理）、学生解题助手对话+资料库切换+历史会话、学生无教师入口
- [x] 7.5 `openspec validate redesign-home-layout --strict` 退出码 0
