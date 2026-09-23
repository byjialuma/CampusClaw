package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaSingleSource 强制后端内嵌 DDL 与 MySQL 初始化脚本逐字节一致，
// 避免“同一份 schema 两处维护”产生漂移。
func TestSchemaSingleSource(t *testing.T) {
	deployPath := filepath.Join("..", "..", "..", "deploy", "mysql", "01_schema.sql")
	want, err := os.ReadFile(deployPath)
	if err != nil {
		t.Fatalf("读取 deploy schema 失败: %v", err)
	}
	if strings.TrimSpace(Schema) == "" {
		t.Fatal("内嵌 Schema 为空")
	}
	if Schema != string(want) {
		t.Fatal("backend/internal/db/schema.sql 与 deploy/mysql/01_schema.sql 内容不一致")
	}
}
