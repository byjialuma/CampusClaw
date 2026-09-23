## Why

校园教学材料（知识库）目前没有任何身份与访问控制：材料需要区分教师/学生身份，并以班级作为数据边界，保证材料只对本班成员可见、仅教师可上传。同时本项目需要一个可一键启动、链路完整（浏览器 → Nginx → Go → MySQL）的运行环境，用于课程演示与验收。

## What Changes

- 新增账号密码登录与退出：教师、学生均使用预置账号密码登录；采用**服务端会话 + HttpOnly Cookie**，不使用 JWT；未登录访问受保护页面被引导到登录页，未登录调用受保护 API 返回 401。
- 新增基于角色的权限控制（teacher / student）：学生拥有与教师相同的上传入口与接口，但调用上传接口时服务端必须拒绝并返回 **403** 与「学生无权限上传文件」提示，而非仅在前端隐藏按钮。
- 新增**班级数据隔离**：班级是服务端强制的数据边界。材料列表、内容查看、下载均在服务端按会话用户所属班级过滤；A 班成员访问 B 班材料一律拒绝（跨班详情/下载返回 403，列表中不出现外班记录）。
- 新增教师材料上传入库：教师可上传 txt / md / pdf 文件；文件元数据写入 MySQL、文件内容写入受控存储；上传成功后本班材料列表可查到该记录，列表项名称即上传文件名；点击材料可在右侧查看文件内容；可经带鉴权的下载接口在**新页面**下载，下载页刷新时若无有效会话则跳转登录页。
- 新增预置种子数据：班级 A/B、教师 A、学生 A1/B1，以及两班可区分的材料标题；建立**班级、用户、讲义、作业、助手、技能**六类核心数据结构并写入种子数据；种子账号口令通过环境变量注入，不硬编码。
- 新增 Docker Compose 启动方式与 `GET /health` 健康检查；对外仅暴露 Nginx，Go 后端与 MySQL 不直接向用户暴露。
- 新增 React 18 + TypeScript + Vite 前端工程：以北大红为主色调；左上角显示班级、姓名与身份，右上角放置退出按钮；左侧为上传区与材料列表，右侧为文件内容预览。

## Capabilities

### New Capabilities

- `classroom-knowledge-base`: 账号密码登录与服务端会话、教师/学生角色权限、服务端班级数据隔离、教学材料上传入库/查看/下载、六类核心结构与预置种子数据、Docker Compose 启动与 GET /health 健康检查。

### Modified Capabilities

（无——本工程为全新仓库，尚无既有 spec。）

## Non-goals

- **检索问答 / RAG**：不对知识库材料做向量化、语义检索或基于材料的问答。
- **对话助手**：助手（assistant）仅作为六类种子数据结构之一存在，不实现聊天、Agent 调用等运行时功能。
- **作业提交与批改**：作业（assignment）仅作为种子数据结构存在，不实现学生提交、教师批阅流程。
- **SSO / 生产级高可用**：不接入单点登录、OAuth、LDAP；不做水平扩展、多副本、负载均衡与生产级 TLS/证书管理。
- **账号自助管理**：不提供自助注册、用户管理后台、密码找回与修改。
- **材料治理操作**：不提供材料的删除、编辑、跨班分享与公开链接。
- **技能执行**：技能（skill）仅作为种子数据结构，不提供执行引擎。

## Impact

- 全新代码结构：`frontend/`（React 18 + TypeScript + Vite 构建，经 Nginx 托管静态资源）、`backend/`（Go + 仅标准库 `net/http`）、`deploy/`（Nginx 配置、MySQL 初始化 SQL 与种子数据）、`docker-compose.yml` 与 `.env.example`。
- 新增 HTTP 接口（均经 Nginx `/api` 反代，Go/MySQL 端口不对外发布）：
  - `POST /api/sessions` 登录、`DELETE /api/sessions` 退出、`GET /api/me` 当前会话信息；
  - `GET /api/materials` 本班材料列表（服务端按班级过滤）；
  - `POST /api/materials` 教师上传（multipart，限 txt/md/pdf；学生 403）；
  - `GET /api/materials/{id}/content` 本班材料内容（跨班 403）；
  - `POST /api/materials/{id}/download-ticket` 换取一次性下载票据、`GET /api/download?ticket=...` 供新标签下载页凭票下载（票据一次性、刷新即失效并跳登录；跨班 403、未登录 401）；
  - `GET /health` 存活/依赖检查（不经鉴权）。
- 新增 MySQL 8.0 库表：classes、users、handouts、assignments、assistants、skills（六类核心结构），会话表 sessions、一次性下载票据表 download_tickets；文件实体落盘到后端受控存储目录（不通过 Web 静态目录直出）。
- 部署与运行：`docker compose up` 一键起栈；依赖环境变量注入数据库连接串与种子账号口令。
