#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
gofmt -s -w ./cmd ./internal ./pkg
go build ./... && echo "build OK"
go vet ./... && echo "vet OK"
go test -count=1 ./... 2>&1 | grep -vE "no test files" | tail -30
