package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Routes 装配全部路由。
//
// 分层：
//
//	/api/health                公开：健康检查
//	/api/setup/*               公开：首启初始化
//	/api/login /api/logout     公开 / 半公开：会话
//	/api/agent/*               Agent 专用：enroll / ws / pull（用 agent_token 鉴权）
//	/api/remote/*              公开：安装脚本与 Agent 二进制（凭据在 query 里）
//	/api/admin/*               需要管理员会话
//	/api/me                    需要登录
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.mwRequestID, s.mwRecover, s.mwAccessLog)

	r.Get("/api/health", s.handleHealth)

	// ---- Agent 网关 ----
	r.Post("/api/agent/enroll", s.handleAgentEnroll)
	r.Get("/api/agent/ws", s.hub.HandleWS)
	r.Post("/api/agent/pull", s.handleAgentPull)

	// ---- 订阅出口（公开，凭据即权限）----
	r.Get("/sub/{token}", s.handleSubscription)

	// ---- 远程安装 ----
	r.Get("/api/remote/install.sh", s.handleInstallScript)
	r.Get("/api/remote/agent/{platform}", s.handleAgentBinary)

	// ---- 首启与登录 ----
	r.Get("/api/setup/status", s.handleSetupStatus)
	r.Post("/api/setup/init", s.handleSetupInit)
	r.Post("/api/login", s.handleLogin)

	// ---- 需要登录 ----
	r.Group(func(pr chi.Router) {
		pr.Use(s.mwRequireAuth, s.mwCSRF)
		pr.Post("/api/logout", s.handleLogout)
		pr.Get("/api/me", s.handleMe)
	})

	// ---- 需要管理员 ----
	r.Group(func(pr chi.Router) {
		pr.Use(s.mwRequireAuth, s.mwCSRF, s.mwRequireAdmin)

		pr.Get("/api/admin/servers", s.handleListServers)
		pr.Post("/api/admin/servers", s.handleCreateServer)
		pr.Get("/api/admin/servers/{id}", s.handleGetServer)
		pr.Patch("/api/admin/servers/{id}", s.handleUpdateServer)
		pr.Delete("/api/admin/servers/{id}", s.handleDeleteServer)
		pr.Post("/api/admin/servers/{id}/enroll-token", s.handleRotateEnrollToken)
		pr.Post("/api/admin/servers/{id}/agent-token", s.handleRotateAgentToken)
		pr.Get("/api/admin/servers/{id}/metrics", s.handleServerMetrics)
		pr.Get("/api/admin/servers/{id}/install-command", s.handleInstallCommand)
		pr.Post("/api/admin/servers/{id}/tasks", s.handleDispatchTask)

		pr.Get("/api/admin/tasks/kinds", s.handleTaskKinds)
		pr.Get("/api/admin/tasks", s.handleListTasks)
		pr.Get("/api/admin/audit", s.handleListAudit)
		pr.Get("/api/admin/overview", s.handleOverview)

		// ---- 入站 ----
		pr.Get("/api/admin/inbounds", s.handleListInbounds)
		pr.Post("/api/admin/inbounds", s.handleCreateInbound)
		pr.Get("/api/admin/inbounds/{id}", s.handleGetInbound)
		pr.Patch("/api/admin/inbounds/{id}", s.handleUpdateInbound)
		pr.Delete("/api/admin/inbounds/{id}", s.handleDeleteInbound)
		pr.Get("/api/admin/inbounds/{id}/clients", s.handleListClients)
		pr.Post("/api/admin/inbounds/{id}/clients", s.handleAddClient)
		pr.Delete("/api/admin/clients/{id}", s.handleDeleteClient)

		// ---- 节点 ----
		pr.Get("/api/admin/nodes", s.handleListNodes)
		pr.Patch("/api/admin/nodes/{id}", s.handleUpdateNode)

		// ---- Xray 配置 ----
		pr.Post("/api/admin/servers/{id}/apply", s.handleApplyXray)
		pr.Get("/api/admin/servers/{id}/xray-preview", s.handleXrayPreview)

		// ---- 用户 ----
		pr.Get("/api/admin/users", s.handleListUsers)
		pr.Post("/api/admin/users", s.handleCreateUser)
		pr.Patch("/api/admin/users/{id}", s.handleUpdateUser)
		pr.Delete("/api/admin/users/{id}", s.handleDeleteUser)
		pr.Post("/api/admin/users/{id}/sub-token", s.handleRotateSubToken)
	})

	// ---- 前端静态资源 ----
	if s.webFS != nil {
		r.NotFound(s.serveSPA)
	} else {
		r.Get("/", s.handleRootPlaceholder)
	}

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    s.version,
		"uptime_sec": int(time.Since(s.startedAt).Seconds()),
		"agents":     s.hub.Count(),
		"time":       time.Now().UnixMilli(),
	})
}

func (s *Server) handleRootPlaceholder(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not_found", "未找到")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "mmx-master",
		"version": s.version,
		"note":    "前端尚未构建（make web）。当前仅提供 API。",
		"health":  "/api/health",
	})
}
