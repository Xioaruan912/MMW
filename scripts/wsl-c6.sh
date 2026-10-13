#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat: TOTP 双因子 + 接口令牌（API Token）

双因子（TOTP）
- internal/auth/totp.go：密钥生成、动态码校验（±1 时间窗容忍客户端时钟漂移）、
  一次性恢复码（8 个，大写分组便于抄写，落库只存 sha256）
- 迁移 v4：users 增加 totp_enabled / totp_pending_secret / recovery_codes
- 两步启用：setup 生成密钥 -> 用户扫码后输入验证码 -> enable 才真正生效
- 登录流程强制第二因子；未通过返回 401 code=totp_required 供前端提示
- 恢复码命中即作废，并扣减剩余计数

接口令牌
- 迁移 v4：api_tokens 表（可设 TTL、记录 last_used、可单独吊销）
- mwRequireAuth 支持 Authorization: Bearer mmx_xxx，与会话 cookie 并存
- 令牌鉴权自动豁免 CSRF（无 cookie 即无跨站自动携带问题）
- 令牌权限等同所属用户，越权仍由角色中间件拦截

修复的真实 bug
- verifySecondFactor 原先从请求上下文取用户 ID。登录流程此时尚未建立会话，
  上下文里没有用户，导致恢复码的消费写到空 ID 上 —— 恢复码可被无限次重复使用。
  改为显式传入 userID。

测试
- 单测 6 组：密钥格式、动态码接受/拒绝、恢复码唯一性与不可逆存储、消费语义、令牌格式
- e2e 新增 25 项（85/85 通过）：完整双因子生命周期、恢复码一次性、
  登录三态（无码/错码/对码）、令牌创建/使用/越权/吊销
- e2e 结构调整：防爆破断言移到末尾（IP 级锁定会污染其它登录用例，这是正确的产品行为）"
git --no-pager log --oneline | head -2
