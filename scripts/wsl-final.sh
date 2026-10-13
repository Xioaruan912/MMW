#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
echo "=== gofmt ==="
gofmt -s -w ./cmd ./internal ./pkg ./web/embed.go
echo "=== make check ==="
make -s check 2>&1 | grep -E "FAIL|ok |Error|error" | head -8
echo "=== 前端构建 ==="
( cd web && npm run build 2>&1 | tail -4 )
echo "=== 重新编译（含内嵌前端） ==="
make -s build && ls -la bin/
echo "=== 验证首页 ==="
./bin/mmx-master -version
