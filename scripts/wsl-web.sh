#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx/web || exit 1
export npm_config_loglevel=error
if [ ! -d node_modules ]; then
  echo "=== npm install ==="
  npm install --no-audit --no-fund 2>&1 | tail -5
fi
echo "=== npm run build ==="
npm run build 2>&1 | tail -30
echo "build exit=$?"
ls -la dist 2>/dev/null | head -10
