package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleTrafficSummary 汇总流量。
//
// 不传 server_id 时返回全部服务器按日合并的曲线；某台服务器离线也能看历史。
func (s *Server) handleTrafficSummary(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	serverID := r.URL.Query().Get("server_id")

	var (
		sum interface{}
		err error
	)
	if serverID != "" {
		sum, err = s.store.ServerTrafficSummary(r.Context(), serverID, days)
	} else {
		sum, err = s.store.AllServersTrafficSummary(r.Context(), days)
	}
	if err != nil {
		mapError(s.log, w, err, "traffic summary")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "server_id": serverID, "summary": sum,
		"generated_at": time.Now().UnixMilli(),
	})
}

// handleUserTraffic 返回某个用户的流量汇总与配额状态。
func (s *Server) handleUserTraffic(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	sum, err := s.store.UserTrafficSummary(r.Context(), id, days)
	if err != nil {
		mapError(s.log, w, err, "user traffic")
		return
	}
	quota, err := s.store.GetUserQuota(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "user quota")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum, "quota": quota})
}

// handleMyTraffic 返回当前登录用户的流量与配额。
func (s *Server) handleMyTraffic(w http.ResponseWriter, r *http.Request) {
	id, _, _ := UserFrom(r.Context())
	sum, err := s.store.UserTrafficSummary(r.Context(), id, 30)
	if err != nil {
		mapError(s.log, w, err, "my traffic")
		return
	}
	quota, err := s.store.GetUserQuota(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "my quota")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum, "quota": quota})
}
