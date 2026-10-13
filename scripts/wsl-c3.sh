#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git add -A
git -c core.autocrlf=false commit -q -m "feat(P6): Agent 侧真实安装 Xray 内核（xray.install）

- internal/agent/xray/install.go：
  探测平台资产名 -> 下载 -> sha256 校验 -> 解压 -> 原子替换二进制
  + geoip/geosite 落盘 -> 注册 systemd/OpenRC -> 有配置则启动
- 下载基址与版本可参数化（支持自建镜像）；固定默认版本而非 latest，保证可复现
- 原子性：先写 .tmp 再 rename，校验失败绝不覆盖已装二进制
- Manager 接口扩展 Install()，embedded / none 模式给出明确不支持的错误
- 任务超时 3min -> 10min；任务输出回传 version/sha256/service_mode/started

e2e 新增 8 项断言（本地 HTTP 镜像 + 伪造 zip，离线可复现）：
- 安装成功、二进制可执行且输出版本、geo 数据落盘
- sha256 不匹配时安装被拒且错误信息明确"
git --no-pager log --oneline | head -2
