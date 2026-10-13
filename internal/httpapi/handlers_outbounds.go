package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/xrayctl"
)

// ---------------------------------------------------------------------------
// 出站
// ---------------------------------------------------------------------------

type outboundReq struct {
	ServerID    string         `json:"server_id"`
	Tag         string         `json:"tag"`
	Protocol    string         `json:"protocol"`
	Settings    map[string]any `json:"settings"`
	Stream      map[string]any `json:"stream"`
	ProxyTag    string         `json:"proxy_tag"`
	SendThrough string         `json:"send_through"`
	Remark      string         `json:"remark"`
	Enabled     *bool          `json:"enabled"`
	Sort        int            `json:"sort"`
}

func (s *Server) handleListOutbounds(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListOutbounds(r.Context(), r.URL.Query().Get("server_id"))
	if err != nil {
		mapError(s.log, w, err, "list outbounds")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outbounds": list, "total": len(list)})
}

func (s *Server) handleCreateOutbound(w http.ResponseWriter, r *http.Request) {
	var req outboundReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.ServerID == "" || strings.TrimSpace(req.Tag) == "" || strings.TrimSpace(req.Protocol) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "server_id / tag / protocol 必填")
		return
	}
	if err := s.store.CheckServerExists(r.Context(), req.ServerID); err != nil {
		mapError(s.log, w, err, "create outbound check server")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	settings, _ := json.Marshal(req.Settings)
	stream, _ := json.Marshal(req.Stream)
	o, err := s.store.CreateOutbound(r.Context(), storage.Outbound{
		ServerID: req.ServerID, Tag: req.Tag, Protocol: req.Protocol,
		Settings: string(settings), Stream: string(stream),
		ProxyTag: req.ProxyTag, SendThrough: req.SendThrough, Remark: req.Remark,
		Enabled: enabled, Sort: req.Sort,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "duplicate", "该服务器上已存在同名出站 tag")
			return
		}
		mapError(s.log, w, err, "create outbound")
		return
	}
	// 链式引用不存在时只提示不回滚：用户可能打算稍后再建前置出站
	if o.ProxyTag != "" {
		all, _ := s.store.ListOutbounds(r.Context(), o.ServerID)
		found := false
		for _, x := range all {
			if x.Tag == o.ProxyTag && x.Enabled {
				found = true
			}
		}
		if !found {
			writeJSON(w, http.StatusCreated, map[string]any{
				"outbound": o,
				"warning": "前置出站 " + o.ProxyTag +
					" 当前不存在或未启用，下发配置前请先创建（否则 Xray 会启动失败）",
			})
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"outbound": o})
}

func (s *Server) handleUpdateOutbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur, err := s.store.GetOutbound(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update outbound get")
		return
	}
	var req outboundReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Tag != "" {
		cur.Tag = req.Tag
	}
	if req.Protocol != "" {
		cur.Protocol = req.Protocol
	}
	if req.Settings != nil {
		b, _ := json.Marshal(req.Settings)
		cur.Settings = string(b)
	}
	if req.Stream != nil {
		b, _ := json.Marshal(req.Stream)
		cur.Stream = string(b)
	}
	cur.ProxyTag = req.ProxyTag
	cur.SendThrough = req.SendThrough
	cur.Remark = req.Remark
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	cur.Sort = req.Sort
	if err := s.store.UpdateOutbound(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "update outbound")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outbound": cur})
}

func (s *Server) handleDeleteOutbound(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteOutbound(r.Context(), chi.URLParam(r, "id")); err != nil {
		if strings.Contains(err.Error(), "引用") {
			writeErr(w, http.StatusConflict, "in_use", err.Error())
			return
		}
		mapError(s.log, w, err, "delete outbound")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 路由规则
// ---------------------------------------------------------------------------

type routingRuleReq struct {
	ServerID    string   `json:"server_id"`
	Sort        int      `json:"sort"`
	Kind        string   `json:"kind"`
	Values      []string `json:"values"`
	OutboundTag string   `json:"outbound_tag"`
	Remark      string   `json:"remark"`
	Enabled     *bool    `json:"enabled"`
}

func (s *Server) handleListRoutingRules(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRoutingRules(r.Context(), r.URL.Query().Get("server_id"))
	if err != nil {
		mapError(s.log, w, err, "list routing rules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rules": list, "total": len(list), "kinds": xrayctl.RuleKindNames(),
	})
}

func (s *Server) handleCreateRoutingRule(w http.ResponseWriter, r *http.Request) {
	var req routingRuleReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.ServerID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少 server_id")
		return
	}
	if !xrayctl.ValidRuleKind(req.Kind) {
		writeErr(w, http.StatusBadRequest, "bad_request",
			"不支持的规则类型 "+req.Kind+"；可用: "+strings.Join(xrayctl.RuleKindNames(), ", "))
		return
	}
	if len(req.Values) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "匹配值不能为空")
		return
	}
	if strings.TrimSpace(req.OutboundTag) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "必须指定出站 tag")
		return
	}
	// 出站必须存在（direct / block 是内置的）
	if req.OutboundTag != "direct" && req.OutboundTag != "block" {
		outs, _ := s.store.ListOutbounds(r.Context(), req.ServerID)
		found := false
		for _, o := range outs {
			if o.Tag == req.OutboundTag && o.Enabled {
				found = true
			}
		}
		if !found {
			writeErr(w, http.StatusBadRequest, "bad_request",
				"出站 "+req.OutboundTag+" 不存在或未启用")
			return
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	vals, _ := json.Marshal(req.Values)
	rule, err := s.store.CreateRoutingRule(r.Context(), storage.RoutingRule{
		ServerID: req.ServerID, Sort: req.Sort, Kind: req.Kind, Values: string(vals),
		OutboundTag: req.OutboundTag, Remark: req.Remark, Enabled: enabled,
	})
	if err != nil {
		mapError(s.log, w, err, "create routing rule")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": rule})
}

func (s *Server) handleUpdateRoutingRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	all, err := s.store.ListRoutingRules(r.Context(), "")
	if err != nil {
		mapError(s.log, w, err, "update routing rule list")
		return
	}
	var cur storage.RoutingRule
	found := false
	for _, x := range all {
		if x.ID == id {
			cur, found = x, true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "规则不存在")
		return
	}
	var req routingRuleReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Kind != "" {
		if !xrayctl.ValidRuleKind(req.Kind) {
			writeErr(w, http.StatusBadRequest, "bad_request", "不支持的规则类型 "+req.Kind)
			return
		}
		cur.Kind = req.Kind
	}
	if len(req.Values) > 0 {
		b, _ := json.Marshal(req.Values)
		cur.Values = string(b)
	}
	if req.OutboundTag != "" {
		cur.OutboundTag = req.OutboundTag
	}
	cur.Remark = req.Remark
	cur.Sort = req.Sort
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := s.store.UpdateRoutingRule(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "update routing rule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": cur})
}

func (s *Server) handleDeleteRoutingRule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRoutingRule(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete routing rule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
