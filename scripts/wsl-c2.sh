#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(P6): 入站矩阵 + Xray 配置生成下发 + 订阅输出（主链路打通）

数据层（迁移 v2）
- inbounds / inbound_clients / nodes / traffic_daily + users 增加
  sub_token / traffic_limit / traffic_used / expire_at

协议矩阵（internal/inbound）
- VLESS / VMess / Trojan / Shadowsocks × TCP / WS / gRPC / HTTPUpgrade
  × none / TLS / REALITY
- Validate 做组合校验（REALITY 仅 TCP 且必须有 dest/私钥/serverNames；TLS 必须有 SNI）
- RenderXrayInbound 渲染前再次校验，拒绝脏数据

配置生成（internal/xrayctl）
- Build 产出 log/api/stats/policy/inbounds/outbounds/routing/dns
- rev 由规范化 JSON 派生：相同期望状态必得相同 rev（幂等下发的基础）
- Apply 通过签名任务下发并同步等回执，而非裸推配置

订阅输出（internal/subscription）
- URI: vless:// vmess:// trojan:// ss://，含 REALITY 公钥派生（x25519）
- Clash/mihomo YAML、sing-box JSON
- UA 分流语义正确：Clash 家族 → Clash；Shadowrocket/v2rayN → base64 URI
- Surge / Loon / QuantumultX 识别为私有格式并明确报错，不静默降级

HTTP API
- 入站 CRUD + 凭据签发 + 节点展示管理
- /servers/{id}/xray-preview 与 /servers/{id}/apply
- 用户 CRUD + 订阅凭据轮换 + 公开订阅出口 /sub/{token}

测试
- 单测 41 项：入站矩阵与渲染、rev 稳定性与错误路径、三种订阅格式
- e2e 44 项全绿"
git --no-pager log --oneline | head -2
