#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
echo "=== gofmt 检查 ==="
unformatted=$(gofmt -l . 2>/dev/null | grep -v '^web/' || true)
if [ -n "$unformatted" ]; then echo "需要格式化:"; echo "$unformatted"; else echo "OK"; fi
echo; echo "=== make check ==="
make -s check 2>&1 | tail -25
echo; echo "=== go test -count=1 ./pkg/... ==="
go test -count=1 ./pkg/... 2>&1 | tail -10
