#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
go get github.com/pquerna/otp@latest 2>&1 | tail -3
go mod tidy 2>&1 | tail -3
echo "--- build ---"
go build ./... && echo "build OK"
go vet ./... && echo "vet OK"
go test -count=1 ./... 2>&1 | grep -v "no test files" | tail -10
