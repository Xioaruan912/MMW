# MMX

**自托管的代理节点管理与订阅分发平台。** Master–Agent 架构，Go + React，SQLite / PostgreSQL，**零外部云依赖**。

> MMX 是「妙妙屋X 全部能力 + 完全自主可控」的开源等价实现：
> 功能面对齐，但**没有许可服务器、没有遥测、没有任何厂商云端耦合**。
> 每一个协议都由本仓库自己定义（见 `docs/`），Agent 完全自研。

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)
[![Status](https://img.shields.io/badge/Status-M2%20编排已打通-orange)](docs/ROADMAP.md)

---

## 它是什么

把多台服务器上的代理节点统一管起来，并把订阅链接分发给用户：

| 能力 | 说明 | 状态 |
|---|---|---|
| **服务管理** | Master–Agent 架构，WebSocket 出站连接，一键安装，心跳与指标 | ✅ M2 |
| **系统指标** | CPU / 内存 / 磁盘 / 负载 / 网速 / 连接数，纯 `/proc` 采集，无 cgo | ✅ M2 |
| **任务编排** | 白名单任务 + Ed25519 签名 + 幂等回执（**没有任意 shell**） | ✅ M2 |
| **节点与入站** | 协议 × 传输 × 安全层矩阵，Xray 配置生成与下发 | ⚪ P6 |
| **订阅分发** | Clash / Surge / Loon / QX / SingBox 等 12 种格式 | ⚪ P7 |
| **用户与套餐** | 用户、套餐、流量配额、到期、续费、拼车 | ⚪ P6–P8 |
| **证书 / 域名** | ACME 自动申请续期，DNS 提供商集成，域名池 | ⚪ P7 |
| **探针** | 伪装状态页 + 对外 API + 实时推送 | ⚪ P7 |
| **TG Bot / MCP** | Telegram 机器人与 AI Agent 接入 | ⚪ P8 |

完整清单见 **[docs/FEATURE-MATRIX.md](docs/FEATURE-MATRIX.md)**，开发计划见 **[docs/ROADMAP.md](docs/ROADMAP.md)**。

---

## 快速开始

### 环境要求

- Go 1.24+
- Linux（Master 与 Agent 都支持任意 Linux；Agent 采集依赖 `/proc`）

### 编译

```bash
git clone <repo> mmx && cd mmx
make build          # 产出 bin/mmx-master 与 bin/mmx-agent
make check          # vet + build + test（提交前必跑）
```

### 起一个主控

```bash
mkdir -p /var/lib/mmx
MMX_DATA_DIR=/var/lib/mmx \
MMX_LISTEN=0.0.0.0:12889 \
MMX_PUBLIC_URL=https://panel.example.com \
  ./bin/mmx-master
```

首次访问 `http://<host>:12889/` → 初始化管理员。

### 加一台服务器

1. 面板「服务管理 → 添加服务器」→ 生成 token → 拿到一键安装命令
2. 在目标服务器上执行：

```bash
curl -fsSL "https://panel.example.com/api/remote/install.sh?token=<一次性令牌>&xray_mode=external" | bash
```

Agent 会：下载二进制 → 校验 sha256 → 写入配置 → 注册 systemd → 出站连回主控。
主控侧服务器卡片随即变为**在线**。

### 一条命令验证全链路

```bash
make e2e
```

会真实拉起一个主控 + 一个 Agent，验证：首启初始化 → 建服务器 → 签发令牌 → 安装脚本 → enroll → WebSocket 握手 → 指标入库 → 任务下发 → 非白名单任务被拒 → 审计落库。

---

## 架构一览

```
浏览器 ──► mmx-master ──┬─ HTTP API      /api/**
                        ├─ Agent 网关    /api/agent/**   (WS + HTTP 降级)
                        ├─ 订阅出口      /sub/**
                        ├─ 探针          /probe/**
                        ├─ MCP           /mcp
                        └─ 内嵌前端      /  (embed.FS)
                              ▲
                              │ 出站 WSS（Agent 发起，可穿过 NAT）
        ┌─────────────────────┼─────────────────────┐
   mmx-agent #1          mmx-agent #2          mmx-agent #3
        └─ xray-core（内联或外置）
```

细节见 **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**。

---

## 安全模型

| 边界 | 做法 |
|---|---|
| **Agent 不出任意命令** | 任务类型是**编译期白名单**（`pkg/agentproto/taskkind.go`）。没有 `shell.exec` 这类类型，新增任务必须改代码 + 加测试 |
| **任务必须签名** | 主控用 Ed25519 对 `v1\|task_id\|kind\|sha256(args)\|nonce` 签名，Agent 验签通过才执行 |
| **令牌三级隔离** | `enroll_token`（一次性，30 分钟）→ `agent_token`（长期，只存 sha256）→ `task_grant`（单次） |
| **独占防抖** | 同一 `agent_token` 换 IP 接入时锁定首个 IP 60 秒，并记录 `agent_token_reuse` 审计 |
| **单向连接** | Agent 从不监听端口，只做出站连接；主控不主动连 Agent |
| **口令与会话** | bcrypt 散列；会话 token 只存 sha256；`HttpOnly` + `SameSite=Lax` cookie |

---

## 目录结构

```
mmx/
├── cmd/
│   ├── mmx-master/            主控入口
│   └── mmx-agent/             Agent 入口
├── internal/
│   ├── agent/                 Agent 运行时（config / link / collect / xray）
│   ├── agentlink/             ★ 主控编排核心（registry / hub / ws / installer / sweeper）
│   ├── auth/                  口令、令牌、会话
│   ├── config/                双端配置加载
│   ├── httpapi/               路由、中间件、handler
│   ├── idgen/                 ULID 标识符
│   ├── logging/               结构化日志 + 轮转
│   ├── storage/               数据访问层 + 迁移
│   └── version/               构建期版本注入
├── pkg/
│   └── agentproto/            ★ Master 与 Agent 共用的线协议
├── docs/                      架构 / 协议 / 功能矩阵 / 路线图
├── scripts/e2e.sh             端到端联调
└── web/                       前端（P9）
```

---

## 与妙妙屋X 的关系

MMX 的功能面以妙妙屋X 为对标基线，但**有意做了三处不同**：

| 项 | 妙妙屋X | MMX |
|---|---|---|
| 许可体系 | 必需（厂商许可服务器裁决） | **不存在**。所有能力默认可用 |
| 数量配额 | 试用档强制上限 | **不设人为上限** |
| 遥测 / 构建指纹上报 | 上报厂商 | **完全不做** |
| REALITY 域名池 | 厂商云端共享池 | **本地池**，可选多主控同步 |
| 拼车 | 厂商云中介 | **本地中介** |
| Agent | 厂商专有二进制 | **自研**，协议见 `docs/AGENT-PROTOCOL.md` |

MMX **不包含**任何来自妙妙屋X 的代码、二进制或私有协议实现。

---

## 开发

```bash
make help          # 查看全部目标
make check         # 提交前必跑
make e2e           # 端到端联调
make agent-all     # 交叉编译各平台 Agent
```

工作纪律：

1. 每个阶段必须能 `make check` 通过才能进入下一阶段
2. 新功能必须配端到端断言（`scripts/e2e*.sh`），不做"只写不验"
3. 协议变更必须同步改 `docs/AGENT-PROTOCOL.md` 与 `pkg/agentproto`
4. 提交信息用 `feat(scope): ...` / `fix(scope): ...`

---

## 许可

[MIT](LICENSE)

依赖的上游开源组件均保留其各自许可（Go 标准库、go-chi、gorilla/websocket、oklog/ulid、modernc.org/sqlite、golang.org/x/crypto、gopkg.in/yaml.v3）；
后续引入 xray-core（MPL-2.0）、go-acme/lego（MIT）、go-telegram/bot（MIT）等时会在 `THIRD_PARTY_NOTICES.md` 逐项列明。
