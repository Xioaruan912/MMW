#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
gofmt -s -w ./cmd ./internal ./pkg
echo "=== 格式化后检查 ==="
unformatted=$(gofmt -l ./cmd ./internal ./pkg || true)
[ -z "$unformatted" ] && echo "全部已格式化" || { echo "仍有:"; echo "$unformatted"; }
echo; echo "=== make check ==="
make -s check 2>&1 | grep -E "FAIL|ok |error|vet" | head -10
echo "check done"
