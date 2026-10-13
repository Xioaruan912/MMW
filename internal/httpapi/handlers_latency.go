package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/storage"
)

// handleNodeLatency 返回某台服务器上全部节点的最新探测结果。
func (s *Server) handleNodeLatency(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.LatestNodeLatency(r.Context(), r.URL.Query().Get("server_id"))
	if err != nil {
		mapError(s.log, w, err, "node latency")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"latency": m, "total": len(m)})
}

// handleNodeLatencySeries 返回单个节点的探测历史。
func (s *Server) handleNodeLatencySeries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	series, err := s.store.NodeLatencySeries(r.Context(), id, 200)
	if err != nil {
		mapError(s.log, w, err, "node latency series")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": id, "points": series})
}

// handleTriggerProbe 立即对某台服务器触发一轮探测。
func (s *Server) handleTriggerProbe(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	if s.prober == nil {
		writeErr(w, http.StatusServiceUnavailable, "prober_disabled", "探测功能未启用")
		return
	}
	_, username, _ := UserFrom(r.Context())
	res, err := s.prober.ProbeServer(r.Context(), serverID, username)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "probe_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// handleTriggerProbeAll 对所有在线服务器触发一轮探测。
func (s *Server) handleTriggerProbeAll(w http.ResponseWriter, r *http.Request) {
	if s.prober == nil {
		writeErr(w, http.StatusServiceUnavailable, "prober_disabled", "探测功能未启用")
		return
	}
	go s.prober.ProbeAll(r.Context())
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":   true,
		"note": "已在后台对所有在线服务器发起探测，稍后刷新查看结果",
	})
}

var _ = storage.NodeLatency{}
