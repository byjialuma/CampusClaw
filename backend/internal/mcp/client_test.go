package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rpcEcho 是测试用的最小 MCP 服务：按方法名回预置 result，并校验 Bearer 令牌。
func newFakeMCP(t *testing.T, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Errorf("请求路径应为 /mcp，实际 %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization 应为 Bearer %s，实际 %q", token, got)
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "campus-tools", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "formula_hint", "description": "公式提示", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "公式提示：x = (-b ± √Δ) / (2a)"}},
				"isError": false,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
}

// 成功路径：initialize / tools/list / tools/call 均返回结构正确的结果，且携带 Bearer 令牌。
func TestClientSuccess(t *testing.T) {
	srv := newFakeMCP(t, "secret")
	defer srv.Close()

	c := NewClient(srv.URL, "secret", 2*time.Second)

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize 失败: %v", err)
	}
	if info.ProtocolVersion != "2024-11-05" || info.Name != "campus-tools" || info.Version != "1.0.0" {
		t.Fatalf("initialize 结果错误: %+v", info)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list 失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "formula_hint" || tools[0].Description != "公式提示" {
		t.Fatalf("tools/list 结果错误: %+v", tools)
	}

	text, err := c.CallTool(context.Background(), "formula_hint", map[string]any{"query": "一元二次方程"})
	if err != nil {
		t.Fatalf("tools/call 失败: %v", err)
	}
	if !strings.Contains(text, "公式提示") {
		t.Fatalf("tools/call 文本错误: %q", text)
	}
}

// 令牌错误（服务端 401）：归一为可读错误且不进入结果解析。
func TestClientUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "wrong", 2*time.Second)
	_, err := c.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("期望包含 401 的可读错误，实际 %v", err)
	}
}

// 超时：客户端超时上限生效，返回可读错误。
func TestClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t", 30*time.Millisecond)
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("超时场景应返回错误")
	}
}

// 非法响应：非 JSON 正文归一为可读错误。
func TestClientInvalidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<<< not json >>>"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t", 2*time.Second)
	_, err := c.CallTool(context.Background(), "formula_hint", map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "解析") {
		t.Fatalf("期望解析失败的可读错误，实际 %v", err)
	}
}

// JSON-RPC error 字段：返回带错误码的可读错误。
func TestClientRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t", 2*time.Second)
	_, err := c.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "-32601") {
		t.Fatalf("期望带错误码的可读错误，实际 %v", err)
	}
}

// id 不匹配：识别并报错。
func TestClientIDMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 42, "result": map[string]any{}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t", 2*time.Second)
	_, err := c.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("期望 id 不匹配错误，实际 %v", err)
	}
}

// 工具自称 isError：CallTool 返回错误，便于上层记为出错状态。
func TestClientToolIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "未知工具：nope"}},
				"isError": true,
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t", 2*time.Second)
	if _, err := c.CallTool(context.Background(), "nope", map[string]any{"query": "x"}); err == nil {
		t.Fatal("isError=true 应返回错误")
	}
}

// baseURL 结尾多余的斜杠会被去掉，避免拼出 //mcp。
func TestNewClientTrimsSlash(t *testing.T) {
	c := NewClient("http://example.com/", "t", time.Second)
	if c.baseURL != "http://example.com" {
		t.Fatalf("baseURL 未去尾斜杠: %q", c.baseURL)
	}
}
