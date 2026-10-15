// Package mcp 实现主机侧（CampusClaw）调用自建 MCP 工具服务的 HTTP JSON-RPC 2.0 客户端。
// 只做请求-响应式的 initialize / tools/list / tools/call，不使用 SSE 或长连接。
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// rpcID 是本客户端固定的请求 id（单次请求-响应，无需并发复用）。
const rpcID = 1

// maxRespBytes 限制单次响应读取字节数，避免远端异常响应耗尽内存。
const maxRespBytes = 1 << 20

// Tool 是 tools/list 返回的单个工具描述（与远端 MCP 服务同形）。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ServerInfo 是 initialize 返回的服务器标识。
type ServerInfo struct {
	ProtocolVersion string
	Name            string
	Version         string
}

// Client 是单个远端 MCP 服务的调用客户端。
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient 构造客户端；timeout <= 0 时不设客户端级超时（仍受调用方 context 约束）。
func NewClient(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

// Initialize 与服务完成握手，返回协议版本与服务器信息。
func (c *Client) Initialize(ctx context.Context) (*ServerInfo, error) {
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := c.do(ctx, "initialize", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return &ServerInfo{
		ProtocolVersion: res.ProtocolVersion,
		Name:            res.ServerInfo.Name,
		Version:         res.ServerInfo.Version,
	}, nil
}

// ListTools 执行工具发现，返回服务暴露的全部工具。
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var res struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.do(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	if res.Tools == nil {
		res.Tools = []Tool{}
	}
	return res.Tools, nil
}

// CallTool 按工具名调用远端工具；入参为单一 query 字符串。
// 工具自称 isError 或远端报错时返回可读错误（供上层记为出错状态）。
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.do(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("工具 %s 返回错误", name)
	}
	var sb strings.Builder
	for _, b := range res.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String(), nil
}

// do 发送一次 JSON-RPC 请求并解析响应；非 2xx、超时、解析失败与 JSON-RPC error 一律归一为可读错误。
func (c *Client) do(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", ID: rpcID, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("构造 MCP 请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造 MCP 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("MCP 服务不可达: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return fmt.Errorf("读取 MCP 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MCP 服务返回 HTTP %d", resp.StatusCode)
	}

	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return fmt.Errorf("解析 MCP 响应失败: %w", err)
	}
	if rpc.Error != nil {
		return fmt.Errorf("MCP 错误 %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.ID != rpcID {
		return fmt.Errorf("MCP 响应 id 不匹配: %d", rpc.ID)
	}
	if out != nil {
		if err := json.Unmarshal(rpc.Result, out); err != nil {
			return fmt.Errorf("解析 MCP 结果失败: %w", err)
		}
	}
	return nil
}
