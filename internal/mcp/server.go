// Package mcp 实现主控内建的 MCP（Model Context Protocol）服务端。
//
// 传输：streamable-HTTP。客户端 POST 一个 JSON-RPC 2.0 请求到 /mcp，
// 服务端返回 JSON-RPC 响应。不实现 SSE 流式（工具都是短时操作）。
//
// 鉴权：复用主控的接口令牌（Authorization: Bearer mmx_...）。
// 工具权限**完全等同**令牌所属账号 —— 普通用户令牌调用管理员工具会被拒。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// ProtocolVersion 是服务端支持的 MCP 协议版本。
const ProtocolVersion = "2024-11-05"

// 支持的协议版本列表（initialize 时按客户端请求回选）。
var supportedVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

// Identity 是本次请求的身份（由 HTTP 层解析后注入）。
type Identity struct {
	UserID   string
	Username string
	Role     string
	// IsAdmin 决定能否调用管理员工具
	IsAdmin bool
}

// Tool 是一个可被 AI Agent 调用的工具。
type Tool struct {
	Name        string
	Description string
	// InputSchema 是 JSON Schema 形式的入参描述
	InputSchema map[string]any
	// AdminOnly 标记只有管理员可调用
	AdminOnly bool
	Handler   func(ctx context.Context, id Identity, args map[string]any) (string, error)
}

// Server 是 MCP 服务端。
type Server struct {
	log   *slog.Logger
	tools map[string]Tool
	order []string
}

// New 构造 MCP 服务端。
func New(log *slog.Logger, tools []Tool) *Server {
	s := &Server{log: log, tools: map[string]Tool{}}
	for _, t := range tools {
		s.tools[t.Name] = t
		s.order = append(s.order, t.Name)
	}
	return s
}

// ---------------------------------------------------------------------------
// JSON-RPC 报文
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// JSON-RPC 错误码。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Handle 处理一个 JSON-RPC 请求，返回响应（可能是 nil，表示通知类消息无需响应）。
func (s *Server) Handle(ctx context.Context, body []byte, id Identity) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", Error: &rpcError{
			Code: codeParseError, Message: "请求不是合法的 JSON-RPC: " + err.Error(),
		}}
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return s.err(req.ID, codeInvalidRequest, "jsonrpc 必须是 2.0")
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized", "notifications/cancelled":
		return nil // 通知无需响应
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return s.handleToolsList(req, id)
	case "tools/call":
		return s.handleToolsCall(ctx, req, id)
	default:
		return s.err(req.ID, codeMethodNotFound, "不支持的方法: "+req.Method)
	}
}

func (s *Server) err(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func (s *Server) handleInitialize(req rpcRequest) *rpcResponse {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)

	// 回选一个双方都支持的版本；都不支持时用服务端默认版本，
	// 让客户端自己决定要不要继续（比直接报错更宽容）。
	chosen := ProtocolVersion
	for _, v := range supportedVersions {
		if v == p.ProtocolVersion {
			chosen = v
			break
		}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"protocolVersion": chosen,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "mmx-master",
			"version": "1.0.0",
		},
		"instructions": "MMX 主控的运维工具集。可用工具覆盖服务器、节点、用户、套餐、流量与任务下发。" +
			"权限等同你的登录账号；管理员令牌才能调用管理类工具。",
	}}
}

func (s *Server) handleToolsList(req rpcRequest, id Identity) *rpcResponse {
	out := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		if t.AdminOnly && !id.IsAdmin {
			continue // 普通用户看不到管理员工具
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": out}}
}

func (s *Server) handleToolsCall(ctx context.Context, req rpcRequest, id Identity) *rpcResponse {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return s.err(req.ID, codeInvalidParams, "params 解析失败: "+err.Error())
	}
	t, ok := s.tools[p.Name]
	if !ok {
		return s.err(req.ID, codeMethodNotFound, "未知工具: "+p.Name)
	}
	if t.AdminOnly && !id.IsAdmin {
		return s.toolError(req.ID, "权限不足：该工具仅管理员可用")
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	text, err := t.Handler(ctx, id, p.Arguments)
	if err != nil {
		return s.toolError(req.ID, err.Error())
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}}
}

// toolError 按 MCP 规范把工具内部错误表达为 isError 结果，而不是 JSON-RPC 错误。
// 这样 AI Agent 能看到错误内容并自行调整，而不是整轮对话失败。
func (s *Server) toolError(id json.RawMessage, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": "错误: " + msg}},
		"isError": true,
	}}
}

// ---------------------------------------------------------------------------
// 参数读取辅助
// ---------------------------------------------------------------------------

// argString 取字符串参数。
func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// argInt 取整型参数。
func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i)
		}
	}
	return def
}

// argBool 取布尔参数。
func argBool(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// argMap 取对象参数。
func argMap(args map[string]any, key string) map[string]any {
	v, ok := args[key]
	if !ok {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// jsonText 把任意值序列化成便于阅读的 JSON 文本。
func jsonText(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
