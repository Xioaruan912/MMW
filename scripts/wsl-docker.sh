#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
echo "=== docker build（可能较慢，首次需拉取基础镜像）==="
timeout 1500 docker build -t mmx:ci --build-arg VERSION=ci . 2>&1 | tail -25
echo "build exit=$?"
echo
echo "=== 镜像 ==="
docker images mmx:ci --format '{{.Repository}}:{{.Tag}} {{.Size}}'
