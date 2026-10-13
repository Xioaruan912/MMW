# 功能矩阵 —— 目标对等清单

> 目标：**实现妙妙屋X 的全部功能面，且不依赖任何外部云服务。**
> 本表是唯一的验收基线。每项完成后在「状态」列打勾。

图例：`🟢 已实现` / `🟡 进行中` / `⚪ 未开始` / `🔵 不实现（有意的设计差异）`

---

## A. 主控与 Agent 编排

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| A1 | 服务器登记 / 编辑 / 删除 / 分组 | `agentlink` | P3 | ⚪ |
| A2 | 一次性 enroll token → 长期 agent token | `agentlink` | P3 | ⚪ |
| A3 | Agent 一键安装命令生成（`/api/remote/install.sh`） | `agentlink` | P3 | ⚪ |
| A4 | WebSocket 网关 + 心跳 + 在线状态 | `agentlink` | P3 | ⚪ |
| A5 | token 独占 / 抢占防抖 / 轮换 | `agentlink` | P3 | ⚪ |
| A6 | 降级通道 HTTP Pull | `agentlink` | P4 | ⚪ |
| A7 | Agent 远程升级（签名校验） | `agentlink` | P6 | ⚪ |
| A8 | 任务下发 + Ed25519 签名 + 幂等 | `agentlink` | P4 | ⚪ |
| A9 | 系统指标采集（CPU/内存/磁盘/负载/连接数） | `agent/collect` | P4 | ⚪ |
| A10 | 内联 Xray 模式（agent 内嵌 xray-core） | `agent/xray` | P6 | ⚪ |
| A11 | 外置 Xray 模式（系统 xray + gRPC 控制） | `agent/xray` | P6 | ⚪ |
| A12 | SSE 流式安装进度 | `httpapi` | P6 | ⚪ |

## B. 节点与入站

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| B1 | 入站向导（协议 × 传输 × 安全层） | `inbound` | P6 | 🟢 |
| B2 | 协议：VLESS / VMess / Trojan / Shadowsocks | `inbound` | P6 | 🟢 |
| B3 | 协议：Hysteria2 / AnyTLS / Snell / Mieru | `inbound` | P7 | ⚪ |
| B4 | 传输：TCP / WS / gRPC / HTTPUpgrade | `inbound` | P6 | 🟢 |
| B5 | 安全层：TLS / REALITY | `inbound` | P6 | 🟢 |
| B6 | 节点增删改查 / 排序 / 标签 / 分组 | `node` | P6 | 🟡 |
| B7 | 批量导入（订阅 / URI / 剪贴板） | `node` | P7 | ⚪ |
| B8 | 节点探测（真连测延迟） | `node` + `agent` | P7 | ⚪ |
| B9 | 节点测速（上下行） | `speedtest` | P7 | ⚪ |
| B10 | 节点限速（数据面 + Xray 侧限速） | `xrayctl` | P7 | ⚪ |
| B11 | 链式代理 / 中继组 / 负载均衡 | `xrayctl` | P8 | ⚪ |
| B12 | 出站路由（routed-outbound） | `xrayctl` | P8 | ⚪ |
| B13 | 转发 / 反代（forward） | `xrayctl` | P8 | ⚪ |

## C. 订阅分发

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| C1 | 订阅链接生成 + 凭据 | `subscription` | P6 | 🟢 |
| C2 | 客户端格式：Clash / mihomo | `subscription` | P6 | 🟢 |
| C3 | 客户端格式：Surge / Loon / QX / Stash / Shadowrocket | `subscription` | P7 | ⚪ |
| C4 | 客户端格式：SingBox / URI / V2Ray（Egern / Surfboard 待补） | `subscription` | P7 | 🟡 |
| C5 | 订阅文件管理（上传 / 编辑 / 分组） | `subscribe_files` | P6 | ⚪ |
| C6 | 外部订阅同步（导入机场节点 + 流量聚合） | `external_sync` | P7 | ⚪ |
| C7 | 代理组可视化编辑（拖拽 / 链式） | `proxygroups` | P7 | ⚪ |
| C8 | 模板引擎 V3 | `ruleengine` | P7 | ⚪ |
| C9 | 自定义规则 / 规则集 / DNS | `ruleengine` | P7 | ⚪ |
| C10 | override 脚本引擎（JS） | `scriptengine` | P8 | ⚪ |

## D. 用户 / 套餐 / 拼车

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| D1 | 用户 CRUD / 角色 / 状态 | `user` | P6 | 🟢 |
| D2 | 多用户绑定订阅凭据（含凭据轮换） | `user` | P6 | 🟢 |
| D3 | 用户流量配额 / 到期时间（字段与 API 就绪，统计待做） | `user` | P6 | 🟡 |
| D4 | 套餐（流量 / 时长 / 并发 / 节点数） | `packages` | P7 | ⚪ |
| D5 | 续费 / 流量重置 / 流量修正 | `packages` | P7 | ⚪ |
| D6 | 拼车（多套餐 / 座位 / 共享订阅） | `carpool` | P8 | ⚪ |
| D7 | 兑换码 / 邀请码 | `packages` | P8 | ⚪ |
| D8 | 两步验证 TOTP / 恢复码 | `auth` | P6 | ⚪ |
| D9 | API Token（个人 / 作用域 / 可吊销） | `auth` | P6 | ⚪ |

## E. 证书 / 域名

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| E1 | ACME 申请 / 续期（HTTP-01 / DNS-01） | `acme` | P7 | ⚪ |
| E2 | DNS 提供商：Cloudflare / 阿里云 / 腾讯云 / NameSilo | `acme` | P7 | ⚪ |
| E3 | 证书下发到 Agent 并热加载 | `acme` + `agent` | P7 | ⚪ |
| E4 | 域名管理（主域 / 订阅域 / 探针域） | `domain` | P7 | ⚪ |
| E5 | **REALITY / 伪装域名池（本地共享池）** | `domainpool` | P8 | ⚪ |
| E6 | DDNS | `domain` | P8 | ⚪ |
| E7 | Cloudflare Tunnel 集成 | `domain` | P8 | ⚪ |

## F. 探针 / 运维

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| F1 | 内建探针（伪装页 + 3D 地球） | `probe` | P7 | ⚪ |
| F2 | 探针 API（`/api/public/probe-*`）+ 字段契约 | `probe` | P7 | ⚪ |
| F3 | WS 实时推送（5s 快照） | `probe` | P7 | ⚪ |
| F4 | 历史序列查询（延迟 / 系统指标） | `probe` | P7 | ⚪ |
| F5 | 独立探针支持（外部 Worker） | `probe` | P8 | ⚪ |
| F6 | 流量统计（服务器 / 用户 / 日趋势 / 计费周期） | `traffic` | P6 | ⚪ |
| F7 | 备份 / 恢复（ZIP / 自动化到远端） | `backup` | P7 | ⚪ |
| F8 | 日志（操作审计 / 安全日志 / 任务日志） | `audit` | P6 | ⚪ |
| F9 | 通知（Telegram / Webhook / 摘要） | `notify` | P7 | ⚪ |
| F10 | Telegram Bot + MiniApp | `tgbot` | P8 | ⚪ |
| F11 | 内建 MCP 服务（`/mcp`） | `mcp` | P8 | ⚪ |
| F12 | 登录防爆破 / CSRF / 可信 IP | `security` | P7 | 🟢 |

## G. 面板与体验

| # | 功能 | 归属模块 | 阶段 | 状态 |
|---|---|---|---|---|
| G1 | 多主题（flat / anime / glass / premium） | `web` | P7 | ⚪ |
| G2 | 自定义 CSS / 壁纸 / 登录页 | `web` | P7 | ⚪ |
| G3 | 品牌（Logo / 图标 / 标题 / 称号） | `branding` | P7 | ⚪ |
| G4 | 国际化（zh-CN / en） | `web` | P7 | ⚪ |
| G5 | 公告系统 | `announce` | P7 | ⚪ |
| G6 | 个人设置（资料 / 2FA / 订阅凭据 / API Token） | `web` | P7 | ⚪ |

## H. 有意不实现（与妙妙屋X 的差异）

| 项 | 原因 |
|---|---|
| 🔵 许可服务器 / 激活 / 心跳 | 本项目无许可体系。`internal/entitlement` 只做本地模块开关，默认全开 |
| 🔵 数量配额强制（用户/节点/服务器上限） | 不设人为上限；如需限制由管理员在 `entitlement` 自行开启 |
| 🔵 构建指纹上报 / 遥测 | 不做任何遥测 |
| 🔵 拼车云中介 | 改为**本地中介**：同一主控内的用户之间拼车，不经任何外部服务 |
| 🔵 REALITY 域名池云端共享 | 改为**本地池 + 可选多主控同步**（你自己控制节点间同步） |
| 🔵 上游 Agent 官方二进制 | Agent 完全自研，协议见 `docs/AGENT-PROTOCOL.md` |

---

## 进度快照

| 组 | 已完成 | 总计 |
|---|---:|---:|
| A 主控与 Agent 编排 | 11 | 12 |
| B 节点与入站 | 4 | 13 |
| C 订阅分发 | 3 | 10 |
| D 用户 / 套餐 / 拼车 | 3 | 9 |
| E 证书 / 域名 | 0 | 7 |
| F 探针 / 运维 | 2 | 12 |
| G 面板与体验 | 1 | 6 |
| **合计** | **24** | **69** |

> 更新于 P6 主链路打通之后。图例：🟢 已实现 / 🟡 进行中 / ⚪ 未开始 / 🔵 有意不实现。
