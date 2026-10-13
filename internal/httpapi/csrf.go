package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/mmx-project/mmx/internal/auth"
)

// CSRF 采用 **double-submit cookie** 方案：
//
//	签发会话时同时下发一个非 HttpOnly 的 mmx_csrf cookie；
//	前端把它的值放进 X-CSRF-Token 请求头；
//	服务端比对「cookie 值」与「请求头值」是否一致。
//
// 跨站攻击者能让浏览器带上 cookie，但读不到 cookie 的值，因此无法伪造请求头。
// 这与 SameSite=Lax 形成两层防护（Lax 在部分老浏览器上行为不一致）。
const (
	CSRFCookie = "mmx_csrf"
	CSRFHeader = "X-CSRF-Token"
)

func newCSRFToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// mwCSRF 校验状态变更请求的 CSRF 令牌。只挂在**基于 cookie 鉴权**的路由组上。
func (s *Server) mwCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(CSRFCookie)
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusForbidden, "csrf_missing", "缺少 CSRF 令牌，请刷新页面后重试")
			return
		}
		got := strings.TrimSpace(r.Header.Get(CSRFHeader))
		if got == "" || !auth.ConstantTimeEqualString(got, c.Value) {
			writeErr(w, http.StatusForbidden, "csrf_invalid", "CSRF 令牌不匹配")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setCSRFCookie 下发 CSRF cookie（非 HttpOnly，前端需要读取）。
func setCSRFCookie(w http.ResponseWriter, token string, secure bool, exp int) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   exp,
	})
}
