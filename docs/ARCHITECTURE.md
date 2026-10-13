# MMX 架构设计

> **MMX** 是一套自托管的代理节点管理与订阅分发平台。
> Master–Agent 架构，Go + React，SQLite / PostgreSQL 双驱动，**零外部云依赖**。

---

## 一、设计原则

| # | 原则 | 含义 |
|---|---|---|
| 1 | **无外部云** | 不依赖任何厂商服务器。许可、域名池、拼车、证书签发全部本地或自建 |
| 2 | **单向依赖** | Agent 只出站连 Master，Master 从不主动连 Agent。Agent 可部署在 NAT 后 |
| 3 | **协议先行** | Master 与 Agent 共用一个 `pkg/agentproto` 包，双方编译期同源，杜绝协议漂移 |
| 4 | **本地权威** | 权限、配额、节点归属全部由本地数据库裁决；不存在"云端下发才生效"的状态 |
| 5 | **可降级** | WebSocket 不可用时降级到 HTTP 长轮询（Pull），再不可用则纯心跳上报 |
| 6 | **单二进制交付** | Master 与 Agent 各一个静态二进制，前端 embed 进 Master |

---

## 二、组件拓扑

```
                       ┌──────────────────────────────────────────┐
                       │              浏览器 / 客户端              │
                       └────────────────────┬─────────────────────┘
                                            │ HTTPS
                       ┌────────────────────▼─────────────────────┐
                       │            mmx-master（主控）             │
                       │  ┌────────────────────────────────────┐  │
                       │  │ HTTP API   /api/**                 │  │
                       │  │ Agent 网关 /api/agent/**  (WS+HTTP) │  │
                       │  │ 订阅出口   /sub/**  探针 /probe/**   │  │
                       │  │ MCP        /mcp                     │  │
                       │  │ 内嵌前端   /  (embed.FS)             │  │
                       │  └────────────────────────────────────┘  │
                       │  ┌───────────┐ ┌───────────┐ ┌────────┐  │
                       │  │ agentlink │ │  xrayctl  │ │  acme  │  │
                       │  │ ★ 编排核心 │ │ 配置生成  │ │  证书  │  │
                       │  └───────────┘ └───────────┘ └────────┘  │
                       │  ┌────────────────────────────────────┐  │
                       │  │ storage  SQLite | PostgreSQL       │  │
                       │  └────────────────────────────────────┘  │
                       └────────────────────┬─────────────────────┘
                                            │ 出站 WSS（Agent 发起）
        ┌───────────────────────────────────┼───────────────────────────────────┐
        │                                   │                                   │
┌───────▼────────┐                 ┌────────▼───────┐                 ┌────────▼───────┐
│  mmx-agent  #1 │                 │  mmx-agent #2  │                 │  mmx-agent #3  │
│  ├ 指标采集     │                 │                │                 │                │
│  ├ xray 生命周期│                 │        ...     │                 │        ...     │
│  └ 任务执行     │                 │                │                 │                │
│  （内联 Xray）  │                 │                │                 │                │
└───────┬────────┘                 └────────────────┘                 └────────────────┘
        │ 本地 gRPC / 进程管理
┌───────▼────────┐
│  xray-core     │  内联（agent 内嵌）或外置（/usr/local/bin/xray）
└────────────────┘
```

---

## 三、进程与模块职责

### 3.1 `cmd/mmx-master`（主控）

| 模块 | 路径 | 职责 |
|---|---|---|
| `config` | `internal/config` | 环境变量 + 配置文件加载，含数据库、监听、TLS、JWT 密钥 |
| `logging` | `internal/logging` | 结构化日志（slog），文件轮转 |
| `storage` | `internal/storage` | 数据访问层，SQLite / PostgreSQL 双驱动，内建迁移 |
| `auth` | `internal/auth` | 会话令牌、JWT、TOTP 双因子、API Token、登录防爆破 |
| `httpapi` | `internal/httpapi` | 路由与中间件（鉴权、CORS、限流、审计） |
| `agentlink` | `internal/agentlink` | **★ 服务器登记、令牌生命周期、WS 网关、任务下发、心跳入库、一键安装命令生成** |
| `xrayctl` | `internal/xrayctl` | Xray 配置生成（入站/出站/路由/DNS）、配置差异计算与下发 |
| `inbound` | `internal/inbound` | 入站（协议×传输×安全层）模型与校验 |
| `subscription` | `internal/subscription` | 12 种客户端格式产出（Clash/mihomo/Stash/Surge/Loon/QX/SingBox/URI…） |
| `ruleengine` | `internal/ruleengine` | V3 模板引擎、规则集、代理组 |
| `user` | `internal/user` | 用户、角色、订阅凭据、流量配额 |
| `packages` | `internal/packages` | 套餐（流量/时长）、绑定与续费 |
| `acme` | `internal/acme` | ACME 证书申请/续期（lego）+ 部署到 Agent |
| `domainpool` | `internal/domainpool` | REALITY/伪装域名池（自建，本地共享） |
| `carpool` | `internal/carpool` | 拼车（本地中介，不依赖外部服务） |
| `probe` | `internal/probe` | 探针数据聚合与对外 API |
| `mcp` | `internal/mcp` | 内建 MCP 服务（`/mcp`），复用主控鉴权 |
| `notify` | `internal/notify` | Telegram / Webhook 通知 |
| `entitlement` | `internal/entitlement` | **本地能力开关**（取代厂商许可体系；默认全开，可由管理员按需关闭模块） |

> **与妙妙屋X 的关键差异**：本项目没有"许可服务器"。`entitlement` 只是一个本地模块开关表，
> 用于按部署场景裁剪功能，不参与任何鉴权裁决。所有能力默认可用。

### 3.2 `cmd/mmx-agent`（远程 Agent）

| 模块 | 路径 | 职责 |
|---|---|---|
| `link` | `internal/agent/link` | 出站 WSS 连接、重连退避、心跳 |
| `collect` | `internal/agent/collect` | CPU / 内存 / 磁盘 / 网络 / 负载 / 连接数采集 |
| `xray` | `internal/agent/xray` | Xray 生命周期：安装、配置写入、启停、健康检查 |
| `exec` | `internal/agent/exec` | 白名单任务执行（安装 nginx / xray、升级、重启服务） |
| `sysinfo` | `internal/agent/sysinfo` | 系统画像（发行版、内核、架构、时区、公网 IP） |

**安全约束**（写进代码，不是文档）：
- Agent 只接受**白名单任务类型**，不接受任意 shell
- 任务体带 `nonce` + 主控签名（Ed25519），Agent 校验后才执行
- Agent 配置只持有 `server_token`，**不持有任何主控凭据**

---

## 四、协议总览

### 4.1 传输层

| 通道 | 端点 | 方向 | 用途 |
|---|---|---|---|
| WS | `/api/agent/ws` | Agent → Master | 主通道：心跳、指标、任务、配置 |
| HTTP | `/api/agent/enroll` | Agent → Master | 首次注册（用一次性 token 换长期凭据） |
| HTTP | `/api/agent/pull` | Agent → Master | WS 不可用时的降级轮询 |
| HTTP | `/api/remote/install.sh` | 用户 → Master | 生成 Agent 一键安装脚本 |

### 4.2 消息模型

见 `docs/AGENT-PROTOCOL.md`。所有消息走同一个信封：

```go
type Envelope struct {
    ID      string      `json:"id"`       // ULID，幂等键
    Type    string      `json:"type"`     // hello / heartbeat / metrics / task / task_result / config / ack
    SentAt  int64       `json:"sent_at"`  // unix ms
    Nonce   string      `json:"nonce,omitempty"`
    Payload json.RawMessage `json:"payload"`
}
```

---

## 五、数据模型（核心表）

```
users(id, username, password_hash, role, status, package_id, traffic_used, expire_at, ...)
packages(id, name, traffic_limit, duration_days, price, max_nodes, ...)
user_subscriptions(id, user_id, token, short_code, enabled, ...)

servers(id, name, address, port, xray_mode[embedded|external], agent_version,
        status[online|offline|degraded], last_seen_at, region, provider, ...)
agent_tokens(id, server_id, token_hash, expires_at, used_at, revoked_at, one_time)

server_metrics(server_id, ts, cpu, mem_used, mem_total, disk_used, disk_total,
               load1, net_up, net_down, uptime, xray_running)

inbounds(id, server_id, tag, protocol, transport, security, port, settings_json,
         stream_json, enabled, ...)

nodes(id, inbound_id, name, region, sort, tags_json, enabled, ...)
node_clients(id, node_id, user_id, uuid, flow, limit_mbps, ...)

rule_templates(id, owner_id, name, version, content_yaml, ...)
rule_providers(id, name, url, type, path, ...)
subscribe_files(id, name, content, ...)
certificates(id, domain, provider, issued_at, expires_at, cert_pem, key_pem, ...)

carpool_slots(id, owner_user_id, package_id, seats_total, seats_used, ...)
domain_pool(id, domain, source[user|admin], status, added_by, used_count, ...)

audit_logs(id, actor, action, target, detail_json, ip, ts)
```

---

## 六、部署形态

| 形态 | 说明 |
|---|---|
| **单机** | `mmx-master` + SQLite，前端内嵌，一个二进制跑起来 |
| **分离** | `mmx-master` 在主控机，`mmx-agent` 在 N 台节点机，Agent 出站连主控 |
| **Docker** | `docker compose up`，Master 用卷持久化 `data/` |
| **Nginx 反代** | 对外只暴露 443；Master 只监听 `127.0.0.1` |

---

## 七、与妙妙屋X 的对照

| 能力 | 妙妙屋X | MMX |
|---|---|---|
| 多服务器编排 | ✅ 闭源 | ✅ 自研（本仓库） |
| Xray 内联/外置 | ✅ | ✅ 同一思路 |
| 协议矩阵 | ✅ 10 种 | ✅ 目标同等 |
| 订阅格式 | ✅ 12 种 | ✅ 目标同等 |
| 规则模板 V3 | ✅ | ✅ 自研复刻 |
| 探针 / 伪装页 | ✅ | ✅ 自研 |
| 证书 ACME | ✅ | ✅ `go-acme/lego` |
| 套餐 / 拼车 | ✅ | ✅ 自研（本地中介） |
| REALITY 域名池 | ✅ 厂商云 | ✅ **本地池**（可多点同步） |
| TG Bot | ✅ | ✅ 自研 |
| MCP | ✅ | ✅ 自研 |
| **许可服务器** | ✅ 必需 | ❌ **不存在**（本地能力开关，默认全开） |
