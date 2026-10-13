#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(web): 入站 / 节点 / 用户与订阅 三个控制台页面

- 节点与入站：按服务器分组、入站表格（协议/传输/安全层 chip）、
  一键下发配置并展示 rev 与 Agent 回执输出
- 新增入站对话框：协议 × 传输 × 安全层矩阵联动
  （REALITY 自动锁定 TCP、按选择动态显示 WS/gRPC/TLS/REALITY/SS 专属字段）
- 节点列表：入站与服务器信息合并展示、节点改名
- 用户与订阅：用户 CRUD、订阅凭据重置；
  对话框三个页签 —— 订阅链接（Clash/URI/base64/SingBox 四种，可复制）、
  已授权节点（列出该用户的 UUID 并可逐条吊销）、签发凭据（按入站批量授权）
- api.ts 补齐入站 / 节点 / 用户 / Xray 全部接口与类型"
git --no-pager log --oneline | head -2
