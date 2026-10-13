#!/usr/bin/env bash
# 在 WSL 内跑 go mod tidy + build + vet + test
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1

echo "=== go version ==="; go version
echo; echo "=== go mod tidy ==="; go mod tidy 2>&1 | tail -10
echo; echo "=== go build ./... ==="
go build ./... 2>&1 | head -50
echo "build exit=$?"
echo; echo "=== go vet ./... ==="; go vet ./... 2>&1 | head -50
echo; echo "=== go test ./... ==="; go test ./... 2>&1 | tail -25
