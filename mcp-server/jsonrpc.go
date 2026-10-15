package main

import "encoding/json"

// JSON-RPC 2.0 错误码（本服务用到的子集）。
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// protocolVersion 是本服务声明的 MCP 协议版本。
const protocolVersion = "2024-11-05"

// rpcRequest 是 JSON-RPC 2.0 请求；ID 原样保留以便回显。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse 是 JSON-RPC 2.0 响应，result 与 error 互斥。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Capabilities    capabilities `json:"capabilities"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type capabilities struct {
	Tools map[string]any `json:"tools"`
}

// dispatch 按方法名分发，未知方法返回 -32601。
func dispatch(req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return resultResponse(req.ID, initializeResult{
			ProtocolVersion: protocolVersion,
			ServerInfo:      serverInfo{Name: "campus-tools", Version: "1.0.0"},
			Capabilities:    capabilities{Tools: map[string]any{}},
		})
	case "tools/list":
		return resultResponse(req.ID, toolsListResult{Tools: toolDefinitions()})
	case "tools/call":
		return callTool(req)
	default:
		return errorResponse(req.ID, codeMethodNotFound, "method not found")
	}
}

// callTool 解析 tools/call 参数并执行工具；缺 name/query 返回 -32602。
func callTool(req rpcRequest) rpcResponse {
	var params callParams
	if len(req.Params) == 0 {
		return errorResponse(req.ID, codeInvalidParams, "missing parameter: name")
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResponse(req.ID, codeInvalidParams, "params must be an object")
	}
	if params.Name == "" {
		return errorResponse(req.ID, codeInvalidParams, "missing parameter: name")
	}

	var args callArguments
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return errorResponse(req.ID, codeInvalidParams, "arguments must be an object")
		}
	}
	if args.Query == "" {
		return errorResponse(req.ID, codeInvalidParams, "missing parameter: query")
	}

	text, known := runTool(params.Name, args.Query)
	return resultResponse(req.ID, toolCallResult{
		Content: []contentBlock{{Type: "text", Text: text}},
		IsError: !known,
	})
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type callArguments struct {
	Query string `json:"query"`
}

// resultResponse 构造成功响应。
func resultResponse(id json.RawMessage, v any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: v}
}

// errorResponse 构造错误响应；id 为 nil 时序列化为 null。
func errorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}
