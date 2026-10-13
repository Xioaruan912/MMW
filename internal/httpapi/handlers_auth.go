package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mmx-project/mmx/internal/auth"
	"github.com/mmx-project/mmx/internal/storage"
)

// ---------------------------------------------------------------------------
// 首启初始化
// ---------------------------------------------------------------------------

// handleSetupStatus 报告系统是否已完成初始化。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		mapError(s.log, w, err, "setup/status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_init": n == 0,
		"version":    s.version,
	})
}

type setupInitReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
}

// handleSetupInit 创建首个管理员账号。仅当系统中没有任何用户时可用。
func (s *Server) handleSetupInit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	n, err := s.store.CountUsers(ctx)
	if err != nil {
		mapError(s.log, w, err, "setup/init count")
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, "already_initialized", "系统已初始化，请直接登录")
		return
	}

	var req setupInitReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败: "+err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "用户名不能为空")
		return
	}
	if len(req.Password) < auth.MinPasswordLength {
		writeErr(w, http.StatusBadRequest, "bad_request",
			"口令至少 "+itoa(auth.MinPasswordLength)+" 位")
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
	u, err := s.store.CreateUser(ctx, req.Username, hash, nick, req.Email, storage.RoleAdmin)
	if err != nil {
		mapError(s.log, w, err, "setup/init create")
		return
	}
	s.store.Audit(ctx, "setup.init", u.Username, u.ID, "初始化管理员", s.clientIP(r))

	// 初始化成功直接签发会话，省去一次登录
	if err := s.issueSession(w, r, u); err != nil {
		mapError(s.log, w, err, "setup/init session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

// ---------------------------------------------------------------------------
// 登录 / 登出
// ---------------------------------------------------------------------------

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin 校验口令并签发会话 cookie。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)

	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	req.Username = strings.TrimSpace(req.Username)

	// 防爆破：先看是否已被锁定
	if ok, remain := s.limiter.Check(ip, req.Username); !ok {
		s.store.Audit(ctx, storage.AuditLoginLocked, req.Username, "",
			fmt.Sprintf("登录被锁定，剩余 %s", remain.Round(time.Second)), ip)
		w.Header().Set("Retry-After", itoa(int(remain.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts",
			fmt.Sprintf("尝试过于频繁，请 %s 后再试", remain.Round(time.Second)))
		return
	}

	u, err := s.store.GetUserByUsername(ctx, req.Username)
	if err != nil || !auth.VerifyPassword(u.PasswordHash, req.Password) {
		// 不区分"用户不存在"与"口令错误"，避免账号枚举
		s.limiter.Fail(ip, req.Username)
		s.store.Audit(ctx, storage.AuditLoginFail, req.Username, "", "登录失败", ip)
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	if u.Status != storage.StatusOn {
		writeErr(w, http.StatusForbidden, "disabled", "账号已被禁用")
		return
	}
	if err := s.issueSession(w, r, u); err != nil {
		mapError(s.log, w, err, "login session")
		return
	}
	s.limiter.Success(ip, req.Username)
	s.store.Audit(ctx, storage.AuditLoginOK, u.Username, u.ID, "", ip)
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

// handleLogout 删除会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	setCSRFCookie(w, "", strings.HasPrefix(s.cfg.BaseURL(), "https://"), -1)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMe 返回当前登录用户。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, _, _ := UserFrom(r.Context())
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "me")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u storage.User) error {
	token, err := auth.GenerateToken()
	if err != nil {
		return err
	}
	if err := s.store.CreateSession(r.Context(), auth.HashToken(token), u.ID,
		r.UserAgent(), s.clientIP(r), s.cfg.SessionTTL); err != nil {
		return err
	}
	secure := strings.HasPrefix(s.cfg.BaseURL(), "https://")
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		Expires:  time.Now().Add(s.cfg.SessionTTL),
	})

	// 同时下发 CSRF 令牌（double-submit cookie，前端需读取并回填请求头）
	csrf, err := newCSRFToken()
	if err != nil {
		return err
	}
	setCSRFCookie(w, csrf, secure, int(s.cfg.SessionTTL.Seconds()))
	return nil
}

func publicUser(u storage.User) map[string]any {
	return map[string]any{
		"id":            u.ID,
		"username":      u.Username,
		"nickname":      u.Nickname,
		"email":         u.Email,
		"role":          u.Role,
		"status":        u.Status,
		"sub_token":     u.SubToken,
		"traffic_limit": u.TrafficLimit,
		"traffic_used":  u.TrafficUsed,
		"expire_at":     u.ExpireAt,
		"created_at":    u.CreatedAt,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

var _ = errors.Is
