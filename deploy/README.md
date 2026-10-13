# 部署资源

日常部署**只需要根目录的一条命令**：

```bash
curl -fsSL https://raw.githubusercontent.com/Xioaruan912/MMW/main/install.sh | bash
```

本目录下的文件是给需要更细控制的人用的：

| 文件 | 用途 |
|---|---|
| `docker-entrypoint.sh` | 容器入口（镜像内部使用，一般不用管） |
| `nginx.conf.example` | 反向代理示例。含 WebSocket 透传与订阅出口的特殊处理 |
| `README.md` | 本文件 |

## 用 Nginx 放到公网

1. 按 `nginx.conf.example` 建好站点（把域名与证书路径改成自己的）
2. 重跑安装脚本，填上公网地址，并把 `MMX_TRUST_PROXY` 设为 `1`
   ```bash
   MMX_PUBLIC_URL=https://panel.example.com MMX_TRUST_PROXY=1      bash install.sh
   ```
3. 只需放行 443，面板端口不必对公网开放

> `MMX_TRUST_PROXY=1` 是必需的：否则主控看到的来源 IP 全是 127.0.0.1，
> 登录限流与 Agent 令牌独占判定会全部失效。

## 从源码构建

```bash
git clone https://github.com/Xioaruan912/MMW.git && cd MMW
docker build -t mmx:local .
docker run -d --name mmx --restart unless-stopped -p 12889:12889 -v /opt/mmx/data:/data mmx:local
```
