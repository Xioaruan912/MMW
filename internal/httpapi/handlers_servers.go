package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// ---------------------------------------------------------------------------
// 服务器列表 / 详情
// ---------------------------------------------------------------------------

type serverView struct {
	storage.Server
	Online   bool                       `json:"online"`
	Metrics  *agentproto.MetricsPayload `json:"metrics,omitempty"`
	LastSeen string                     `json:"last_seen_human"`
}

func (s *Server) viewServer(r *http.Request, sv storage.Server, withMetrics bool) serverView {
	v := serverView{
		Server: sv,
		Online: s.hub.IsOnline(sv.ID),
	}
	if sv.LastSeenAt > 0 {
		v.LastSeen = time.UnixMilli(sv.LastSeenAt).Format(time.RFC3339)
	}
	if withMetrics {
		if m, ok, err := s.store.LatestMetrics(r.Context(), sv.ID); err == nil && ok {
			v.Metrics = &agentproto.MetricsPayload{
				TS: m.TS, CPUPercent: m.CPUPercent,
				MemUsed: m.MemUsed, MemTotal: m.MemTotal,
				DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal,
				Load1: m.Load1, Load5: m.Load5, Load15: m.Load15,
				NetUp: m.NetUp, NetDown: m.NetDown,
				TCPConns: m.TCPConns, UDPConns: m.UDPConns,
				Processes: m.Processes, UptimeSec: m.UptimeSec, XrayRunning: m.XrayRunning,
			}
		}
	}
	return v
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListServers(r.Context())
	if err != nil {
		mapError(s.log, w, err, "list servers")
		return
	}
	out := make([]serverView, 0, len(list))
	for _, sv := range list {
		out = append(out, s.viewServer(r, sv, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "total": len(out)})
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	sv, err := s.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(s.log, w, err, "get server")
		return
	}
	tokens, _ := s.store.ListTokens(r.Context(), sv.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"server": s.viewServer(r, sv, true),
		"tokens": tokens,
	})
}

// ---------------------------------------------------------------------------
// 增删改
// ---------------------------------------------------------------------------

type createServerReq struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Remark   string `json:"remark"`
	XrayMode string `json:"xray_mode"`
}

// handleCreateServer 新建服务器，并返回一次性入驻令牌与安装命令。
func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	_, username, _ := UserFrom(r.Context())

	created, err := s.reg.CreateServer(r.Context(), req.Name, req.Address, req.Remark, req.XrayMode, username)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"server":       s.viewServer(r, created.Server, false),
		"enroll_token": created.EnrollToken, // 明文仅此一次
		"expires_at":   created.ExpiresAt,
		"install_cmd":  s.installCommand(created.Server, created.EnrollToken),
	})
}

type updateServerReq struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Remark   string `json:"remark"`
	XrayMode string `json:"xray_mode"`
}

func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateServerReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	cur, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update server get")
		return
	}
	if req.Name == "" {
		req.Name = cur.Name
	}
	if req.XrayMode == "" {
		req.XrayMode = cur.XrayMode
	}
	if err := s.store.UpdateServer(r.Context(), id, req.Name, req.Address, req.Remark, req.XrayMode); err != nil {
		mapError(s.log, w, err, "update server")
		return
	}
	updated, _ := s.store.GetServer(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"server": s.viewServer(r, updated, false)})
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, username, _ := UserFrom(r.Context())
	if err := s.store.DeleteServer(r.Context(), id); err != nil {
		mapError(s.log, w, err, "delete server")
		return
	}
	s.store.Audit(r.Context(), storage.AuditServerDelete, username, id, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 令牌
// ---------------------------------------------------------------------------

// handleRotateEnrollToken 重新签发一次性入驻令牌（吊销旧的）。
func (s *Server) handleRotateEnrollToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, username, _ := UserFrom(r.Context())
	plain, _, exp, err := s.reg.IssueEnrollToken(r.Context(), id, username)
	if err != nil {
		mapError(s.log, w, err, "rotate enroll token")
		return
	}
	sv, _ := s.store.GetServer(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{
		"enroll_token": plain,
		"expires_at":   exp,
		"install_cmd":  s.installCommand(sv, plain),
	})
}

// handleRotateAgentToken 重新签发长期 agent 令牌。
// 注意：老令牌不会立即失效（给 Agent 轮换留窗口），由管理员在合适时机吊销。
func (s *Server) handleRotateAgentToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, username, _ := UserFrom(r.Context())
	plain, tok, err := s.reg.IssueAgentToken(r.Context(), id, username)
	if err != nil {
		mapError(s.log, w, err, "rotate agent token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_token": plain,
		"token_id":    tok.ID,
		"note":        "明文仅此一次；旧令牌仍然有效，请在 Agent 完成切换后手动吊销",
	})
}

// ---------------------------------------------------------------------------
// 指标
// ---------------------------------------------------------------------------

func (s *Server) handleServerMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	to := time.Now()
	from := to.Add(-6 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			from = ts
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			to = ts
		}
	}
	series, err := s.store.SeriesMetrics(r.Context(), id, from, to, 2000)
	if err != nil {
		mapError(s.log, w, err, "server metrics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_id": id, "from": from, "to": to, "points": series,
	})
}

// ---------------------------------------------------------------------------
// 安装命令与任务
// ---------------------------------------------------------------------------

func (s *Server) installCommand(sv storage.Server, enrollToken string) string {
	base := strings.TrimRight(s.cfg.BaseURL(), "/")
	mode := sv.XrayMode
	if mode == "" {
		mode = storage.XrayModeExternal
	}
	return "curl -fsSL \"" + base + "/api/remote/install.sh?token=" + enrollToken +
		"&xray_mode=" + mode + "\" | bash"
}

// handleInstallCommand 重新展示安装命令（会新签一枚入驻令牌）。
func (s *Server) handleInstallCommand(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sv, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "install command get server")
		return
	}
	_, username, _ := UserFrom(r.Context())
	plain, _, exp, err := s.reg.IssueEnrollToken(r.Context(), id, username)
	if err != nil {
		mapError(s.log, w, err, "install command issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"install_cmd":  s.installCommand(sv, plain),
		"enroll_token": plain,
		"expires_at":   exp,
	})
}

type dispatchTaskReq struct {
	Kind string             `json:"kind"`
	Args agentproto.RawArgs `json:"args"`
}

// handleDispatchTask 下发一个白名单任务并等待结果。
func (s *Server) handleDispatchTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, username, _ := UserFrom(r.Context())

	var req dispatchTaskReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if !agentproto.KnownTask(req.Kind) {
		writeErr(w, http.StatusBadRequest, "unknown_task", "任务不在白名单内")
		return
	}
	res, err := s.hub.DispatchTask(r.Context(), id, req.Kind, req.Args, username)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "task_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Server) handleTaskKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"kinds": agentproto.TaskKinds()})
}

// handleListTasks 查询任务历史（含超时与失败记录）。
//
// 任务已持久化到 pending_tasks，因此这里能看到完整的下发历史，
// 而不是只有内存中还在等待的那几个。
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	serverID := r.URL.Query().Get("server_id")
	list, err := s.store.ListTasks(r.Context(), serverID, 100)
	if err != nil {
		mapError(s.log, w, err, "list tasks")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": list, "total": len(list)})
}

// ---------------------------------------------------------------------------
// 审计与总览
// ---------------------------------------------------------------------------

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.ListAudit(r.Context(), 200)
	if err != nil {
		mapError(s.log, w, err, "list audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "total": len(logs)})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		mapError(s.log, w, err, "overview")
		return
	}
	var online int
	for _, sv := range servers {
		if s.hub.IsOnline(sv.ID) {
			online++
		}
	}
	users, _ := s.store.CountUsers(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"servers_total":    len(servers),
		"servers_online":   online,
		"agents_connected": s.hub.Count(),
		"users_total":      users,
		"version":          s.version,
		"uptime_sec":       int(time.Since(s.startedAt).Seconds()),
	})
}
