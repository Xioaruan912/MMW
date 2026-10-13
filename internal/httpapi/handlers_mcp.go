package httpapi

import (
	"io"
	"net/http"

	"github.com/mmx-project/mmx/internal/mcp"
	"github.com/mmx-project/mmx/internal/storage"
)

// handleMCP 处理 MCP（Model Context Protocol）streamable-HTTP 请求。
//
// 只实现 POST：所有工具都是短时操作，不需要 SSE 流式。
// GET / DELETE 按规范返回明确状态，而不是 404 —— 客户端据此能判断
// "服务在，只是不支持该能力"，而不是"地址写错了"。
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeErr(w, http.StatusServiceUnavailable, "mcp_disabled", "MCP 服务未启用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Allow", "POST")
		writeErr(w, http.StatusMethodNotAllowed, "sse_not_supported",
			"MCP 服务只支持 POST（未实现 SSE 流式）")
		return
	case http.MethodDelete:
		// 无状态服务：会话结束无需服务端动作
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持的方法")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "读取请求体失败")
		return
	}

	userID, username, role := UserFrom(r.Context())
	id := mcp.Identity{
		UserID: userID, Username: username, Role: role,
		IsAdmin: role == storage.RoleAdmin,
	}

	resp := s.mcp.Handle(r.Context(), body, id)
	if resp == nil {
		// JSON-RPC 通知：按规范返回 202 且无响应体
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
