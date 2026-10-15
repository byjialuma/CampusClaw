package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const testToken = "s3cr3t-test-token"

// newTestHandler 通过环境变量注入令牌，构造 handler 与 server。
func newTestHandler(t *testing.T) (*server, http.Handler) {
	t.Helper()
	t.Setenv("MCP_AUTH_TOKEN", testToken)
	srv := newServer(os.Getenv("MCP_AUTH_TOKEN"))
	return srv, srv.newHandler()
}

// doMCP 以给定令牌向 /mcp 发送请求体。
func doMCP(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// rawResponse 用于解析 JSON-RPC 响应的原始结构。
type rawResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) rawResponse {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, 期望 application/json; charset=utf-8", ct)
	}
	var resp rawResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	if resp.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q, 期望 2.0", resp.JSONRPC)
	}
	return resp
}

func TestInitialize(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, 期望 200", rec.Code)
	}
	resp := decodeResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("initialize 不应返回错误: %+v", resp.Error)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("解析 result 失败: %v", err)
	}
	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion = %q, 期望 2024-11-05", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != "campus-tools" || result.ServerInfo.Version == "" {
		t.Errorf("serverInfo 不完整: %+v", result.ServerInfo)
	}
	if result.Capabilities.Tools == nil {
		t.Errorf("capabilities.tools 缺失")
	}
}

func TestToolsList(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	resp := decodeResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("tools/list 不应返回错误: %+v", resp.Error)
	}
	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("解析 result 失败: %v", err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("工具数量 = %d, 期望 2", len(result.Tools))
	}
	for _, tool := range result.Tools {
		if tool.Name == "" || tool.Description == "" {
			t.Errorf("工具缺少 name/description: %+v", tool)
		}
		if tool.InputSchema.Type != "object" {
			t.Errorf("工具 %s inputSchema.type = %q, 期望 object", tool.Name, tool.InputSchema.Type)
		}
		if _, ok := tool.InputSchema.Properties["query"]; !ok {
			t.Errorf("工具 %s 缺少 properties.query", tool.Name)
		}
		if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "query" {
			t.Errorf("工具 %s required = %v, 期望 [query]", tool.Name, tool.InputSchema.Required)
		}
	}
}

func TestToolsCallSuccess(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"formula_hint","arguments":{"query":"一元二次方程"}}}`)
	resp := decodeResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("tools/call 不应返回错误: %+v", resp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("解析 result 失败: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Type != "text" || result.Content[0].Text == "" {
		t.Fatalf("content 不合法: %+v", result.Content)
	}
	if result.IsError {
		t.Errorf("命中查询时 isError 应为 false")
	}
}

func TestToolsCallMissStillSuccess(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"unit_convert","arguments":{"query":"hello world"}}}`)
	resp := decodeResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("识别不到查询仍应为成功: %+v", resp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("解析 result 失败: %v", err)
	}
	if result.IsError {
		t.Errorf("识别不到查询时 isError 应为 false")
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Fatalf("未命中时仍应返回用法说明文本")
	}
}

func TestParseError(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0", not-json`)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, 期望 200", rec.Code)
	}
	resp := decodeResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Fatalf("期望 -32700, 实际 %+v", resp.Error)
	}
}

func TestMethodNotFound(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0","id":5,"method":"does/not/exist","params":{}}`)
	resp := decodeResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("期望 -32601, 实际 %+v", resp.Error)
	}
}

func TestInvalidParams(t *testing.T) {
	_, h := newTestHandler(t)
	cases := []struct {
		name string
		body string
	}{
		{"缺 name", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{}}`},
		{"缺 query", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"formula_hint","arguments":{}}}`},
		{"无 params", `{"jsonrpc":"2.0","id":8,"method":"tools/call"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doMCP(t, h, testToken, tc.body)
			resp := decodeResponse(t, rec)
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("期望 -32602, 实际 %+v", resp.Error)
			}
		})
	}
}

func TestAuthNoToken(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌 HTTP 状态 = %d, 期望 401", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("401 响应体应为 JSON: %s", rec.Body.String())
	}
}

func TestAuthWrongToken(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, "wrong-token", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌 HTTP 状态 = %d, 期望 401", rec.Code)
	}
}

func TestAuthCorrectToken(t *testing.T) {
	_, h := newTestHandler(t)
	rec := doMCP(t, h, testToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确令牌 HTTP 状态 = %d, 期望 200", rec.Code)
	}
}

func TestHealthNoAuth(t *testing.T) {
	_, h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("健康检查 HTTP 状态 = %d, 期望 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("健康检查响应应为 JSON: %s", rec.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, 期望 ok", body["status"])
	}
}
