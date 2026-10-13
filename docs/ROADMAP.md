# MMX 开发路线图

> 原则：**每个阶段都必须是一个「能跑、能验证」的完整切片**，不允许出现"写了三个月没跑过"的模块。

---

## 阶段总览

| 阶段 | 主题 | 关键交付 | 状态 |
|---|---|---|---|
| **P0** | 项目规划 | 架构 / 协议 / 功能矩阵 / 路线图 | ✅ |
| **P1** | 工程骨架 | go.mod / 配置 / 日志 / SQLite / HTTP 路由 / Makefile | ✅ |
| **P2** | 共享协议 | `pkg/agentproto` 信封与全部载荷 | ✅ |
| **P3** | 主控编排 | `agentlink`：服务器 + 令牌 + WS 网关 + 安装命令生成 | ✅ |
| **P4** | Agent 运行时 | `mmx-agent`：出站连接 / 心跳 / 指标 / 任务执行 | ✅ |
| **P5** | **端到端打通** | Master + Agent 跑通，指标入库，API 可见在线 | ✅ |
| **D1** | 安全欠债 | 登录防爆破 / CSRF / 可信客户端 IP | ✅ |
| **P6** | 核心业务 | 用户 / 入站 / 节点 / 订阅 / Xray 配置下发 / 双因子 | 🟡 主链路已通 |
| **P7** | 完整分发 | 12 种订阅格式 / 模板V3 / 探针 / ACME / 通知 / 面板主题 | ⚪ |
| **P8** | 高级能力 | 域名池 / 拼车 / TG Bot / MCP / 转发 / 限速 / 测速 | ⚪ |
| **P9** | 工程化 | 前端 embed ✅ / Docker / 一键安装 / CI / 文档站 | 🟡 |

---

## P0 —— 项目规划 ✅ 定义

**产出**
- `docs/ARCHITECTURE.md`：组件拓扑、模块职责、数据模型、部署形态
- `docs/AGENT-PROTOCOL.md`：信封格式、载荷定义、任务白名单、连接生命周期
- `docs/FEATURE-MATRIX.md`：功能验收基线（A–H 组）
- `docs/ROADMAP.md`：本文件

**验收**：能把"要做什么/怎么做/怎么验"讲清楚，且功能矩阵覆盖妙妙屋X 全部功能面。

---

## P1 —— 工程骨架

**产出**
```
mmx/
├── go.mod                    module github.com/mmx-project/mmx
├── Makefile                  build / run / test / lint / web
├── .gitignore .golangci.yml
├── cmd/mmx-master/main.go
├── cmd/mmx-agent/main.go
├── internal/
│   ├── config/               环境变量 + 配置文件 + 默认值 + 校验
│   ├── logging/              slog + 文件轮转
│   ├── storage/              sqlite/pg 双驱动 + 迁移框架
│   ├── version/              版本号（ldflags 注入）
│   └── httpapi/              chi 路由 + 中间件链（请求 ID / 日志 / 恢复 / CORS / 鉴权）
└── web/                      Vite + React 19 骨架（先空壳）
```

**验收**：`make build` 出两个二进制；`mmx-master` 能起来并响应 `/api/health`；`mmx-agent -h` 正常。

---

## P2 —— 共享协议包

**产出**：`pkg/agentproto/`
- `envelope.go`：Envelope、类型常量、编解码、ULID
- `payload.go`：Hello / HelloAck / Heartbeat / Metrics / Task / TaskResult / Config
- `taskkind.go`：任务白名单枚举 + 参数校验
- `sign.go`：Ed25519 签名/验签、nonce 生成
- `protocol_test.go`：往返编解码 + 签名校验 + 未知字段兼容

**验收**：`go test ./pkg/agentproto/...` 全绿；非法 kind 被拒；签名错被拒。

---

## P3 —— 主控编排（agentlink）

**产出**
- 表：`servers` / `agent_tokens` / `server_metrics` / `agent_audit`
- `internal/agentlink/registry.go`：服务器与令牌生命周期
- `internal/agentlink/hub.go`：连接注册表 + 广播 + 任务派发 + 结果等待
- `internal/agentlink/ws.go`：WS 网关（握手、IP 独占、令牌校验）
- `internal/agentlink/installer.go`：一键安装脚本生成
- `internal/httpapi/servers.go`：`/api/admin/servers` CRUD、`/api/remote/install.sh`
- 离线判定定时器（`3×heartbeat` → offline）

**验收**
- `curl /api/remote/install.sh?token=xxx` 返回可执行脚本
- 脚本里含 `master_url` / 一次性 token / `xray_mode`，且 token 只能消费一次

---

## P4 —— Agent 运行时

**产出**：`internal/agent/**`
- `config`：YAML + 环境变量 + 命令行
- `link`：WSS 客户端、退避重连、心跳、Pull 降级
- `collect`：`/proc` 解析采集（不引 cgo，纯 Go）
- `exec`：任务白名单执行器（含签名校验）
- `xray`：进程管理、配置写入、健康检查
- `cmd/mmx-agent`：单二进制

**验收**：Agent 起得来、连得上、心跳可见；非法任务被拒；断网自动重连。

---

## P5 —— 端到端打通

**产出**
- `scripts/e2e.sh`：一键起主控 + 起 Agent + 断言心跳入库 + 断言 API 在线
- 前端最小页面：服务器列表（显示在线 / 指标 / Xray 状态）

**验收**：`bash scripts/e2e.sh` 全绿。这是**第一个真正的里程碑**。

---

## P6 —— 核心业务（可用性拐点）

- 用户 / 角色 / 订阅凭据 / 2FA / API Token
- 入站向导（4 协议 × 2 传输 × TLS/REALITY）
- 节点管理 + 排序 + 标签
- Xray 配置生成 → 差异计算 → 下发到 Agent → 热重载
- 订阅链接生成（Clash + URI 两种先打通）
- 流量统计与计费周期

**验收**：一台主控 + 一台 Agent，能建节点、出订阅、在 Clash 里真正连上网。

---

## P7 —— 完整分发

- 12 种订阅格式补齐
- 模板 V3 + 自定义规则 + 代理组编辑器
- 探针（内建 + API + WS 推送）
- ACME（Cloudflare / 阿里云 / 腾讯云 / NameSilo）+ 证书下发
- 通知（TG / Webhook）、公告、备份恢复
- 面板：多主题 / 自定义 CSS / 品牌 / i18n
- 静默模式 / 安全日志 / 防爆破

---

## P8 —— 高级能力

- 域名池（本地池 + 多主控同步）
- 拼车（本地中介，多套餐 / 座位 / 共享订阅）
- TG Bot + MiniApp
- MCP 服务端
- 节点测速 / 限速 / 真连探测
- 链式代理 / 中继组 / 负载均衡 / 转发 / 出站路由 / override 脚本

---

## P9 —— 工程化

- 前端 `embed.FS` 进主控（单二进制交付）
- `Dockerfile` + `docker-compose.yml`
- 一键安装脚本（systemd + nginx 反代模板）
- GitHub Actions：build / test / lint / release（多平台）
- 文档站（VitePress 或 Astro）

---

## 里程碑判据

| 里程碑 | 判据 |
|---|---|
| **M1 骨架可跑** | `make build && ./mmx-master` 起得来；`/api/health` 200 |
| **M2 编排打通** | 安装命令 → Agent 接入 → 服务器卡片变在线（P5） |
| **M3 真能上网** | 主控建节点 → Agent 起 Xray → Clash 用订阅连上（P6） |
| **M4 功能对等** | `FEATURE-MATRIX.md` A–G 组全绿（P8） |
| **M5 可交付** | 单二进制 / Docker / 一键脚本三形态都能起（P9） |

---

## 工作纪律

1. **每阶段结束必须能 `make check`（build + vet + test）通过**，否则不进入下一阶段。
2. **不使用任何厂商私有接口**。所有协议我们自己定义在 `docs/*`，代码与文档同源。
3. **依赖只选上游开源项目**（xray-core / lego / go-telegram-bot / mcp-go …），不 vendor 任何厂商 fork。
4. **每个新功能必须配端到端断言**（`scripts/e2e*.sh`），不做"只写不验"。
5. 提交信息用 `feat(scope): ...` / `fix(scope): ...` 规范。

---

## 进度快照

```
P0 ✅  规划            架构 / 协议 / 功能矩阵 / 路线图
P1 ✅  工程骨架         go.mod / Makefile / config / logging / storage(只追加迁移) / httpapi 中间件
P2 ✅  共享协议         pkg/agentproto（信封 + 全部载荷 + 任务白名单 + Ed25519）
P3 ✅  主控编排         internal/agentlink（registry / hub / ws / installer / sweeper）
P4 ✅  Agent 运行时     internal/agent（config / link / collect / xray）
P5 ✅  端到端           e2e 覆盖 Master-Agent 全链路
D1 ✅  安全欠债         登录防爆破 / CSRF / 可信客户端 IP 判定
P6 🟡  核心业务主链路   入站 → 用户 → 凭据 → Xray 配置生成 → 下发 → Agent 落地 → 订阅输出
P9 🟡  工程化           前端已 embed（单二进制交付）
```

### P6 已完成（e2e 覆盖）

| 断言 | 结果 |
|---|---|
| 创建入站（VLESS × TCP × REALITY）+ 自动生成展示节点 | ✅ |
| 创建用户 + 自动签发订阅凭据 | ✅ |
| 为用户在入站上签发 UUID 凭据 | ✅ |
| `GET /servers/{id}/xray-preview` 生成完整 Xray 配置 | ✅ |
| rev 由配置内容派生（内容变则 rev 变，内容同则 rev 同） | ✅ |
| `POST /servers/{id}/apply` 经签名任务下发，Agent 同步回执 | ✅ |
| Agent 真实写出 `config.json` 并记录 `.mmx-rev` | ✅ |
| 订阅按 UA 分流（Clash / base64-URI / sing-box） | ✅ |
| Clash 订阅含节点名、uuid、REALITY 参数 | ✅ |
| URI 订阅含 `pbk=`（REALITY 公钥由服务端私钥派生） | ✅ |
| 未实现的私有格式（Surge/Loon/QX）明确报错而非静默降级 | ✅ |
| 无效订阅凭据返回 404 | ✅ |

### P6 追加完成（本轮）

| 断言 | 结果 |
|---|---|
| 任务持久化：离线下发的任务入队而非丢失 | 🟢 |
| Pull 降级通道可从同一队列取任务，且不重复下发 | 🟢 |
| 任务历史可查（含超时与失败） | 🟢 |
| 主控重启/掉线留下的 dispatched 任务由 sweeper 结算为超时 | 🟢 |
| TOTP 双因子：两步启用、一次性恢复码、登录强制第二因子 | 🟢 |
| API Token：可设 TTL、可吊销、豁免 CSRF、权限等同所属用户 | 🟢 |
| Xray 配置补齐 API inbound（原先缺失导致 stats 不可用） | 🟢 |
| 流量：服务器日表 + 每用户归集 + 配额三态判定 | 🟢 |

### P6 剩余

- SSE 流式安装进度
- 前端：流量图表、双因子设置页、API Token 管理页
- 更多订阅格式（Surge / Loon / QX / Egern / Surfboard）

