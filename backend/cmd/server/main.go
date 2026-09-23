// CampusClaw 服务入口：等待数据库 → 幂等建表 → 清理过期票据 → 播种 → 启动 HTTP。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"campusclaw/internal/auth"
	"campusclaw/internal/config"
	"campusclaw/internal/db"
	"campusclaw/internal/seed"
	"campusclaw/internal/server"
	"campusclaw/internal/store"
)

// downloadTicketTTL 是一次性下载票据的有效期。
const downloadTicketTTL = 60 * time.Second

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("campusclaw ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	// 等待 MySQL 就绪（最长 60 秒）。
	database, err := db.OpenAndWait(cfg, 60*time.Second)
	if err != nil {
		log.Fatalf("数据库不可用: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	if err := db.EnsureSchema(ctx, database); err != nil {
		log.Fatalf("建表失败: %v", err)
	}

	storage, err := store.NewStorage(filepath.Join(cfg.DataDir, "materials"))
	if err != nil {
		log.Fatalf("初始化材料存储失败: %v", err)
	}

	tickets := &store.TicketRepository{DB: database, TTL: downloadTicketTTL}
	if err := tickets.DeleteExpired(ctx); err != nil {
		log.Printf("清理过期下载票据失败（继续启动）: %v", err)
	}

	if err := seed.Run(ctx, database, storage, cfg); err != nil {
		log.Fatalf("写入种子数据失败: %v", err)
	}

	api := &server.API{
		Users:      &auth.Repository{DB: database, TTL: cfg.SessionTTL},
		Materials:  &store.MaterialRepository{DB: database, Storage: storage},
		Tickets:    tickets,
		DB:         database,
		SessionTTL: cfg.SessionTTL,
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("HTTP 服务监听 %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Printf("收到退出信号，开始关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
}
