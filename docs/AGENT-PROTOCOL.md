# MMX Agent 协议规格 v1

> 主控与 Agent 之间唯一的契约。**Master 与 Agent 编译期共用 `pkg/agentproto`**，
> 因此本文件描述的是「线上格式」，任何字段变更都必须同步改包版本号。

- 协议版本：`1`
- 传输：WebSocket（主）/ HTTPS 轮询（降级）
- 编码：JSON（Envelope），二进制负载用 base64

---

## 1. 端点

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| `POST` | `/api/agent/enroll` | 一次性 `enroll_token` | 首次注册：换回长期 `agent_token` + 主控公钥 |
| `GET` | `/api/agent/ws` | `agent_token`（`Authorization: Bearer` 或 `?token=`） | 主通道 |
| `POST` | `/api/agent/pull` | `agent_token` | 降级轮询：取任务 + 上报指标（同一 Envelope 格式） |
| `GET` | `/api/remote/install.sh` | 一次性 `token`（query） | 生成安装脚本（不鉴权，token 自身即凭据） |

### 1.1 令牌模型（三级）

```
[管理员点「添加服务器」]
        │
        ▼
① enroll_token   一次性，TTL 30 分钟，绑定 server_id
        │  Agent 首次 enroll 时消费
        ▼
② agent_token    长期，存 sha256(明文)，绑定 server_id
        │  可轮换：主控下发 RotateToken 任务，新旧并行 5 分钟
        ▼
③ task_grant     短期，单次任务授权，Ed25519 签名
```

**独占规则**：一个 `agent_token` 只允许一个 IP 的活跃连接。
新 IP 接入时若旧连接仍在，主控**锁定先连 IP 60 秒**并记录 `agent_token_reuse` 审计事件。
（这条是防"同一 token 复制到多台机器"造成的写入风暴。）

---

## 2. Envelope

```jsonc
{
  "id": "01J...",           // ULID，发送方生成，幂等键
  "type": "heartbeat",
  "v": 1,                    // 协议版本
  "sent_at": 1788000000123,  // unix 毫秒
  "nonce": "base64",         // 仅 task/task_result 需要
  "payload": { }             // 见下
}
```

### 消息类型

| type | 方向 | payload |
|---|---|---|
| `hello` | Agent → Master | `HelloPayload` |
| `hello_ack` | Master → Agent | `HelloAckPayload` |
| `heartbeat` | Agent → Master | `HeartbeatPayload` |
| `heartbeat_ack` | Master → Agent | `{}` |
| `metrics` | Agent → Master | `MetricsPayload` |
| `task` | Master → Agent | `TaskPayload` |
| `task_result` | Agent → Master | `TaskResultPayload` |
| `config` | Master → Agent | `ConfigPayload`（完整或 diff） |
| `ack` | 双向 | `{ "id": "<被确认的 envelope id>" }` |
| `error` | 双向 | `{ "code": "", "message": "" }` |

---

## 3. 载荷定义

### 3.1 `HelloPayload`（Agent → Master，连接建立后第一条）

```jsonc
{
  "agent_version": "1.0.0",
  "protocol_version": 1,
  "hostname": "node-01",
  "os": "linux", "arch": "amd64",
  "kernel": "6.1.0-13-amd64",
  "distro": "Debian GNU/Linux 13",
  "boot_id": "8f2c...",          // 用于识别"同一台机器重启"
  "xray_mode": "embedded",        // embedded | external | none
  "xray_version": "25.9.1",
  "capabilities": ["metrics", "xray_control", "nginx_install", "acme_deploy"],
  "listen_port": 12891
}
```

### 3.2 `HelloAckPayload`（Master → Agent）

```jsonc
{
  "server_id": "srv_01J...",
  "server_name": "香港-01",
  "heartbeat_interval_sec": 15,
  "metrics_interval_sec": 30,
  "master_public_key": "base64-ed25519",   // 用于校验 task 签名
  "features": { "probe": true, "acme": true, "carpool": true },
  "server_time": 1788000000123             // Agent 据此校正时钟偏差
}
```

### 3.3 `HeartbeatPayload`（Agent → Master）

```jsonc
{
  "uptime_sec": 86400,
  "xray_running": true,
  "xray_config_rev": "sha256:ab12...",   // 配置版本，主控据此决定是否重下发
  "agent_load": 0.12,
  "seq": 1042
}
```

### 3.4 `MetricsPayload`（Agent → Master）

```jsonc
{
  "ts": 1788000000123,
  "cpu_percent": 12.5,
  "mem_used": 536870912, "mem_total": 2147483648,
  "swap_used": 0, "swap_total": 0,
  "disk_used": 12884901888, "disk_total": 42949672960,
  "load1": 0.12, "load5": 0.20, "load15": 0.18,
  "net_up": 1048576, "net_down": 8388608,       // 字节/秒（区间平均）
  "tcp_conns": 342, "udp_conns": 12,
  "processes": 128,
  "xray_rss": 104857600,
  "traffic": {                                   // 自上次上报以来的增量（字节）
    "up": 123456789, "down": 987654321
  }
}
```

### 3.5 `TaskPayload`（Master → Agent）

```jsonc
{
  "task_id": "tsk_01J...",
  "kind": "xray.apply_config",   // 见任务白名单
  "nonce": "base64-16bytes",
  "signature": "base64-ed25519", // 主控私钥对 (task_id|kind|sha256(args)|nonce) 签名
  "deadline_ms": 60000,
  "args": { }                    // 依 kind 而定
}
```

#### 任务白名单（**没有任意 shell**）

| kind | args | 说明 |
|---|---|---|
| `xray.install` | `{ "mode": "embedded\|external", "version": "25.9.1" }` | 安装 Xray |
| `xray.apply_config` | `{ "config": {...}, "rev": "sha256:.." }` | 写入配置并热重载 |
| `xray.restart` / `xray.stop` / `xray.start` | `{}` | 生命周期 |
| `xray.uninstall` | `{}` | 卸载 |
| `nginx.install` / `nginx.uninstall` | `{}` | 反代组件 |
| `agent.rotate_token` | `{ "new_token": "..." }` | 令牌轮换 |
| `agent.upgrade` | `{ "version": "1.1.0", "sha256": "...", "url": "..." }` | 自升级 |
| `sys.reboot` | `{ "delay_sec": 5 }` | 重启（需管理员二次确认） |
| `acme.deploy_cert` | `{ "domain": "...", "cert_pem": "...", "key_pem": "..." }` | 部署证书 |
| `probe.collect_now` | `{}` | 立即采集一次 |

### 3.6 `TaskResultPayload`（Agent → Master）

```jsonc
{
  "task_id": "tsk_01J...",
  "status": "ok",             // ok | failed | rejected
  "started_at": 1788000000200, "finished_at": 1788000003500,
  "error": "",
  "output": "…截断到 8KB…",
  "result": { }               // 依 kind 而定，如新配置的 rev
}
```

### 3.7 `ConfigPayload`（Master → Agent）

主控把「这台服务器应该长什么样」整体下发，Agent 侧做幂等收敛：

```jsonc
{
  "rev": "sha256:ab12...",
  "xray": {
    "log":   { "loglevel": "warning" },
    "inbounds":  [ { "tag": "in-443", "protocol": "vless", "port": 443, ... } ],
    "outbounds": [ { "tag": "direct", "protocol": "freedom" } ],
    "routing":   { "rules": [ ] },
    "dns":       { }
  },
  "nginx": { "enabled": true, "conf": "..." }
}
```

---

## 4. 连接生命周期

```
Agent 启动
  │  读取配置：master_url / agent_token / heartbeat_interval
  ▼
POST /api/agent/enroll   ──── 首次才有 enroll_token；否则跳过
  │
  ▼
GET /api/agent/ws?token=…
  │
  ├─ Master: 校验 token → 绑定 server_id → 检查 IP 独占
  ├─ Agent : 发 hello
  ├─ Master: 回 hello_ack（含心跳间隔、主控公钥）
  ▼
循环：
  ├─ 每 heartbeat_interval 发 heartbeat
  ├─ 每 metrics_interval   发 metrics
  ├─ 收到 task → 校验签名 + nonce 未过期 → 执行 → 回 task_result
  └─ 收到 config → 计算 rev，与本地比对；不一致则 apply
  ▼
断线 → 指数退避重连（1s,2s,4s,…,最长 60s，带 ±20% 抖动）
     → 连续 3 次 WS 失败 → 降级 HTTP /api/agent/pull（30s 一次）
     → 恢复后自动升回 WS
```

**离线判定**：Master 侧 `now - last_seen_at > 3 × heartbeat_interval` → `offline`。
超时 10 分钟未归 → `degraded`（保留最后指标，探针标记为离线）。

---

## 5. 时钟与幂等

- 所有时间为 unix 毫秒；Agent 用 `hello_ack.server_time` 计算偏移并本地校正。
- `Envelope.id`（ULID）是幂等键：Master 对 `metrics` / `task_result` 做 10 分钟去重窗口。
- `task_result` 丢失时，Master 按 `task_id` 重查；超过 `deadline_ms × 3` 未回 → 判失败。

---

## 6. 兼容性策略

- `v` 字段：Master 支持 `[1]`；遇到不支持的版本回 `error{code:"unsupported_protocol"}` 并断开。
- 新增字段一律可选（`omitempty`），旧端忽略未知字段。
- 破坏性变更 → 升主版本号，Master 对旧版 Agent 保持只读（能收心跳，不下发任务）。
