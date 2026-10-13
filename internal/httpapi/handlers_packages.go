package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/notify"
	"github.com/mmx-project/mmx/internal/storage"
)

// ---------------------------------------------------------------------------
// 套餐
// ---------------------------------------------------------------------------

type packageReq struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	TrafficLimit int64  `json:"traffic_limit"`
	DurationDays int    `json:"duration_days"`
	PriceCents   int64  `json:"price_cents"`
	Currency     string `json:"currency"`
	MaxNodes     int    `json:"max_nodes"`
	MaxServers   int    `json:"max_servers"`
	Sort         int    `json:"sort"`
	Enabled      *bool  `json:"enabled"`
}

func (s *Server) handleListPackages(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListPackages(r.Context())
	if err != nil {
		mapError(s.log, w, err, "list packages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": list, "total": len(list)})
}

func (s *Server) handleCreatePackage(w http.ResponseWriter, r *http.Request) {
	var req packageReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "套餐名不能为空")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, err := s.store.CreatePackage(r.Context(), storage.Package{
		Name: req.Name, Description: req.Description,
		TrafficLimit: req.TrafficLimit, DurationDays: req.DurationDays,
		PriceCents: req.PriceCents, Currency: req.Currency,
		MaxNodes: req.MaxNodes, MaxServers: req.MaxServers,
		Sort: req.Sort, Enabled: enabled,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "duplicate", "套餐名已存在")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "package.create", username, p.ID, p.Name, s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"package": p})
}

func (s *Server) handleUpdatePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur, err := s.store.GetPackage(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update package get")
		return
	}
	var req packageReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Name != "" {
		cur.Name = req.Name
	}
	cur.Description = req.Description
	cur.TrafficLimit = req.TrafficLimit
	cur.DurationDays = req.DurationDays
	cur.PriceCents = req.PriceCents
	if req.Currency != "" {
		cur.Currency = req.Currency
	}
	cur.MaxNodes = req.MaxNodes
	cur.MaxServers = req.MaxServers
	cur.Sort = req.Sort
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if err := s.store.UpdatePackage(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "update package")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"package": cur})
}

func (s *Server) handleDeletePackage(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePackage(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete package")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type applyPackageReq struct {
	PackageID     string `json:"package_id"`
	ResetTraffic  bool   `json:"reset_traffic"`
	ExtendFromNow bool   `json:"extend_from_now"`
}

// handleApplyPackage 把套餐绑定到用户（开通/续费）。
func (s *Server) handleApplyPackage(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	var req applyPackageReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.PackageID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少 package_id")
		return
	}
	u, err := s.store.ApplyPackage(r.Context(), userID, req.PackageID, storage.ApplyPackageOptions{
		ResetTraffic:  req.ResetTraffic,
		ExtendFromNow: req.ExtendFromNow,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "apply_failed", err.Error())
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "package.apply", username, userID,
		"package="+req.PackageID, s.clientIP(r))

	// 套餐/续费是典型的需要通知用户的事件
	s.notifier.Notify(r.Context(), notifyEventUser("套餐已开通",
		u.Username+" 的套餐已更新为 "+req.PackageID, u.ID))

	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

// ---------------------------------------------------------------------------
// 兑换码
// ---------------------------------------------------------------------------

type redeemCodeReq struct {
	Code         string `json:"code"`
	PackageID    string `json:"package_id"`
	TrafficBytes int64  `json:"traffic_bytes"`
	DurationDays int    `json:"duration_days"`
	MaxUses      int    `json:"max_uses"`
	ExpiresAt    int64  `json:"expires_at"`
	Count        int    `json:"count"` // 批量生成数量（默认 1）
}

func (s *Server) handleListRedeemCodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRedeemCodes(r.Context(), 200)
	if err != nil {
		mapError(s.log, w, err, "list redeem codes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": list, "total": len(list)})
}

// handleCreateRedeemCode 生成兑换码（支持批量）。
func (s *Server) handleCreateRedeemCode(w http.ResponseWriter, r *http.Request) {
	var req redeemCodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}
	if count > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request", "单次最多生成 200 张")
		return
	}
	if req.PackageID != "" {
		if _, err := s.store.GetPackage(r.Context(), req.PackageID); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "套餐不存在")
			return
		}
	}
	if req.PackageID == "" && req.TrafficBytes == 0 && req.DurationDays == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "必须指定套餐，或至少给出流量/时长")
		return
	}

	out := make([]storage.RedeemCode, 0, count)
	for i := 0; i < count; i++ {
		c, err := s.store.CreateRedeemCode(r.Context(), storage.RedeemCode{
			Code: req.Code, PackageID: req.PackageID, TrafficBytes: req.TrafficBytes,
			DurationDays: req.DurationDays, MaxUses: req.MaxUses, ExpiresAt: req.ExpiresAt,
		})
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeErr(w, http.StatusConflict, "duplicate", "兑换码已存在")
				return
			}
			mapError(s.log, w, err, "create redeem code")
			return
		}
		out = append(out, c)
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "redeem.create", username, "",
		"count="+itoa(len(out))+" package="+req.PackageID, s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"codes": out, "total": len(out)})
}

func (s *Server) handleDeleteRedeemCode(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRedeemCode(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete redeem code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMyRedeem 用户自助兑换。
func (s *Server) handleMyRedeem(w http.ResponseWriter, r *http.Request) {
	userID, _, _ := UserFrom(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请输入兑换码")
		return
	}
	u, rec, err := s.store.Redeem(r.Context(), code, userID)
	if err != nil {
		if errors.Is(err, storage.ErrRedeemInvalid) {
			// 统一的失败文案：不区分"不存在"与"已用尽"，避免被枚举
			writeErr(w, http.StatusBadRequest, "invalid_code", "兑换码无效或已用尽")
			return
		}
		mapError(s.log, w, err, "redeem")
		return
	}
	s.store.Audit(r.Context(), "redeem.use", u.Username, rec.ID, code[:min(len(code), 4)]+"****", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"user": publicUser(u),
		"redeemed": map[string]any{
			"traffic_bytes": rec.TrafficBytes,
			"duration_days": rec.DurationDays,
			"package_id":    rec.PackageID,
		},
	})
}

// handleMyPackage 返回当前用户的套餐与配额概览。
func (s *Server) handleMyPackage(w http.ResponseWriter, r *http.Request) {
	userID, _, _ := UserFrom(r.Context())
	u, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		mapError(s.log, w, err, "my package")
		return
	}
	quota, err := s.store.GetUserQuota(r.Context(), userID)
	if err != nil {
		mapError(s.log, w, err, "my package quota")
		return
	}
	resp := map[string]any{"quota": quota}
	if u.PackageID != "" {
		if pkg, err := s.store.GetPackage(r.Context(), u.PackageID); err == nil {
			resp["package"] = pkg
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// notifyEventUser 构造一条与用户相关的事件。
func notifyEventUser(title, message, userID string) notify.Event {
	return notify.Event{
		Kind: notify.EventUserCreated, Level: "info", Title: title,
		Message: message, UserID: userID, Timestamp: time.Now(),
	}
}
