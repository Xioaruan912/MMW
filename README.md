# MMX

**自托管的代理节点管理与订阅分发平台。**

把多台服务器上的代理节点统一管起来，把订阅链接分发给用户。
Master–Agent 架构，Docker 一键部署，**不依赖任何外部云服务**。

[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-29MB-blue)](Dockerfile)

---

## 一条命令装好

```bash
curl -fsSL https://raw.githubusercontent.com/Xioaruan912/MMW/main/install.sh | bash
```

脚本会自动完成：装 Docker（如果没装）→ 拉镜像（拉不到就本地构建）→ 启动容器 →
等健康检查通过后打印面板地址。数据落在宿主目录里，长期保留。

装完后打开 `http://<本机IP>:12889/`，按向导创建管理员账号即可。

### 也可以自己用 Docker

```bash
docker run -d --name mmx --restart unless-stopped \
  -p 12889:12889 \
  -v /opt/mmx/data:/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xioaruan912/mmw:latest
```

或用 compose（想固定版本、加反代时更顺手）：

```bash
git clone https://github.com/Xioaruan912/MMW.git && cd MMW
docker compose up -d
```

---

## 装完怎么用

### 1. 登录面板

`http://<本机IP>:12889/` → 首次进入是初始化向导 → 创建管理员 → 登录。

### 2. 接入节点机

面板 → **服务管理** → **添加服务器** → 填名称与地址 → 选 Xray 模式 → 点生成 Token。

面板会给出一条命令，**复制到目标服务器上执行**：

```bash
curl -fsSL "http://<你的面板地址>/api/remote/install.sh?token=<一次性令牌>&xray_mode=external" | bash
```

Agent 会自动下载、校验、注册服务并连回主控。稍等几秒，服务器卡片就会变成**在线**。

> Agent 只做出站连接，从不监听端口 —— 节点机在 NAT 后面也能接入。

### 3. 配置节点

**节点与入站** → 新增入站 → 选协议（VLESS / VMess / Trojan / Shadowsocks）、
传输（TCP / WS / gRPC / HTTPUpgrade）、安全层（TLS / REALITY）→ 创建。

然后点该服务器的 **下发配置**，主控会生成 Xray 配置并推给 Agent 落地。

### 4. 分发订阅

**用户与订阅** → 新增用户 → 打开该用户的 **订阅链接** → 把链接填进客户端。

支持 12 种格式，客户端按 User-Agent 自动匹配；也可以手动指定：

| 客户端 | 链接 |
|---|---|
| Clash / mihomo / Stash | `?format=clash` |
| Surge | `?format=surge` |
| Loon | `?format=loon` |
| Quantumult X | `?format=qx` |
| sing-box | `?format=singbox` |
| Shadowrocket / v2rayN | 直接填订阅地址（base64 URI） |

---

## 主要功能

| 模块 | 说明 |
|---|---|
| **服务管理** | 多台服务器统一接入，一键安装 Agent，实时在线状态与系统指标 |
| **节点与入站** | 4 种协议 × 4 种传输 × 3 种安全层；配置一键下发 |
| **订阅分发** | 12 种客户端格式；订阅模板可自定义代理组与规则 |
| **用户与套餐** | 用户、流量配额、套餐、兑换码、到期提醒 |
| **流量统计** | 服务器与用户的日流量、计费周期、配额判定 |
| **节点探测** | TCP 真连测延迟，按延迟排序 |
| **证书** | ACME 自动申请续期（HTTP-01 / DNS-01） |
| **探针** | 对外伪装成状态监控页，可选密码保护 |
| **出站与路由** | 自定义出站、链式代理（中转）、按域名/IP 分流 |
| **通知** | Telegram、Webhook（带签名），服务器上下线自动提醒 |
| **备份恢复** | 一键导出/导入，恢复自动留档旧库 |
| **MCP** | 内建 MCP 服务，AI Agent 可直连运维 |
| **双因子** | TOTP + 一次性恢复码；接口令牌可单独吊销 |

---

## 运维

```bash
docker logs -f mmx            # 看日志
docker restart mmx            # 重启
docker stop mmx               # 停止
docker rm -f mmx && rm -rf /opt/mmx/data   # 卸载
```

**升级**：重跑安装脚本即可，`/opt/mmx/data` 不受影响。

**备份**：面板 → 系统设置 → 备份，或直接备份宿主上的 `/opt/mmx/data`。
这是唯一需要备份的目录（数据库 + 主控密钥）。

**放到公网**：建议前面挂一层 Nginx 只暴露 443。
示例配置见 [`deploy/nginx.conf.example`](deploy/nginx.conf.example)，
重跑安装脚本时填上公网地址、并把 `MMX_TRUST_PROXY` 设为 `1`。

---

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `MMX_PORT` | `12889` | 对外端口 |
| `MMX_DATA_DIR` | `/opt/mmx/data` | 数据目录 |
| `MMX_PUBLIC_URL` | 空 | 公网地址，用于生成 Agent 安装命令 |
| `MMX_TRUST_PROXY` | `0` | 部署在反代之后时设为 `1` |
| `MMX_IMAGE` | ghcr.io 镜像 | 指定镜像（拉不到会自动本地构建） |
| `MMX_YES` | `0` | 设为 `1` 则非交互全用默认值 |

---

## 安全模型

| 边界 | 做法 |
|---|---|
| Agent 不接受任意命令 | 任务类型是**编译期白名单**，没有"执行 shell"这类任务 |
| 任务必须签名 | 主控用 Ed25519 签名，Agent 验签通过才执行 |
| 令牌分层 | 一次性入驻令牌 → 长期令牌 → 单次任务签名，泄露半径逐层收敛 |
| 单向连接 | Agent 只出站，从不监听端口 |
| 面板加固 | 登录防爆破、CSRF、TOTP 双因子、接口令牌可吊销 |
| 无遥测 | 不做任何使用数据上报 |

---

## 许可

[MIT](LICENSE)。第三方组件见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

> MMX 不分发 Xray-core 等第三方程序；`xray.install` 由 Agent 从对应官方 Release
> 下载并校验，属于使用者自行获取。
