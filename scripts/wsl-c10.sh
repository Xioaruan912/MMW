#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(P7): 探针（伪装状态页）+ 通用设置存储

基础设施
- 迁移 v6：通用 system_settings 键值表（品牌/探针/通知等共用，避免每加功能加一张表）
- 迁移 v6：servers 增加展示字段（地区/城市/服务商/电信PaidPeer/流量上限/计费起始日/
  是否在探针展示/权重）
- storage：GetSetting/SetSetting/GetJSONSetting/SetJSONSetting 等
- storage：ServerTrafficRange / UserTrafficRange（按计费周期区间汇总，
  与自然月不同 —— 周期起点由用户配置）

探针（internal/probe）
- Settings：开关、标题、Logo、主题、是否显示服务器名、是否显示地球、
  是否要求令牌、刷新间隔
- Snapshot：字段名与官方探针 API 文档对齐，便于外部探针复用
- 默认全部保守：**默认关闭**（对外暴露面）、**默认不显示服务器名**（最小信息披露）
- show_name=false 时按地区回退命名，而不是「服务器 1/2/3」这种可枚举名
- PeriodRange：按每月起始日计算计费周期，含跨年与非法值夹取（1-28）

接口
- GET  /api/public/probe-servers（关闭时统一 404，不暴露存在性）
- GET  /api/public/probe-ws（按 RefreshSeconds 推送）
- GET  /api/public/probe-series?kind=system|traffic|latency
- GET/PUT /api/admin/probe/settings、POST /api/admin/probe/token
- PATCH /api/admin/servers/{id}/display

修复的真实 bug
- Settings.Token 标成 json:\"-\" 导致令牌**完全不会被持久化**，
  轮换后连自己也用不了。改为独立键 probe.token 存储。

设计取舍
- kind=latency 明确返回 501 并说明原因，而不是返回空数组 ——
  空数组会让前端以为「探测了但没数据」，排障方向完全错

测试
- 单测 6 组：默认值保守性、计费周期四种边界（月中/月初/跨年/起始当日）、非法起始日夹取
- e2e 新增 13 项（116/116 通过）"
git --no-pager log --oneline | head -3
