-- CampusClaw 核心结构（由 MySQL 初始化目录执行；Go 后端启动时以同一份文件幂等兜底）
-- 共十六张表：班级、用户、讲义、作业、助手、技能、智能体、智能体技能绑定、智能体材料绑定、
--             服务端会话、一次性下载票据、材料向量索引状态、助手会话、助手消息、MCP 服务器、MCP 调用审计

CREATE TABLE IF NOT EXISTS classes (
  id         BIGINT       NOT NULL AUTO_INCREMENT,
  name       VARCHAR(64)  NOT NULL,
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_classes_name (name)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS users (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  username      VARCHAR(64)  NOT NULL,
  password_hash VARCHAR(100) NOT NULL,
  display_name  VARCHAR(64)  NOT NULL,
  role          ENUM('teacher', 'student') NOT NULL,
  class_id      BIGINT       NULL,
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_users_username (username),
  KEY idx_users_class (class_id),
  CONSTRAINT fk_users_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS handouts (
  id                BIGINT        NOT NULL AUTO_INCREMENT,
  class_id          BIGINT        NOT NULL,
  uploader_user_id  BIGINT        NULL,
  original_name     VARCHAR(255)  NOT NULL,
  stored_path       VARCHAR(512)  NOT NULL,
  file_type         VARCHAR(16)   NOT NULL,
  size_bytes        BIGINT        NOT NULL,
  created_at        DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_handouts_class_created (class_id, created_at),
  CONSTRAINT fk_handouts_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE,
  CONSTRAINT fk_handouts_uploader FOREIGN KEY (uploader_user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS assignments (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  class_id    BIGINT       NOT NULL,
  title       VARCHAR(255) NOT NULL,
  description TEXT         NULL,
  due_at      DATETIME(3)  NULL,
  created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_assignments_class (class_id),
  CONSTRAINT fk_assignments_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS assistants (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  class_id      BIGINT       NOT NULL,
  name          VARCHAR(64)  NOT NULL,
  description   VARCHAR(255) NULL,
  system_prompt TEXT         NULL,
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_assistants_class (class_id),
  CONSTRAINT fk_assistants_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS skills (
  id           BIGINT       NOT NULL AUTO_INCREMENT,
  assistant_id BIGINT       NOT NULL,
  name         VARCHAR(120) NOT NULL,
  description  VARCHAR(255) NULL,
  enabled      TINYINT(1)   NOT NULL DEFAULT 0,
  content      MEDIUMTEXT   NULL,
  source       VARCHAR(16)  NOT NULL DEFAULT 'custom',
  builtin      TINYINT(1)   NOT NULL DEFAULT 0,
  created_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_skills_assistant (assistant_id),
  CONSTRAINT fk_skills_assistant FOREIGN KEY (assistant_id) REFERENCES assistants (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 智能体：班级内可维护多个，各自持有提示词；技能与材料经绑定表挂到智能体。
-- 存量迁移时每个 assistants 行生成一个同班默认智能体（沿用 name 与 system_prompt）。
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

-- 智能体技能绑定：多对多；仅绑定且已启用的技能参与该智能体的路由。
CREATE TABLE IF NOT EXISTS agent_skills (
  agent_id BIGINT NOT NULL,
  skill_id BIGINT NOT NULL,
  PRIMARY KEY (agent_id, skill_id),
  CONSTRAINT fk_agent_skills_agent FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE CASCADE,
  CONSTRAINT fk_agent_skills_skill FOREIGN KEY (skill_id) REFERENCES skills (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 智能体材料绑定：多对多；召回限定为绑定到该智能体的讲义。
CREATE TABLE IF NOT EXISTS agent_materials (
  agent_id   BIGINT NOT NULL,
  handout_id BIGINT NOT NULL,
  PRIMARY KEY (agent_id, handout_id),
  CONSTRAINT fk_agent_materials_agent   FOREIGN KEY (agent_id)   REFERENCES agents (id)   ON DELETE CASCADE,
  CONSTRAINT fk_agent_materials_handout FOREIGN KEY (handout_id) REFERENCES handouts (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS sessions (
  id         CHAR(64)     NOT NULL,
  user_id    BIGINT       NOT NULL,
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at DATETIME(3)  NOT NULL,
  PRIMARY KEY (id),
  KEY idx_sessions_user (user_id),
  KEY idx_sessions_expires (expires_at),
  CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS download_tickets (
  token      CHAR(64)    NOT NULL,
  handout_id BIGINT      NOT NULL,
  user_id    BIGINT      NOT NULL,
  class_id   BIGINT      NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at DATETIME(3) NOT NULL,
  used_at    DATETIME(3) NULL,
  PRIMARY KEY (token),
  KEY idx_tickets_handout (handout_id),
  KEY idx_tickets_expires (expires_at),
  CONSTRAINT fk_tickets_handout FOREIGN KEY (handout_id) REFERENCES handouts (id) ON DELETE CASCADE,
  CONSTRAINT fk_tickets_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_tickets_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 材料向量索引状态：分片正文与向量存 Qdrant，MySQL 仅记录每份讲义的索引生命周期。
CREATE TABLE IF NOT EXISTS material_index (
  handout_id  BIGINT       NOT NULL,
  status      ENUM('pending', 'ready', 'failed') NOT NULL DEFAULT 'pending',
  chunk_count INT          NOT NULL DEFAULT 0,
  error       VARCHAR(512) NULL,
  indexed_at  DATETIME(3)  NULL,
  updated_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (handout_id),
  KEY idx_material_index_status (status),
  CONSTRAINT fk_material_index_handout FOREIGN KEY (handout_id) REFERENCES handouts (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 助手会话：学生与解题助手的多轮对话容器，归属唯一学生；绑定一个本班智能体（会话内不切换）。
CREATE TABLE IF NOT EXISTS conversations (
  id         BIGINT       NOT NULL AUTO_INCREMENT,
  class_id   BIGINT       NOT NULL,
  user_id    BIGINT       NOT NULL,
  agent_id   BIGINT       NOT NULL,
  title      VARCHAR(255) NOT NULL,
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_conversations_user (user_id, id),
  KEY idx_conversations_agent (agent_id),
  CONSTRAINT fk_conversations_user  FOREIGN KEY (user_id)  REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT fk_conversations_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE,
  CONSTRAINT fk_conversations_agent FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 助手消息：每轮提问与回答；assistant 轮记录引用、当时的提示词快照与技能状态。
CREATE TABLE IF NOT EXISTS messages (
  id              BIGINT       NOT NULL AUTO_INCREMENT,
  conversation_id BIGINT       NOT NULL,
  role            ENUM('user','assistant') NOT NULL,
  content         MEDIUMTEXT   NOT NULL,
  citations       JSON         NULL,
  prompt_used     TEXT         NULL,
  skill_enabled   TINYINT(1)   NULL,
  skills_used     JSON         NULL,
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_messages_conversation (conversation_id, id),
  CONSTRAINT fk_messages_conversation FOREIGN KEY (conversation_id) REFERENCES conversations (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- MCP 服务器：教师登记的本班自建 MCP 工具服务。Bearer 令牌只存服务端，接口响应只回 hasAuth 布尔。
CREATE TABLE IF NOT EXISTS mcp_servers (
  id         BIGINT       NOT NULL AUTO_INCREMENT,
  class_id   BIGINT       NOT NULL,
  name       VARCHAR(120) NOT NULL,
  base_url   VARCHAR(512) NOT NULL,
  auth_token VARCHAR(512) NULL,
  connected  TINYINT(1)   NOT NULL DEFAULT 0,
  tool_cache JSON         NULL,
  last_error VARCHAR(512) NULL,
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_mcp_servers_class_name (class_id, name),
  CONSTRAINT fk_mcp_servers_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- MCP 调用审计：每次工具调用尝试一行；服务器被删后 server_id 置 NULL，仍可读 server_name 快照。
-- 例外：引用服务器在本班已被删除时不写审计（见 tutor 编排）。
CREATE TABLE IF NOT EXISTS mcp_calls (
  id              BIGINT       NOT NULL AUTO_INCREMENT,
  class_id        BIGINT       NOT NULL,
  user_id         BIGINT       NULL,
  server_id       BIGINT       NULL,
  server_name     VARCHAR(120) NOT NULL,
  conversation_id BIGINT       NULL,
  tool_ref        VARCHAR(255) NOT NULL,
  status          ENUM('success', 'denied', 'error') NOT NULL,
  error           VARCHAR(512) NULL,
  duration_ms     INT          NOT NULL DEFAULT 0,
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_mcp_calls_class_created (class_id, created_at),
  CONSTRAINT fk_mcp_calls_class  FOREIGN KEY (class_id)  REFERENCES classes (id) ON DELETE CASCADE,
  CONSTRAINT fk_mcp_calls_user   FOREIGN KEY (user_id)   REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT fk_mcp_calls_server FOREIGN KEY (server_id) REFERENCES mcp_servers (id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
