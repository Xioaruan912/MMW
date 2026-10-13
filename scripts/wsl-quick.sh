#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
gofmt -s -w ./cmd ./internal ./pkg
go build ./... && echo "build OK"
go vet ./... && echo "vet OK"
go test -count=1 ./pkg/... 2>&1 | tail -3
