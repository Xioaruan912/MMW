#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat: 任务持久化 + Pull 降级通道能力对齐

问题：任务原先只存在于内存 map。主控重启/崩溃会让在途任务静默消失，
且 /api/agent/pull（HTTP 降级通道）根本无从取任务——功能形同虚设。

主控侧
- 迁移 v3：pending_tasks 表（含签名、nonce、deadline、attempts、结果）
- Hub.DispatchTask 改为「先落库 -> 再投递 -> 落结果」，任何返回路径都结算记录
- 离线服务器下发不再报错即丢：任务入队，等 Pull 或重连后取走
- ClaimPendingTasks：原子领取并标记 dispatched，保证幂等不重复下发
- ExpireStaleTasks：sweeper 结算长时间无回执的任务（原先会永久卡在 dispatched）
- GET /api/admin/tasks：任务历史（含超时/失败），不再只看到内存里那几个

Agent 侧
- internal/agent/link/pull.go：完整的 Pull 会话
  WS 连续失败 3 次 -> 切 HTTP 轮询（30s）；连续成功 5 轮 -> 尝试回到 WS
  上报心跳/指标/历史任务结果，取回队列任务并按同一套验签+白名单执行
- 时钟偏差记录与告警（>30s 提示校时）

e2e 新增 8 项断言（60/60 通过）：
任务历史持久化 -> 离线下发返回 502 且入队 -> Pull 取回带签名任务
-> 二次 Pull 为空（幂等）-> 无效令牌 401"
git --no-pager log --oneline | head -2
