# 第三方组件与许可

MMX 自身的代码以 [MIT](LICENSE) 发布。下面列出运行期依赖的上游组件及其许可。

## Go 依赖

| 组件 | 用途 | 许可 |
|---|---|---|
| [github.com/go-chi/chi/v5](https://github.com/go-chi/chi) | HTTP 路由 | MIT |
| [github.com/gorilla/websocket](https://github.com/gorilla/websocket) | WebSocket（Agent 网关 / 探针推送） | BSD-3-Clause |
| [github.com/oklog/ulid/v2](https://github.com/oklog/ulid) | 时间有序标识符 | Apache-2.0 |
| [github.com/pquerna/otp](https://github.com/pquerna/otp) | TOTP 双因子 | Apache-2.0 |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) | bcrypt / curve25519 | BSD-3-Clause |
| [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml) | YAML（订阅与模板） | MIT / Apache-2.0 |
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) | 纯 Go SQLite 驱动（无 cgo） | BSD-3-Clause |

## 前端依赖

| 组件 | 用途 | 许可 |
|---|---|---|
| [react](https://react.dev) / [react-dom](https://react.dev) | UI 框架 | MIT |
| [vite](https://vitejs.dev) | 构建工具 | MIT |
| [typescript](https://www.typescriptlang.org) | 类型系统 | Apache-2.0 |

## 运行期可选的外部程序

MMX **不内嵌**这些程序，只在需要时由 Agent 下载或调用：

| 程序 | 用途 | 许可 |
|---|---|---|
| [Xray-core](https://github.com/XTLS/Xray-core) | 代理内核（`xray.install` 从官方 Release 获取） | MPL-2.0 |
| [mihomo](https://github.com/MetaCubeX/mihomo) | 节点真连测速（可选） | GPL-3.0 |

> 说明：MMX 与上述项目无隶属关系，也不分发它们的二进制。
> `xray.install` 由 Agent 从 Xray 官方 Release 下载并校验，属于使用者自行获取。
