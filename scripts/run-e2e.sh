#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
bash scripts/e2e.sh
