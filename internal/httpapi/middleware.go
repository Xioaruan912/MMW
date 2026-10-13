package httpapi

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/mmx-project/mmx/internal/auth"
	"github.com/mmx-project/mmx/internal/idgen"
	"github.com/mmx-project/mmx/internal/reqinfo"
	"github.com/mmx-project/mmx/internal/storage"
)

// 内部错误哨兵，用于 mapError 映射状态码。
var (
	errBadRequest   = errors.New("bad request")
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

// SessionCookie 是会话 cookie 名。
const SessionCookie = "mmx_session"

type ctxKey string

const (
	ctxUserID   ctxKey = "user_id"
	ctxUsername ctxKey = "username"
	ctxRole     ctxKey = "role"
	ctxRequest  ctxKey = "request_id"
	ctxAuthKind ctxKey = "auth_kind"
)

// 鉴权方式。
const (
	AuthKindSession = "session"
	AuthKindToken   = "token"
)

// AuthKindFrom 返回本次请求的鉴权方式。
func AuthKindFrom(ctx context.Context) string {
	k, _ := ctx.Value(ctxAuthKind).(string)
	return k
}

// bearerToken 从 Authorization 头取 Bearer 令牌。
func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(v, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

// UserFrom 从请求上下文取当前用户 ID / 角色。
func UserFrom(ctx context.Context) (id, username, role string) {
	id, _ = ctx.Value(ctxUserID).(string)
	username, _ = ctx.Value(ctxUsername).(string)
	role, _ = ctx.Value(ctxRole).(string)
	return
}

// ---------------------------------------------------------------------------
// 中间件
// ---------------------------------------------------------------------------

func (s *Server) mwRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = idgen.New("")
		}
		w.Header().Set("X-Request-Id", rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequest, rid)))
	})
}

func (s *Server) mwRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("请求 panic", "path", r.URL.Path, "panic", rec,
					"stack", string(debug.Stack()))
				writeErr(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Hijack 让 WebSocket 升级能在日志中间件之后正常工作。
// 这是必需的：一旦包装了 ResponseWriter 而未透传 Hijacker，
// gorilla/websocket 会报 "response does not implement http.Hijacker"。
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("底层 ResponseWriter 不支持 Hijack")
	}
	return h.Hijack()
}

// Flush 透传，保证 SSE 等流式响应不被缓冲住。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 让 http.ResponseController 能找到原始 ResponseWriter。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) mwAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		// 静态资源与健康检查不刷屏
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "dur", time.Since(start).Round(time.Millisecond).String(),
			"ip", s.clientIP(r))
	})
}

// mwRequireAuth 校验身份。两种方式：
//
//  1. Authorization: Bearer mmx_xxx  —— 接口令牌（程序调用，不走 CSRF）
//  2. mmx_session cookie             —— 浏览器会话（需过 CSRF 中间件）
func (s *Server) mwRequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := bearerToken(r); strings.HasPrefix(tok, "mmx_") {
			s.authByAPIToken(next, w, r, tok)
			return
		}
		s.authBySession(next, w, r)
	})
}

func (s *Server) authByAPIToken(next http.Handler, w http.ResponseWriter, r *http.Request, tok string) {
	ctx := r.Context()
	at, err := s.store.FindAPITokenByHash(ctx, auth.HashToken(tok))
	if err != nil || !at.Valid(time.Now()) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "接口令牌无效或已失效")
		return
	}
	u, err := s.store.GetUser(ctx, at.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "令牌所属账号不存在")
		return
	}
	if u.Status != storage.StatusOn {
		writeErr(w, http.StatusForbidden, "disabled", "账号已被禁用")
		return
	}
	s.store.TouchAPIToken(ctx, at.ID)

	c := context.WithValue(ctx, ctxUserID, u.ID)
	c = context.WithValue(c, ctxUsername, u.Username)
	c = context.WithValue(c, ctxRole, u.Role)
	c = context.WithValue(c, ctxAuthKind, AuthKindToken)
	next.ServeHTTP(w, r.WithContext(c))
}

func (s *Server) authBySession(next http.Handler, w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	u, err := s.store.SessionUser(r.Context(), auth.HashToken(c.Value))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "会话已失效，请重新登录")
		return
	}
	if u.Status != storage.StatusOn {
		writeErr(w, http.StatusForbidden, "disabled", "账号已被禁用")
		return
	}
	ctx := context.WithValue(r.Context(), ctxUserID, u.ID)
	ctx = context.WithValue(ctx, ctxUsername, u.Username)
	ctx = context.WithValue(ctx, ctxRole, u.Role)
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindSession)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// mwRequireAdmin 在 RequireAuth 之后校验管理员角色。
func (s *Server) mwRequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, role := UserFrom(r.Context())
		if role != storage.RoleAdmin {
			writeErr(w, http.StatusForbidden, "forbidden", "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func directClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// clientIP 在信任反代的前提下解析真实客户端 IP。
func (s *Server) clientIP(r *http.Request) string {
	return reqinfo.ClientIP(r, s.cfg.TrustProxy)
}
