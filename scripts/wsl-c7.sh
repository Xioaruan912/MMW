#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat: 流量统计（服务器日表 + 每用户归集 + 配额判定）

修复的真实缺陷
- 生成的 Xray 配置里有 api/stats/policy 段，但**没有 API inbound**（dokodemo-door）。
  缺少它时 stats 与在线人数根本查不到 —— 统计功能形同虚设。
  现在补上仅监听 127.0.0.1 的 api 入站，并把 APITag/APIPort 变成可测常量。

流量归集（internal/traffic）
- 服务器总量：复用 metrics 里的 traffic 增量，落 traffic_daily
- 每用户用量：新增任务 xray.query_stats，Agent 调 `xray api statsquery --reset`
  拿 email 维度的累计计数（走官方 CLI，不把 xray-core 的 protobuf 依赖拖进 Agent）
- ParseStatsOutput 同时兼容文本与 JSON 输出，纯函数可单测
- email -> user_id 映射取自入站客户端；未匹配的 email 计数上报但不落库
- 写入用户日表与 users.traffic_used 同事务，避免两者对不上

数据层
- 迁移 v5：user_traffic_daily(day,user_id,up,down)
- AddServerTraffic / AddUserTraffic / {Server,User,AllServers}TrafficSummary / GetUserQuota
- 配额语义：limit=0 视为不限量；用满即 exhausted；过期单独标记

接口
- GET /api/admin/traffic?server_id=&days=
- GET /api/admin/users/{id}/traffic（汇总 + 配额）
- GET /api/me/traffic

测试
- storage 集成测试（真 SQLite + 真迁移）：迁移幂等、全部表可用、
  日表累加、用户总额与日表一致性、5 种配额场景、多服务器按日合并
- traffic 解析测试：文本/JSON 双格式、重复项累加、int64 边界、垃圾输入
- xrayctl：API 入站存在性、只监听回环、APIPort 解析、关闭 API 时不生成
- e2e 新增 6 项（91/91 通过）：汇总结构、单机汇总、配额三态"
git --no-pager log --oneline | head -2
