#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
echo "=== shellcheck: deploy ==="
shellcheck -S warning deploy/*.sh 2>&1 | head -40
echo "exit=$?"
echo
echo "=== shellcheck: scripts ==="
shellcheck -S warning scripts/e2e.sh 2>&1 | head -40
echo "exit=$?"
