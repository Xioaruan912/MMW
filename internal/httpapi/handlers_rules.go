package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/ruleengine"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/subscription"
)

// ---------------------------------------------------------------------------
// 订阅模板
// ---------------------------------------------------------------------------

func (s *Server) handleListRuleTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRuleTemplates(r.Context())
	if err != nil {
		mapError(s.log, w, err, "list templates")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"templates":    list,
		"total":        len(list),
		"placeholders": []string{ruleengine.PlaceholderProxies, ruleengine.PlaceholderProxyNames},
	})
}

type ruleTemplateReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	CopyFrom    string `json:"copy_from"` // 按名字复制一个已有模板（含内置）
}

// handleCreateRuleTemplate 新建模板；可用 copy_from 从内置模板复制起步。
func (s *Server) handleCreateRuleTemplate(w http.ResponseWriter, r *http.Request) {
	var req ruleTemplateReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.CopyFrom != "" && req.Content == "" {
		src, err := s.store.GetRuleTemplateByName(r.Context(), req.CopyFrom)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "被复制的模板不存在: "+req.CopyFrom)
			return
		}
		req.Content = src.Content
		if req.Name == "" {
			req.Name = src.Name + "-copy"
		}
		if req.Description == "" {
			req.Description = src.Description
		}
	}
	t := storage.RuleTemplate{
		Name: req.Name, Description: req.Description, Content: req.Content, Builtin: false,
	}
	if err := ruleengine.Validate(t); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_template", err.Error())
		return
	}
	created, err := s.store.CreateRuleTemplate(r.Context(), t)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "duplicate", "模板名已存在")
			return
		}
		mapError(s.log, w, err, "create template")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "template.create", username, created.ID, created.Name, s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"template": created})
}

// handleUpdateRuleTemplate 更新模板。
func (s *Server) handleUpdateRuleTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur, err := s.store.GetRuleTemplate(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update template get")
		return
	}
	var req ruleTemplateReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Name != "" {
		cur.Name = req.Name
	}
	cur.Description = req.Description
	if req.Content != "" {
		cur.Content = req.Content
	}
	if err := ruleengine.Validate(cur); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_template", err.Error())
		return
	}
	if err := s.store.UpdateRuleTemplate(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "update template")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": cur})
}

func (s *Server) handleDeleteRuleTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRuleTemplate(r.Context(), chi.URLParam(r, "id")); err != nil {
		if strings.Contains(err.Error(), "内置模板") {
			writeErr(w, http.StatusBadRequest, "builtin_immutable", err.Error())
			return
		}
		mapError(s.log, w, err, "delete template")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePreviewRuleTemplate 用当前节点集预览模板渲染结果（不落库）。
func (s *Server) handlePreviewRuleTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tmpl, err := s.store.GetRuleTemplate(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "preview template get")
		return
	}
	// 预览用当前账号自己的节点；一个节点都没有时用占位节点，
	// 这样管理员至少能看到模板结构是否成立（而不是只看到一句报错）。
	userID, _, _ := UserFrom(r.Context())
	u, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		mapError(s.log, w, err, "preview template get user")
		return
	}
	entries, err := s.buildUserEntries(r, u)
	if err != nil {
		mapError(s.log, w, err, "preview template entries")
		return
	}
	if len(entries) == 0 {
		entries = placeholderEntries()
	}

	body, err := subscription.RenderClashWithTemplate(entries, tmpl, subscription.DefaultClashOptions())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "render_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// ---------------------------------------------------------------------------
// 规则集
// ---------------------------------------------------------------------------

type ruleProviderReq struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Behavior string `json:"behavior"`
	Format   string `json:"format"`
	URL      string `json:"url"`
	Path     string `json:"path"`
	Interval int    `json:"interval"`
	Enabled  *bool  `json:"enabled"`
}

func (s *Server) handleListRuleProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRuleProviders(r.Context())
	if err != nil {
		mapError(s.log, w, err, "list rule providers")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": list, "total": len(list)})
}

func (s *Server) handleCreateRuleProvider(w http.ResponseWriter, r *http.Request) {
	var req ruleProviderReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "名称不能为空")
		return
	}
	if req.Type != "file" && strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "type=http 时必须提供 URL")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, err := s.store.CreateRuleProvider(r.Context(), storage.RuleProvider{
		Name: req.Name, Type: req.Type, Behavior: req.Behavior, Format: req.Format,
		URL: req.URL, Path: req.Path, Interval: req.Interval, Enabled: enabled,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "duplicate", "规则集名已存在")
			return
		}
		mapError(s.log, w, err, "create rule provider")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"provider": p})
}

func (s *Server) handleDeleteRuleProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRuleProvider(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete rule provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 订阅文件
// ---------------------------------------------------------------------------

type subscribeFileReq struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Enabled *bool  `json:"enabled"`
}

func (s *Server) handleListSubscribeFiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSubscribeFiles(r.Context())
	if err != nil {
		mapError(s.log, w, err, "list subscribe files")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": list, "total": len(list)})
}

func (s *Server) handleCreateSubscribeFile(w http.ResponseWriter, r *http.Request) {
	var req subscribeFileReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "名称与内容不能为空")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	f, err := s.store.CreateSubscribeFile(r.Context(), storage.SubscribeFile{
		Name: req.Name, Content: req.Content, Enabled: enabled,
	})
	if err != nil {
		mapError(s.log, w, err, "create subscribe file")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"file": f})
}

func (s *Server) handleDeleteSubscribeFile(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSubscribeFile(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete subscribe file")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUserTemplate 设置/读取用户的订阅模板。
func (s *Server) handleSetUserTemplate(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	var req struct {
		TemplateID string `json:"template_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.TemplateID != "" {
		if _, err := s.store.GetRuleTemplate(r.Context(), req.TemplateID); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "模板不存在")
			return
		}
	}
	if err := s.store.SetUserTemplate(r.Context(), userID, req.TemplateID); err != nil {
		mapError(s.log, w, err, "set user template")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// placeholderEntries 返回一组用于模板预览的占位节点。
//
// 目的：在没有任何真实节点时也能验证模板结构（缩进、占位符位置、YAML 合法性），
// 而不是让管理员只能看到"没有节点"。
func placeholderEntries() []subscription.Entry {
	in := storage.Inbound{
		ID: "inb_preview", ServerID: "srv_preview", Tag: "preview-443",
		Protocol: "vless", Transport: "tcp", Security: "tls", Port: 443,
		Listen: "0.0.0.0", Settings: `{"flow":"xtls-rprx-vision"}`,
		Stream: `{"server_name":"preview.example.com"}`, Enabled: true,
	}
	return []subscription.Entry{{
		Node: storage.NodeView{
			Node:       storage.Node{ID: "nod_preview", InboundID: in.ID, Name: "预览节点", Enabled: true},
			Inbound:    in,
			ServerName: "preview",
			ServerAddr: "203.0.113.1",
		},
		Client: storage.InboundClient{ID: "cli_preview", UUID: "00000000-0000-4000-8000-000000000000", Enabled: true},
		Spec:   inbound.Parse(in),
	}}
}
