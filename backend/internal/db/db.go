// Package db 负责 MySQL 连接、启动等待与幂等建表。
package db

import (
	"context"
	"database/sql"
	_ "embed"
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
	return ensureColumn(ctx, db, "skills", "enabled",
		"ALTER TABLE skills ADD COLUMN enabled TINYINT(1) NOT NULL DEFAULT 0")
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
