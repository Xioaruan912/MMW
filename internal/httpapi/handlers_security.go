package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/auth"
	"github.com/mmx-project/mmx/internal/storage"
)

// ---------------------------------------------------------------------------
// 双因子（TOTP）
// ---------------------------------------------------------------------------

// handleTOTPSetup 生成密钥与扫码 URL（此时尚未启用，需用户输入验证码确认）。
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	id, username, _ := UserFrom(r.Context())
	st, err := s.store.GetTOTPState(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "totp setup state")
		return
	}
	if st.Enabled {
		writeErr(w, http.StatusConflict, "already_enabled", "双因子已启用；如需重置请先关闭")
		return
	}
	secret, url, err := auth.GenerateTOTPSecret(username)
	if err != nil {
		mapError(s.log, w, err, "totp setup generate")
		return
	}
	if err := s.store.SetTOTPPendingSecret(r.Context(), id, secret); err != nil {
		mapError(s.log, w, err, "totp setup persist")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":      secret,
		"otpauth_url": url,
		"note":        "用验证器扫码后输入 6 位验证码以完成启用；未完成前双因子不生效",
	})
}

type totpCodeReq struct {
	Code string `json:"code"`
}

// handleTOTPEnable 校验验证码并正式启用，同时返回一次性恢复码。
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	id, _, _ := UserFrom(r.Context())
	var req totpCodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	st, err := s.store.GetTOTPState(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "totp enable state")
		return
	}
	if st.Enabled {
		writeErr(w, http.StatusConflict, "already_enabled", "双因子已启用")
		return
	}
	if st.PendingSecret == "" {
		writeErr(w, http.StatusBadRequest, "no_pending", "请先调用 /api/me/totp/setup 生成密钥")
		return
	}
	if !auth.VerifyTOTP(st.PendingSecret, req.Code) {
		writeErr(w, http.StatusBadRequest, "invalid_code", "验证码不正确（请确认设备时间准确）")
		return
	}

	plain, hashes, err := auth.GenerateRecoveryCodes(auth.RecoveryCodeNum)
	if err != nil {
		mapError(s.log, w, err, "totp enable recovery")
		return
	}
	raw, _ := json.Marshal(hashes)
	if err := s.store.EnableTOTP(r.Context(), id, st.PendingSecret, string(raw)); err != nil {
		mapError(s.log, w, err, "totp enable persist")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "security.totp_enable", username, id, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"recovery_codes": plain,
		"note":           "恢复码只显示这一次，请立即妥善保存；每个码只能用一次",
	})
}

// handleTOTPDisable 关闭双因子（需当前动态码或恢复码）。
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	id, username, _ := UserFrom(r.Context())
	var req totpCodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	st, err := s.store.GetTOTPState(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "totp disable state")
		return
	}
	if !st.Enabled {
		writeErr(w, http.StatusBadRequest, "not_enabled", "双因子未启用")
		return
	}
	if !s.verifySecondFactor(r.Context(), id, st, req.Code) {
		writeErr(w, http.StatusBadRequest, "invalid_code", "验证码或恢复码不正确")
		return
	}
	if err := s.store.DisableTOTP(r.Context(), id); err != nil {
		mapError(s.log, w, err, "totp disable persist")
		return
	}
	s.store.Audit(r.Context(), "security.totp_disable", username, id, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}

// handleTOTPStatus 返回当前双因子状态。
func (s *Server) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	id, _, _ := UserFrom(r.Context())
	st, err := s.store.GetTOTPState(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "totp status")
		return
	}
	var remaining int
	if st.RecoveryCodes != "" {
		var hashes []string
		if json.Unmarshal([]byte(st.RecoveryCodes), &hashes) == nil {
			remaining = len(hashes)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":             st.Enabled,
		"recovery_codes_left": remaining,
	})
}

// verifySecondFactor 依次尝试动态码与恢复码。
//
// 注意：必须**显式传入 userID**，不能从请求上下文取。
// 登录流程里此时尚未建立会话，上下文里根本没有用户，
// 靠上下文会把恢复码的消费写到空 ID 上，导致恢复码永远用不掉（可重复使用）。
func (s *Server) verifySecondFactor(ctx context.Context, userID string, st storage.TOTPState, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	if len(code) == 6 && auth.VerifyTOTP(st.Secret, code) {
		return true
	}
	if st.RecoveryCodes == "" {
		return false
	}
	var hashes []string
	if err := json.Unmarshal([]byte(st.RecoveryCodes), &hashes); err != nil {
		return false
	}
	remaining, ok := auth.MatchRecoveryCode(hashes, code)
	if !ok {
		return false
	}
	raw, _ := json.Marshal(remaining)
	_ = s.store.ConsumeRecoveryCode(ctx, userID, string(raw))
	return true
}

// ---------------------------------------------------------------------------
// 接口令牌（API Token）
// ---------------------------------------------------------------------------

type createAPITokenReq struct {
	Name string `json:"name"`
	// TTL 秒数；0 或省略表示长期有效
	TTLSeconds int64 `json:"ttl_seconds"`
}

// handleCreateAPIToken 生成接口令牌。明文只返回一次。
func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	id, username, _ := UserFrom(r.Context())
	var req createAPITokenReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "未命名令牌"
	}
	plain, err := auth.GenerateAPIKey()
	if err != nil {
		mapError(s.log, w, err, "api token generate")
		return
	}
	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	t, err := s.store.CreateAPIToken(r.Context(), id, req.Name, auth.HashToken(plain), "", ttl)
	if err != nil {
		mapError(s.log, w, err, "api token create")
		return
	}
	s.store.Audit(r.Context(), "security.api_token_create", username, t.ID, req.Name, s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": plain,
		"meta":  t,
		"note":  "明文只显示这一次；请以 Authorization: Bearer <token> 使用",
	})
}

// handleListAPITokens 列出当前用户的令牌。
func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	id, _, _ := UserFrom(r.Context())
	list, err := s.store.ListAPITokens(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "api token list")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"id": t.ID, "name": t.Name, "created_at": t.CreatedAt,
			"expires_at": t.ExpiresAt, "last_used_at": t.LastUsedAt,
			"revoked": t.RevokedAt != 0,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out, "total": len(out)})
}

// handleRevokeAPIToken 吊销令牌。
func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	id, username, _ := UserFrom(r.Context())
	tokenID := chi.URLParam(r, "id")
	if err := s.store.RevokeAPIToken(r.Context(), id, tokenID); err != nil {
		mapError(s.log, w, err, "api token revoke")
		return
	}
	s.store.Audit(r.Context(), "security.api_token_revoke", username, tokenID, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
