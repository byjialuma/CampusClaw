// campusclaw-mcp 服务入口：以标准库 net/http 暴露 JSON-RPC 2.0 工具端点。
// 除 GET /health 外，POST /mcp 一律要求正确的 Bearer 令牌。
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// defaultPort 是默认监听端口（可被环境变量 MCP_PORT 覆盖）。
const defaultPort = "8090"

// maxBodyBytes 限制单次请求体大小，避免超大请求占用内存。
const maxBodyBytes = 1 << 20

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("campusclaw-mcp ")

	token := os.Getenv("MCP_AUTH_TOKEN")
	if token == "" {
		log.Fatal("MCP_AUTH_TOKEN 未设置，拒绝以空令牌启动")
	}

	port := os.Getenv("MCP_PORT")
	if port == "" {
		port = defaultPort
	}

	srv := newServer(token)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.newHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("MCP 服务监听 :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Printf("收到退出信号，开始关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
}

// server 持有处理请求所需的共享状态。
type server struct {
	token string
}

// newServer 用给定令牌构造服务实例。
func newServer(token string) *server {
	return &server{token: token}
}

// newHandler 构建路由树：GET /health 无需鉴权，POST /mcp 经令牌校验。
func (s *server) newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /mcp", s.requireAuth(s.mcp))
	return mux
}

// health 健康检查，恒返回 200 与 JSON。
func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// requireAuth 校验 Authorization: Bearer <token>，令牌与配置常量时间比较。
func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// mcp 处理 JSON-RPC 2.0 请求，响应恒为 JSON。
func (s *server) mcp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusOK, errorResponse(nil, codeParseError, "unable to read request body"))
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, errorResponse(nil, codeParseError, "request body is not valid JSON"))
		return
	}

	writeJSON(w, http.StatusOK, dispatch(req))
}

// writeJSON 以 application/json 写出 v。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
