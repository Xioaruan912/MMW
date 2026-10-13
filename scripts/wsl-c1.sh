#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "fix(security): 登录防爆破 / CSRF / 可信客户端 IP

安全欠债清偿：
- 新增 internal/httpapi/ratelimit.go：登录限流，按 IP 与用户名双维度计数，
  5 次失败锁 15 分钟并指数退避（上限 24h），命中返回 429 + Retry-After
- 新增 internal/httpapi/csrf.go：double-submit cookie 方案，
  会话签发时下发非 HttpOnly 的 mmx_csrf，状态变更请求必须回填 X-CSRF-Token
- 新增 internal/reqinfo：只有 MMX_TRUST_PROXY=1 时才采信 X-Forwarded-For/X-Real-IP，
  避免伪造来源 IP 绕过限流与 Agent 令牌独占判定；Hub 同步接入
- 修复 serveSPA 中无保护的 f.(io.ReadSeeker) 断言（embed 实现变更即 panic），
  改为带回落的安全实现并支持 Range/If-Modified-Since
- 前端 request() 自动读取 mmx_csrf 并回填请求头

e2e 新增断言组（27/27 通过）：
- CSRF 令牌下发 + 缺少 CSRF 头时 403
- 连续错误口令触发 429 + 锁定期内正确口令同样被拒
- 审计新增 auth.login.locked 事件"
git --no-pager log --oneline | head -2
