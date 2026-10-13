package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/auth"
	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/subscription"
	"github.com/mmx-project/mmx/internal/xrayctl"
)

// ---------------------------------------------------------------------------
// 入站
// ---------------------------------------------------------------------------

type inboundReq struct {
	ServerID  string                   `json:"server_id"`
	Tag       string                   `json:"tag"`
	Protocol  string                   `json:"protocol"`
	Transport string                   `json:"transport"`
	Security  string                   `json:"security"`
	Port      int                      `json:"port"`
	Listen    string                   `json:"listen"`
	Remark    string                   `json:"remark"`
	Enabled   *bool                    `json:"enabled"`
	NodeName  string                   `json:"node_name"`
	Region    string                   `json:"region"`
	Settings  inbound.ProtocolSettings `json:"settings"`
	Stream    inbound.StreamSettings   `json:"stream"`
}

func (r inboundReq) toSpec() inbound.Spec {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return inbound.Spec{
		ServerID: r.ServerID, Tag: r.Tag, Protocol: r.Protocol, Transport: r.Transport,
		Security: r.Security, Port: r.Port, Listen: r.Listen, Remark: r.Remark,
		Enabled: enabled, ProtocolSettings: r.Settings, Stream: r.Stream,
	}
}

// handleListInbounds 列出入站。
func (s *Server) handleListInbounds(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListInbounds(r.Context(), r.URL.Query().Get("server_id"))
	if err != nil {
		mapError(s.log, w, err, "list inbounds")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"inbounds":   list,
		"protocols":  inbound.SupportedProtocols(),
		"transports": inbound.SupportedTransports(),
		"securities": inbound.SupportedSecurities(),
	})
}

// handleCreateInbound 创建入站并自动生成同名节点。
func (s *Server) handleCreateInbound(w http.ResponseWriter, r *http.Request) {
	var req inboundReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	spec := req.toSpec()
	spec.Normalize()
	if err := spec.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	nodeName := req.NodeName
	if nodeName == "" {
		nodeName = spec.Tag
	}
	in, node, err := s.store.CreateInboundWithNode(r.Context(), spec.ToStorage(), nodeName, req.Region)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "duplicate", "该服务器上已存在同名 tag")
			return
		}
		mapError(s.log, w, err, "create inbound")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "inbound.create", username, in.ID,
		in.Protocol+"/"+in.Transport+"/"+in.Security+":"+itoa(in.Port), s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"inbound": in, "node": node})
}

// handleGetInbound 取入站详情（含客户端）。
func (s *Server) handleGetInbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	in, err := s.store.GetInbound(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "get inbound")
		return
	}
	clients, _ := s.store.ListClients(r.Context(), id)
	node, _ := s.store.GetNodeByInbound(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{
		"inbound": in, "clients": clients, "node": node,
		"spec": inbound.Parse(in),
	})
}

// handleUpdateInbound 更新入站。
func (s *Server) handleUpdateInbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur, err := s.store.GetInbound(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update inbound get")
		return
	}
	var req inboundReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	req.ServerID = cur.ServerID
	if req.Tag == "" {
		req.Tag = cur.Tag
	}
	spec := req.toSpec()
	spec.Normalize()
	if err := spec.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	next := spec.ToStorage()
	next.ID = id
	if err := s.store.UpdateInbound(r.Context(), next); err != nil {
		mapError(s.log, w, err, "update inbound")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "inbound.update", username, id, "", s.clientIP(r))
	out, _ := s.store.GetInbound(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"inbound": out})
}

// handleDeleteInbound 删除入站。
func (s *Server) handleDeleteInbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteInbound(r.Context(), id); err != nil {
		mapError(s.log, w, err, "delete inbound")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "inbound.delete", username, id, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 客户端凭据
// ---------------------------------------------------------------------------

type addClientReq struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Flow   string `json:"flow"`
}

// handleAddClient 给某用户签发入站凭据。
func (s *Server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	inboundID := chi.URLParam(r, "id")
	if _, err := s.store.GetInbound(r.Context(), inboundID); err != nil {
		mapError(s.log, w, err, "add client get inbound")
		return
	}
	var req addClientReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.UserID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少 user_id")
		return
	}
	u, err := s.store.GetUser(r.Context(), req.UserID)
	if err != nil {
		mapError(s.log, w, err, "add client get user")
		return
	}
	email := req.Email
	if email == "" {
		email = u.Username
	}
	c, err := s.store.UpsertClient(r.Context(), inboundID, req.UserID, email, req.Flow)
	if err != nil {
		mapError(s.log, w, err, "add client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"client": c})
}

// handleListClients 列出某入站的凭据。
func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListClients(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(s.log, w, err, "list clients")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": list})
}

// handleDeleteClient 吊销凭据。
func (s *Server) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeClient(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete client")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 节点展示
// ---------------------------------------------------------------------------

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	views, err := s.store.ListNodeViews(r.Context(), r.URL.Query().Get("server_id"))
	if err != nil {
		mapError(s.log, w, err, "list nodes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": views, "total": len(views)})
}

type updateNodeReq struct {
	Name    string `json:"name"`
	Region  string `json:"region"`
	Tags    string `json:"tags"`
	Sort    int    `json:"sort"`
	Enabled *bool  `json:"enabled"`
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	views, err := s.store.ListNodeViews(r.Context(), "")
	if err != nil {
		mapError(s.log, w, err, "update node list")
		return
	}
	var cur storage.Node
	found := false
	for _, v := range views {
		if v.ID == id {
			cur, found = v.Node, true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
		return
	}
	var req updateNodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Name != "" {
		cur.Name = req.Name
	}
	cur.Region = req.Region
	cur.Tags = req.Tags
	cur.Sort = req.Sort
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := s.store.UpdateNode(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "update node")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": cur})
}

// ---------------------------------------------------------------------------
// Xray 配置下发
// ---------------------------------------------------------------------------

// handleApplyXray 生成配置并下发到 Agent。
func (s *Server) handleApplyXray(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	_, username, _ := UserFrom(r.Context())

	res, err := s.xray.Apply(r.Context(), serverID, username)
	if err != nil {
		// 配置已生成但服务器离线 —— 仍返回 200 与已生成的 rev，让调用方知道状态
		if res != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"result": res})
			return
		}
		writeErr(w, http.StatusBadGateway, "apply_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// handleXrayPreview 预览将要下发的配置（不落地，便于排障）。
func (s *Server) handleXrayPreview(w http.ResponseWriter, r *http.Request) {
	build, err := s.xray.BuildForServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(s.log, w, err, "xray preview")
		return
	}
	raw, _ := xrayctl.Canonical(build.Config)
	writeJSON(w, http.StatusOK, map[string]any{
		"rev": build.Rev, "tags": build.Tags, "config": json.RawMessage(raw),
	})
}

// ---------------------------------------------------------------------------
// 用户
// ---------------------------------------------------------------------------

type createUserReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		mapError(s.log, w, err, "list users")
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, publicUser(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "total": len(out)})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "用户名不能为空")
		return
	}
	if len(req.Password) < auth.MinPasswordLength {
		writeErr(w, http.StatusBadRequest, "bad_request", "口令至少 "+itoa(auth.MinPasswordLength)+" 位")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	nick := req.Nickname
	if nick == "" {
		nick = req.Username
	}
	role := req.Role
	if role != storage.RoleAdmin {
		role = storage.RoleUser
	}
	u, err := s.store.CreateUser(r.Context(), req.Username, hash, nick, req.Email, role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "duplicate", "用户名已存在")
			return
		}
		mapError(s.log, w, err, "create user")
		return
	}
	// 自动签发订阅凭据
	token, _ := auth.GenerateToken()
	_ = s.store.SetUserSubToken(r.Context(), u.ID, token)
	u.SubToken = token

	_, actor, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "user.create", actor, u.ID, u.Username, s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"user": publicUser(u)})
}

type updateUserReq struct {
	Nickname     *string `json:"nickname"`
	Email        *string `json:"email"`
	Status       *string `json:"status"`
	TrafficLimit *int64  `json:"traffic_limit"`
	TrafficUsed  *int64  `json:"traffic_used"`
	ExpireAt     *int64  `json:"expire_at"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update user get")
		return
	}
	var req updateUserReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Nickname != nil {
		u.Nickname = *req.Nickname
	}
	if req.Email != nil {
		u.Email = *req.Email
	}
	if req.Status != nil && (*req.Status == storage.StatusOn || *req.Status == storage.StatusOff) {
		u.Status = *req.Status
	}
	if req.TrafficLimit != nil {
		u.TrafficLimit = *req.TrafficLimit
	}
	if req.TrafficUsed != nil {
		u.TrafficUsed = *req.TrafficUsed
	}
	if req.ExpireAt != nil {
		u.ExpireAt = *req.ExpireAt
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		mapError(s.log, w, err, "update user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	me, _, _ := UserFrom(r.Context())
	if me == id {
		writeErr(w, http.StatusBadRequest, "bad_request", "不能删除当前登录账号")
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		mapError(s.log, w, err, "delete user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRotateSubToken 重置订阅凭据（旧链接立即失效）。
func (s *Server) handleRotateSubToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetUser(r.Context(), id); err != nil {
		mapError(s.log, w, err, "rotate sub token get user")
		return
	}
	token, err := auth.GenerateToken()
	if err != nil {
		mapError(s.log, w, err, "rotate sub token gen")
		return
	}
	if err := s.store.SetUserSubToken(r.Context(), id, token); err != nil {
		mapError(s.log, w, err, "rotate sub token set")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sub_token": token,
		"sub_url":   s.subscriptionURL(token),
	})
}

func (s *Server) subscriptionURL(token string) string {
	return strings.TrimRight(s.cfg.BaseURL(), "/") + "/sub/" + token
}

// ---------------------------------------------------------------------------
// 订阅出口（公开，凭据即权限）
// ---------------------------------------------------------------------------

// handleSubscription 按凭据输出订阅内容；格式由 UA 或 ?format= 决定。
func (s *Server) handleSubscription(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	u, err := s.store.GetUserBySubToken(r.Context(), token)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "订阅不存在或凭据已失效")
		return
	}
	if u.Status != storage.StatusOn {
		writeErr(w, http.StatusForbidden, "disabled", "账号已被禁用")
		return
	}

	format := subscription.Format(r.URL.Query().Get("format"))
	if format == "" {
		format = subscription.DetectFormat(r.UserAgent())
	}

	entries, err := s.buildUserEntries(r, u)
	if err != nil {
		mapError(s.log, w, err, "subscription build entries")
		return
	}
	body, ctype, err := subscription.Render(entries, format)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "render_failed", err.Error())
		return
	}

	// 订阅内容随节点变化，不做长缓存；同时给出客户端所需的元信息头
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Subscription-Userinfo", subscriptionUserInfo(u))
	w.Header().Set("Profile-Update-Interval", "6")
	_, _ = w.Write(body)
}

// buildUserEntries 组装某用户可见的全部节点。
func (s *Server) buildUserEntries(r *http.Request, u storage.User) ([]subscription.Entry, error) {
	clients, err := s.store.ListUserClients(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	byInbound := make(map[string]storage.InboundClient, len(clients))
	for _, c := range clients {
		byInbound[c.InboundID] = c
	}
	if len(byInbound) == 0 {
		return nil, nil
	}
	views, err := s.store.ListNodeViews(r.Context(), "")
	if err != nil {
		return nil, err
	}
	return subscription.BuildEntries(views, byInbound), nil
}

// subscriptionUserInfo 生成 clients 通用的流量信息头。
func subscriptionUserInfo(u storage.User) string {
	upload := u.TrafficUsed / 4
	download := u.TrafficUsed - upload
	var expire int64
	if u.ExpireAt > 0 {
		expire = u.ExpireAt / 1000
	}
	return "upload=" + itoa64(upload) + "; download=" + itoa64(download) +
		"; total=" + itoa64(u.TrafficLimit) + "; expire=" + itoa64(expire)
}
