// Package db 负责 MySQL 连接、启动等待与幂等建表。
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"time"

	"campusclaw/internal/config"

	_ "github.com/go-sql-driver/mysql"
)

// Schema 与 deploy/mysql/01_schema.sql 保持逐字节一致
// （schema_sync_test.go 会强制校验，防止双份漂移）。
//
//go:embed schema.sql
var Schema string

// OpenAndWait 在最长 wait 时间内轮询数据库直到可连接。
func OpenAndWait(cfg *config.Config, wait time.Duration) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&multiStatements=true&loc=Local",
		cfg.MySQL.User,
		cfg.MySQL.Password,
		cfg.MySQL.Host,
		cfg.MySQL.Port,
		cfg.MySQL.Database,
	)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	deadline := time.Now().Add(wait)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return db, nil
		}
		if time.Now().After(deadline) {
			db.Close()
			return nil, fmt.Errorf("等待数据库就绪超时(%v): %w", wait, err)
		}
		log.Printf("等待 MySQL 就绪: %v", err)
		time.Sleep(2 * time.Second)
	}
}

// EnsureSchema 幂等执行内嵌 DDL，作为 MySQL 初始化目录之外的兜底；
// 随后为存量库补齐后期新增列（ALTER 前查 INFORMATION_SCHEMA，保证重复执行不报错）。
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, Schema); err != nil {
		return fmt.Errorf("初始化表结构失败: %w", err)
	}
	if err := ensureColumn(ctx, db, "assistants", "system_prompt",
		"ALTER TABLE assistants ADD COLUMN system_prompt TEXT NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "enabled",
		"ALTER TABLE skills ADD COLUMN enabled TINYINT(1) NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "content",
		"ALTER TABLE skills ADD COLUMN content MEDIUMTEXT NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "source",
		"ALTER TABLE skills ADD COLUMN source VARCHAR(16) NOT NULL DEFAULT 'custom'"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "builtin",
		"ALTER TABLE skills ADD COLUMN builtin TINYINT(1) NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// 技能包：包标识、frontmatter、包目录与内容哈希。
	if err := ensureColumn(ctx, db, "skills", "slug",
		"ALTER TABLE skills ADD COLUMN slug VARCHAR(64) NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "frontmatter",
		"ALTER TABLE skills ADD COLUMN frontmatter JSON NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "dir_path",
		"ALTER TABLE skills ADD COLUMN dir_path VARCHAR(255) NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "skills", "content_hash",
		"ALTER TABLE skills ADD COLUMN content_hash CHAR(64) NULL"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, db, "skills", "uq_skills_assistant_slug",
		"ALTER TABLE skills ADD UNIQUE KEY uq_skills_assistant_slug (assistant_id, slug)"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "messages", "skills_used",
		"ALTER TABLE messages ADD COLUMN skills_used JSON NULL"); err != nil {
		return err
	}
	// 技能名称上限 120 字（设计 §6 校验口径），存量库的 VARCHAR(64) 需幂等扩宽。
	if err := ensureVarcharMin(ctx, db, "skills", "name", 120,
		"ALTER TABLE skills MODIFY COLUMN name VARCHAR(120) NOT NULL"); err != nil {
		return err
	}
	// 多智能体变更：智能体三表（由内嵌 Schema 幂等建表）与 conversations.agent_id 迁移回填。
	return migrateAgents(ctx, db)
}

// migrateAgents 幂等迁移多智能体数据模型：
// agents/agent_skills/agent_materials 建表由内嵌 Schema 完成；此处补 conversations.agent_id
// 可空列 → 一次性回填 → 收紧为非空并补索引/外键。回填仅在列首次新增时执行，
// 避免教师后续解绑技能/材料被重启流程重新绑定。
func migrateAgents(ctx context.Context, db *sql.DB) error {
	added, err := addColumnIfMissing(ctx, db, "conversations", "agent_id",
		"ALTER TABLE conversations ADD COLUMN agent_id BIGINT NULL")
	if err != nil {
		return err
	}
	if added {
		if err := backfillAgents(ctx, db); err != nil {
			return err
		}
	}
	if err := ensureColumnNotNull(ctx, db, "conversations", "agent_id",
		"ALTER TABLE conversations MODIFY COLUMN agent_id BIGINT NOT NULL"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, db, "conversations", "idx_conversations_agent",
		"ALTER TABLE conversations ADD INDEX idx_conversations_agent (agent_id)"); err != nil {
		return err
	}
	return ensureForeignKey(ctx, db, "conversations", "fk_conversations_agent",
		"ALTER TABLE conversations ADD CONSTRAINT fk_conversations_agent FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE RESTRICT")
}

// backfillAgents 一次性回填存量数据（仅在 conversations.agent_id 首次新增时执行）：
// 每个班级生成默认智能体（沿用 assistants 的 name/system_prompt），绑定该班全部技能与讲义，
// 并回填该班会话的 agent_id。全部语句幂等（NOT EXISTS / INSERT IGNORE / WHERE NULL）。
func backfillAgents(ctx context.Context, db *sql.DB) error {
	steps := []struct {
		desc string
		sql  string
	}{
		{"默认智能体", `INSERT INTO agents (class_id, name, system_prompt)
			SELECT a.class_id, a.name, a.system_prompt
			FROM assistants a
			JOIN (SELECT class_id, MIN(id) AS id FROM assistants GROUP BY class_id) m ON m.id = a.id
			WHERE NOT EXISTS (SELECT 1 FROM agents g WHERE g.class_id = a.class_id)`},
		{"兜底智能体", `INSERT INTO agents (class_id, name, system_prompt)
			SELECT c.class_id, '默认智能体', NULL
			FROM conversations c
			WHERE NOT EXISTS (SELECT 1 FROM agents g WHERE g.class_id = c.class_id)
			GROUP BY c.class_id`},
		{"绑定技能", `INSERT IGNORE INTO agent_skills (agent_id, skill_id)
			SELECT d.id, s.id
			FROM (SELECT class_id, MIN(id) AS id FROM agents GROUP BY class_id) d
			JOIN assistants a ON a.class_id = d.class_id
			JOIN skills s ON s.assistant_id = a.id`},
		{"绑定材料", `INSERT IGNORE INTO agent_materials (agent_id, handout_id)
			SELECT d.id, h.id
			FROM (SELECT class_id, MIN(id) AS id FROM agents GROUP BY class_id) d
			JOIN handouts h ON h.class_id = d.class_id`},
		{"回填会话", `UPDATE conversations c
			JOIN (SELECT class_id, MIN(id) AS id FROM agents GROUP BY class_id) d ON d.class_id = c.class_id
			SET c.agent_id = d.id
			WHERE c.agent_id IS NULL`},
	}
	for _, step := range steps {
		if _, err := db.ExecContext(ctx, step.sql); err != nil {
			return fmt.Errorf("回填%s失败: %w", step.desc, err)
		}
	}
	return nil
}

// addColumnIfMissing 在列缺失时补列并返回 true（表示本次确实新增）。
func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, ddl string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("检查 %s.%s 列失败: %w", table, column, err)
	}
	if n > 0 {
		return false, nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return false, fmt.Errorf("补齐 %s.%s 列失败: %w", table, column, err)
	}
	return true, nil
}

// ensureColumnNotNull 在列可空时改为 NOT NULL；已是非空则跳过（幂等）。
func ensureColumnNotNull(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	var nullable string
	err := db.QueryRowContext(ctx,
		`SELECT IS_NULLABLE FROM INFORMATION_SCHEMA.COLUMNS
		  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&nullable)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("列 %s.%s 不存在，无法收紧为非空", table, column)
	}
	if err != nil {
		return fmt.Errorf("检查 %s.%s 是否可空失败: %w", table, column, err)
	}
	if nullable == "NO" {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("收紧 %s.%s 非空失败: %w", table, column, err)
	}
	return nil
}

// ensureIndex 在索引缺失时创建；已存在则跳过（幂等）。
func ensureIndex(ctx context.Context, db *sql.DB, table, index, ddl string) error {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
		  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`,
		table, index).Scan(&n)
	if err != nil {
		return fmt.Errorf("检查 %s 索引 %s 失败: %w", table, index, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("创建 %s 索引 %s 失败: %w", table, index, err)
	}
	return nil
}

// ensureForeignKey 在外键缺失时创建；已存在则跳过（幂等）。
func ensureForeignKey(ctx context.Context, db *sql.DB, table, constraint, ddl string) error {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
		  WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = ?
		    AND CONSTRAINT_NAME = ? AND CONSTRAINT_TYPE = 'FOREIGN KEY'`,
		table, constraint).Scan(&n)
	if err != nil {
		return fmt.Errorf("检查 %s 外键 %s 失败: %w", table, constraint, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("创建 %s 外键 %s 失败: %w", table, constraint, err)
	}
	return nil
}

// ensureVarcharMin 在 VARCHAR 列长度低于 minLen 时执行扩宽 DDL；达标则跳过。
func ensureVarcharMin(ctx context.Context, db *sql.DB, table, column string, minLen int, ddl string) error {
	var n sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT CHARACTER_MAXIMUM_LENGTH FROM INFORMATION_SCHEMA.COLUMNS
		  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&n)
	if err != nil {
		return fmt.Errorf("检查 %s.%s 列宽失败: %w", table, column, err)
	}
	if n.Valid && n.Int64 >= int64(minLen) {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("扩宽 %s.%s 列失败: %w", table, column, err)
	}
	return nil
}

// ensureColumn 在列缺失时执行补列 DDL；已存在则跳过（幂等升级存量库）。
func ensureColumn(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&n)
	if err != nil {
		return fmt.Errorf("检查 %s.%s 列失败: %w", table, column, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("补齐 %s.%s 列失败: %w", table, column, err)
	}
	return nil
}
