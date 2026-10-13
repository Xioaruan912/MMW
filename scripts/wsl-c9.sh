#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(P7): 订阅格式补齐到 12 种（Surge/Surfboard/Loon/QX/Egern）

新增渲染器
- internal/subscription/params.go：抽出与客户端无关的 nodeParams，
  各渲染器共享同一套解析，避免逐个实现导致行为漂移
- ini.go：Surge / Surfboard / Loon / Quantumult X 四个 INI 家族渲染器
- egern.go：Egern（YAML），复用 mihomo 兼容的代理条目，
  但外层用 policy_groups/policies 而非 proxy-groups/proxies

设计取舍：**不静默降级**
- Surge 不支持 httpupgrade → 明确报错
- Surfboard / Quantumult X 不支持 REALITY → 明确报错
  静默给一份导入即失败的配置，比直接报错难排查得多

UA 分流修正
- Surfboard 原先被错分到 Clash（Surfboard 是 INI 格式，不是 Clash YAML）
- Shadowrocket 单独成格式（走明文 URI）
- 新增 Egern；Stash 仍走 Clash（确实兼容）

配套
- GET /sub/formats：公开列出全部格式与中文标签
- 前端订阅对话框改为从该接口拉取，不再硬编码四种

测试
- 新增 8 组单测：四种 INI 渲染的字段断言、不兼容组合必须报错、
  全格式可渲染（用全客户端都支持的 TLS 节点）、
  **REALITY 支持矩阵**（支持组必须成功 / 不支持组必须报错）
- e2e 新增 12 项（103/103 通过）
- 修复 e2e 自身两处问题：
  1) 请求了不存在的 /api/sub/formats（SPA 回落返回 index.html 也给了 200）
  2) 含中文的 JSON 经 jget 管道在 ASCII locale 下会静默丢数据，改用 grep 计数"
git --no-pager log --oneline | head -2
