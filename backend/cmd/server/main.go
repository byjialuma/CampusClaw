// CampusClaw 服务入口：等待数据库 → 幂等建表 → 清理过期票据 → 播种 →
// 等待 Qdrant → 确保向量集合 → 回填未索引材料 → 启动 HTTP。
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
	"campusclaw/internal/embedding"
	"campusclaw/internal/indexer"
	"campusclaw/internal/search"
	"campusclaw/internal/seed"
	"campusclaw/internal/server"
	"campusclaw/internal/store"
	"campusclaw/internal/vector"
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

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 等待 MySQL 就绪（最长 60 秒）。
	database, err := db.OpenAndWait(cfg, 60*time.Second)
	if err != nil {
		log.Fatalf("数据库不可用: %v", err)
	}
	defer database.Close()

	if err := db.EnsureSchema(rootCtx, database); err != nil {
		log.Fatalf("建表失败: %v", err)
	}

	storage, err := store.NewStorage(filepath.Join(cfg.DataDir, "materials"))
	if err != nil {
		log.Fatalf("初始化材料存储失败: %v", err)
	}

	tickets := &store.TicketRepository{DB: database, TTL: downloadTicketTTL}
	if err := tickets.DeleteExpired(rootCtx); err != nil {
		log.Printf("清理过期下载票据失败（继续启动）: %v", err)
	}

	if err := seed.Run(rootCtx, database, storage, cfg); err != nil {
		log.Fatalf("写入种子数据失败: %v", err)
	}

	// ---- 向量检索组件装配 ----
	embedder, err := embedding.New(cfg.Embedding)
	if err != nil {
		log.Fatalf("初始化向量化器失败: %v", err)
	}
	vectors := vector.New(cfg.Qdrant.URL)
	if err := waitForQdrant(rootCtx, vectors, 60*time.Second); err != nil {
		log.Fatalf("向量库不可用: %v", err)
	}
	if err := vectors.EnsureCollection(rootCtx, cfg.Embedding.Dim); err != nil {
		log.Fatalf("确保向量集合失败: %v", err)
	}

	states := &store.IndexStateRepository{DB: database}
	materialsRepo := &store.MaterialRepository{DB: database, Storage: storage}
	idxQueue := indexer.NewQueue(&indexer.Service{
		States:    states,
		Materials: materialsRepo,
		Embedder:  embedder,
		Vectors:   vectors,
	})
	idxQueue.Start(rootCtx)

	// 回填：从未索引（无 material_index 行）与上次失败的材料重新入队。
	pending, err := states.ListPendingHandoutIDs(rootCtx)
	if err != nil {
		log.Printf("查询待索引材料失败（跳过启动回填）: %v", err)
	} else {
		for _, id := range pending {
			if err := idxQueue.Enqueue(rootCtx, id); err != nil {
				log.Printf("材料 %d 启动回填入队失败: %v", id, err)
			}
		}
		if len(pending) > 0 {
			log.Printf("已回填 %d 份待索引材料", len(pending))
		}
	}

	api := &server.API{
		Users:     &auth.Repository{DB: database, TTL: cfg.SessionTTL},
		Materials: materialsRepo,
		Tickets:   tickets,
		DB:        database,
		Indexer:   idxQueue,
		Vectors:   vectors,
		SearchSvc: &search.Service{
			Embedder: embedder,
			Vectors:  vectors,
			TopK:     cfg.Qdrant.TopK,
		},
		SessionTTL: cfg.SessionTTL,
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

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

// waitForQdrant 轮询 Qdrant 健康端点直到就绪或超时。
func waitForQdrant(ctx context.Context, c *vector.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = c.Ping(pingCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(2 * time.Second)
	}
}
