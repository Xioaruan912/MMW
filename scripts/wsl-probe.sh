#!/usr/bin/env bash
set -uo pipefail
export PATH=$PATH:/usr/local/go/bin
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
W=$(mktemp -d)
MMX_DATA_DIR=$W MMX_LISTEN=127.0.0.1:19999 ./bin/mmx-master >$W/m.log 2>&1 &
P=$!
sleep 2
echo "=== GET /sub/formats ==="
curl -s -i http://127.0.0.1:19999/sub/formats | head -20
echo
echo "=== GET /sub/nonexistent ==="
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:19999/sub/nonexistent
kill $P 2>/dev/null
rm -rf $W
