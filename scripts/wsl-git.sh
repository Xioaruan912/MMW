#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
if [ ! -d .git ]; then
  git init -q -b main
  git config user.name  "MMX Builder"
  git config user.email "builder@mmx.local"
fi
git add -A
git -c core.autocrlf=false commit -q -m "feat: MMX 骨架与 Master-Agent 编排打通（M2）

规划
- docs/ARCHITECTURE.md   组件拓扑、模块职责、数据模型、部署形态
- docs/AGENT-PROTOCOL.md 线协议规格（信封/载荷/任务白名单/连接生命周期）
- docs/FEATURE-MATRIX.md 对标妙妙屋X 的功能验收基线（A-G 组）
- docs/ROADMAP.md        P0-P9 阶段计划与里程碑判据

工程骨架（P1）
- go.mod / Makefile / .gitignore / LICENSE(MIT) / README
- internal/{config,logging,storage,idgen,version,auth}
- storage 采用只追加迁移列表，SQLite 走 modernc（纯 Go，无 cgo）

共享协议（P2）
- pkg/agentproto: Envelope / 全部载荷 / 任务白名单 / Ed25519 签名
- 11 项单元测试，含「args 摘要经 JSON 往返不漂移」回归

主控编排（P3）
- internal/agentlink: registry（服务器+令牌三级模型）/ hub（连接注册表+任务派发）
  / ws（握手+IP 独占）/ installer（一键安装脚本）/ sweeper（离线判定）
- internal/httpapi: chi 路由 + 请求ID/恢复/日志/鉴权中间件 + 全部 handler

Agent 运行时（P4）
- internal/agent: config（YAML+env）/ link（出站 WSS+退避重连+心跳+任务执行）
  / collect（纯 /proc 采集，无 cgo）/ xray（external 模式生命周期）
- cmd/mmx-agent 单二进制

端到端（P5）
- scripts/e2e.sh：首启初始化 -> 建服务器 -> 签发令牌 -> 安装脚本 -> enroll
  -> WebSocket 握手 -> 指标入库 -> 任务下发 -> 非白名单任务被拒 -> 审计落库
- 结果：19/19 全部通过

关键设计决定
- 任务白名单是编译期常量，不存在 shell.exec 之类任意命令任务
- 任务 Ed25519 签名覆盖 v1|task_id|kind|sha256(args)|nonce
- Agent 只做出站连接，从不监听端口
- 无许可体系、无遥测、无厂商云端耦合（与妙妙屋X 的有意差异，见 README）"
git --no-pager log --oneline | head -3
echo "--- 文件统计 ---"
git ls-files | wc -l
echo "Go 源码行数: $(git ls-files '*.go' | xargs wc -l 2>/dev/null | tail -1)"
