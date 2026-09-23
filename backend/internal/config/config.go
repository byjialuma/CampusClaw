// Package config 负责从环境变量读取配置并做启动期校验。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是应用运行所需的全部配置。
type Config struct {
	HTTPAddr string

	MySQL MySQLConfig

	DataDir string
	SeedDir string

	SessionTTL time.Duration

	SeedTeacherA   SeedAccount
	SeedStudentA1  SeedAccount
	SeedStudentB1  SeedAccount
}

// MySQLConfig 是数据库连接参数。
type MySQLConfig struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
}

// SeedAccount 是一个预置账号的用户名与初始密码（仅首次播种使用）。
type SeedAccount struct {
	Username string
	Password string
}

// Load 从环境变量加载配置；缺少必需变量时返回列明缺项的错误。
func Load() (*Config, error) {
	var missing []string
	req := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	cfg := &Config{
		HTTPAddr: getenv("HTTP_ADDR", ":8080"),
		MySQL: MySQLConfig{
			Host:     getenv("MYSQL_HOST", "mysql"),
			Port:     getenv("MYSQL_PORT", "3306"),
			Database: getenv("MYSQL_DATABASE", "campusclaw"),
			User:     req("MYSQL_USER"),
			Password: req("MYSQL_PASSWORD"),
		},
		DataDir: getenv("DATA_DIR", "/app/data"),
		SeedDir: getenv("SEED_DIR", "/app/seed/materials"),
		SessionTTL: time.Duration(getenvInt("SESSION_TTL_SECONDS", 28800)) * time.Second,
		SeedTeacherA: SeedAccount{
			Username: req("SEED_TEACHER_A_USERNAME"),
			Password: req("SEED_TEACHER_A_PASSWORD"),
		},
		SeedStudentA1: SeedAccount{
			Username: req("SEED_STUDENT_A1_USERNAME"),
			Password: req("SEED_STUDENT_A1_PASSWORD"),
		},
		SeedStudentB1: SeedAccount{
			Username: req("SEED_STUDENT_B1_USERNAME"),
			Password: req("SEED_STUDENT_B1_PASSWORD"),
		},
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("缺少必需环境变量: %v", missing)
	}
	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
