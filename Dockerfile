# =============================================================================
#  MMX 主控镜像 —— 多阶段构建，最终镜像只含一个静态二进制
#
#  构建：
#     docker build -t mmx:dev .
#  运行：
#     docker run -d --name mmx -p 12889:12889 \
#       -v mmx-data:/data -e MMX_PUBLIC_URL=https://panel.example.com mmx:dev
# =============================================================================

# ---------- 1. 前端 ----------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
# 优先用 lockfile 做可复现安装；没有 lockfile 时退回 install
RUN if [ -f package-lock.json ]; then npm ci --no-audit --no-fund; \
    else npm install --no-audit --no-fund; fi
COPY web/ ./
RUN npm run build

# ---------- 2. 后端 ----------
FROM golang:1.26-alpine AS build
WORKDIR /src

# 先只拷依赖清单，利用层缓存：源码改动不会导致重新下载依赖
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 用构建好的前端覆盖占位产物，使前端真正被 embed 进二进制
COPY --from=web /src/web/dist ./web/dist

ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w \
        -X github.com/mmx-project/mmx/internal/version.Version=${VERSION} \
        -X github.com/mmx-project/mmx/internal/version.Commit=${COMMIT} \
        -X github.com/mmx-project/mmx/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/mmx-master ./cmd/mmx-master && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w" -o /out/mmx-agent ./cmd/mmx-agent

# ---------- 3. 运行 ----------
FROM alpine:3.20
# ca-certificates：调用 Telegram / Webhook / ACME 都需要
# tzdata：日志与计费周期按本地时区
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S mmx && adduser -S -G mmx mmx && \
    mkdir -p /data && chown mmx:mmx /data

COPY --from=build /out/mmx-master /usr/local/bin/mmx-master
# Agent 也放进镜像，便于用同一个镜像在节点机上跑 Agent
COPY --from=build /out/mmx-agent /usr/local/bin/mmx-agent
COPY --from=build /src/deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh

USER mmx
WORKDIR /data
ENV MMX_DATA_DIR=/data \
    MMX_LISTEN=0.0.0.0:12889

EXPOSE 12889
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:12889/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["mmx-master"]
