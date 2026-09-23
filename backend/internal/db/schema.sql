-- CampusClaw 核心结构（由 MySQL 初始化目录执行；Go 后端启动时以同一份文件幂等兜底）
-- 共八张表：班级、用户、讲义、作业、助手、技能、服务端会话、一次性下载票据

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
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  class_id    BIGINT       NOT NULL,
  name        VARCHAR(64)  NOT NULL,
  description VARCHAR(255) NULL,
  created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_assistants_class (class_id),
  CONSTRAINT fk_assistants_class FOREIGN KEY (class_id) REFERENCES classes (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS skills (
  id           BIGINT       NOT NULL AUTO_INCREMENT,
  assistant_id BIGINT       NOT NULL,
  name         VARCHAR(64)  NOT NULL,
  description  VARCHAR(255) NULL,
  created_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_skills_assistant (assistant_id),
  CONSTRAINT fk_skills_assistant FOREIGN KEY (assistant_id) REFERENCES assistants (id) ON DELETE CASCADE
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
