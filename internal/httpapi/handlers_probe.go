package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mmx-project/mmx/internal/probe"
	"github.com/mmx-project/mmx/internal/storage"
)

// ProbeTokenHeader 是外部探针携带访问令牌的请求头。
const ProbeTokenHeader = "X-Mmwx-Probe-Token"

// probeAuthorized 校验探针访问权限。
//
// 未开启 require_token 时同源开放；开启后必须带正确令牌。
// 注意：探针关闭时统一返回 404 —— 不暴露"这里有个探针"的存在性。
func (s *Server) probeAuthorized(r *http.Request) (probe.Settings, bool) {
	st, err := s.probe.LoadSettings(r.Context())
	if err != nil || !st.Enabled {
		return st, false
	}
	if !st.RequireToken {
		return st, true
	}
	got := r.Header.Get(ProbeTokenHeader)
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	if st.Token == "" || got != st.Token {
		return st, false
	}
	return st, true
}

// handleProbeServers 返回探针快照。
func (s *Server) handleProbeServers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.probeAuthorized(r); !ok {
		writeErr(w, http.StatusNotFound, "not_found", "未找到")
		return
	}
	snap, err := s.probe.Snapshot(r.Context())
	if err != nil {
		mapError(s.log, w, err, "probe snapshot")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, snap)
}

// handleProbeWS 每 RefreshSeconds 推送一次快照。
func (s *Server) handleProbeWS(w http.ResponseWriter, r *http.Request) {
	st, ok := s.probeAuthorized(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "未找到")
		return
	}
	ws, err := upgraderProbe.Upgrade(w, r, nil)
	if err != nil {
		s.log.Debug("探针 WS 升级失败", "err", err)
		return
	}
	defer ws.Close()

	interval := time.Duration(st.RefreshSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	ctx := r.Context()
	send := func() bool {
		snap, err := s.probe.Snapshot(ctx)
		if err != nil {
			return false
		}
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteJSON(snap) == nil
	}

	if !send() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !send() {
				return
			}
		}
	}
}

// handleProbeSeries 返回历史序列。
//
// kind 支持：
//
//	system  —— CPU / 内存 / 负载 / 网速（来自 server_metrics）
//	traffic —— 日流量（来自 traffic_daily）
//
// latency 需要节点真连探测（P8），当前明确报错而不是返回空数组 ——
// 返回空数组会让前端以为"探测了但没数据"，排障时方向完全错。
func (s *Server) handleProbeSeries(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.probeAuthorized(r); !ok {
		writeErr(w, http.StatusNotFound, "not_found", "未找到")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "system"
	}
	serverID := r.URL.Query().Get("server_id")

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

	switch kind {
	case "system":
		if serverID == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "kind=system 需要 server_id")
			return
		}
		pts, err := s.store.SeriesMetrics(r.Context(), serverID, from, to, 2000)
		if err != nil {
			mapError(s.log, w, err, "probe series system")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": kind, "server_id": serverID, "from": from, "to": to, "points": pts,
		})

	case "traffic":
		days := 30
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				days = n
			}
		}
		var (
			sum storage.TrafficSummary
			err error
		)
		if serverID != "" {
			sum, err = s.store.ServerTrafficSummary(r.Context(), serverID, days)
		} else {
			sum, err = s.store.AllServersTrafficSummary(r.Context(), days)
		}
		if err != nil {
			mapError(s.log, w, err, "probe series traffic")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": kind, "server_id": serverID, "days": days, "series": sum.Series,
			"up": sum.Up, "down": sum.Down, "total": sum.Total,
		})

	case "latency":
		writeErr(w, http.StatusNotImplemented, "not_implemented",
			"节点延迟历史尚未实现（需要节点真连探测，计划于 P8）")

	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "kind 必须是 system / traffic / latency")
	}
}

// ---------------------------------------------------------------------------
// 管理端
// ---------------------------------------------------------------------------

func (s *Server) handleGetProbeSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.probe.LoadSettings(r.Context())
	if err != nil {
		mapError(s.log, w, err, "probe settings get")
		return
	}
	// Token 不回显，只告知是否已设置
	hasToken := s.probe.HasToken(r.Context())
	st.Token = ""
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": st, "has_token": hasToken,
		"token_header": ProbeTokenHeader,
	})
}

func (s *Server) handlePutProbeSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.probe.LoadSettings(r.Context())
	if err != nil {
		mapError(s.log, w, err, "probe settings load")
		return
	}
	var req probe.Settings
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	// Token 与 Revision 不由请求体决定
	req.Token = cur.Token
	req.Revision = cur.Revision
	if err := s.probe.SaveSettings(r.Context(), req); err != nil {
		mapError(s.log, w, err, "probe settings save")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "probe.settings_update", username, "",
		"enabled="+strconv.FormatBool(req.Enabled), s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRotateProbeToken 轮换独立探针的访问令牌。
func (s *Server) handleRotateProbeToken(w http.ResponseWriter, r *http.Request) {
	cur, err := s.probe.LoadSettings(r.Context())
	if err != nil {
		mapError(s.log, w, err, "probe token load")
		return
	}
	token, err := newCSRFToken() // 复用同一个随机源（24 字节 base64url）
	if err != nil {
		mapError(s.log, w, err, "probe token generate")
		return
	}
	cur.Token = token
	cur.RequireToken = true
	if err := s.probe.SaveSettings(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "probe token save")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "probe.token_rotate", username, "", "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"token":  token,
		"header": ProbeTokenHeader,
		"note":   "明文只显示这一次；外部探针请求时带上该请求头",
	})
}

// upgraderProbe 探针 WS 的升级器（允许跨站，因为探针可能被外部站点引用）。
var upgraderProbe = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 8192,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

var _ = context.Background
