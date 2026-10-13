# 文档索引

> MMX 的文档与代码同源：`docs/` 里的协议与设计决定，代码里都有对应的实现与测试。

## 入门

| 文档 | 内容 |
|---|---|
| [../README.md](../README.md) | 项目定位、快速开始、安全模型、目录结构 |
| [../docs/ARCHITECTURE.md](ARCHITECTURE.md) | 组件拓扑、模块职责、数据模型、部署形态 |
| [../docs/AGENT-PROTOCOL.md](AGENT-PROTOCOL.md) | Master–Agent 线协议规格（信封 / 载荷 / 任务白名单 / 连接生命周期） |

## 规划与验收

| 文档 | 内容 |
|---|---|
| [FEATURE-MATRIX.md](FEATURE-MATRIX.md) | 对标妙妙屋X 的功能验收基线（A–G 组）+ 有意不实现项（H 组） |
| [ROADMAP.md](ROADMAP.md) | P0–P9 阶段计划、里程碑判据、工作纪律 |

## 运维

| 资源 | 说明 |
|---|---|
| [../deploy/install-master.sh](../deploy/install-master.sh) | 主控一键安装（依赖自举 + sha256 校验 + systemd + 冒烟测试） |
| [../deploy/nginx.conf.example](../deploy/nginx.conf.example) | 反向代理示例（含 WebSocket 与订阅出口的特殊处理） |
| [../docker-compose.yml](../docker-compose.yml) | Docker 部署 |
| Agent 安装 | 面板「服务管理 → 添加服务器」生成的一键命令（内嵌一次性令牌） |

## 开发

```bash
make help          # 全部目标
make check         # vet + build + test（提交前必跑）
make e2e           # 端到端联调（主控 + Agent + 断言）
make agent-all     # 交叉编译各平台 Agent
```

工作纪律（摘自 ROADMAP）：

1. 每个阶段必须能 `make check` 通过才能进入下一阶段
2. 新功能必须配端到端断言（`scripts/e2e*.sh`），不做"只写不验"
3. 协议变更必须同步改 `docs/AGENT-PROTOCOL.md` 与 `pkg/agentproto`
4. 提交信息用 `feat(scope): ...` / `fix(scope): ...`

## 关键设计决定（为什么这样做）

这些决定在代码里都有注释说明，汇总在此便于快速理解：

| 主题 | 决定 | 原因 |
|---|---|---|
| Agent 任务 | **编译期白名单**，没有任意 shell 类型 | 一个"能执行任意命令"的 Agent 等于把节点机交出去 |
| 任务下发 | Ed25519 签名 + nonce + 幂等 ID | 防止中间人伪造指令；幂等 ID 让重放无害 |
| 任务队列 | 持久化到 `pending_tasks` | 主控重启不该让在途任务静默消失；Pull 降级通道也要能取任务 |
| Agent 连接 | 只做出站连接，从不监听端口 | 节点机通常在 NAT 后；也让攻击面收敛到主控一侧 |
| 令牌模型 | enroll（一次性）→ agent（长期）→ 任务签名 | 分层失效半径：泄露一次令牌不等于泄露全部 |
| 订阅格式 | 不支持就**明确报错**，不静默降级 | 给一份"看着像配置但导入即错"的文件，排障成本极高 |
| 模板占位符 | 缩进跟随占位符所在列 | 占位符既可能在顶层也可能在嵌套层，写死缩进必然在某一层被打破 |
| 备份 | `VACUUM INTO` 而非直拷 .db | WAL 模式下直拷可能拿到"主库已更新、WAL 未合并"的不一致副本 |
| 恢复 | 不在运行时原地替换数据库 | 会同时踩到连接池句柄、WAL 文件、内存状态未刷新 |
| 探针 | 默认关闭 + 默认不显示服务器名 | 探针是对外暴露面；服务器名可能包含内部标识 |
| 通知 | 全异步 + 去重窗口 | 通知服务器挂了不该拖垮主控；服务器抖动不该刷屏 |
| 限流 | 按 IP 与用户名双维度 | 只按 IP 可被分布式绕过；只按用户名会让攻击者锁死正常用户 |
| 可信 IP | 只在 `MMX_TRUST_PROXY=1` 时采信 XFF | 否则任何人可伪造来源 IP 绕过限流与令牌独占 |
| 流量归属 | 走 `xray api statsquery` 官方 CLI | 不必把 xray-core 的 protobuf 依赖拖进 Agent；CLI 与内核版本天然匹配 |
| MCP | 工具权限等同令牌所属账号 | AI Agent 不该成为权限提升的跳板 |
