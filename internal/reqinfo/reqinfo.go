// Package reqinfo 负责从 HTTP 请求中解析可信的客户端信息。
//
// 关键点：只有当部署者显式声明"我在反向代理之后"（MMX_TRUST_PROXY=1）时，
// 才采信 X-Forwarded-For / X-Real-IP。否则任何人都能伪造来源 IP，
// 从而绕过登录限流、Agent 令牌独占等基于 IP 的判定。
package reqinfo

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP 返回可信的客户端 IP。
func ClientIP(r *http.Request, trustProxy bool) string {
	if r == nil {
		return ""
	}
	if trustProxy {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			// 取最左侧（最初发起方）
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			if ip := strings.TrimSpace(v); ip != "" {
				return ip
			}
		}
		if v := strings.TrimSpace(r.Header.Get("X-Real-Ip")); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// IsPrivate 判断 IP 是否属于私有/回环网段（用于探针与 SSRF 白名单判断）。
func IsPrivate(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast()
}
