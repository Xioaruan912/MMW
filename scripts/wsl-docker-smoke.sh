#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
docker rm -f mmx-smoke >/dev/null 2>&1 || true
echo "=== 启动容器 ==="
docker run -d --name mmx-smoke -p 12899:12889 -e MMX_PUBLIC_URL=http://127.0.0.1:12899 mmx:ci >/dev/null
for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:12899/api/health >/dev/null 2>&1; then break; fi
  sleep 1
done
echo "health: $(curl -fsS http://127.0.0.1:12899/api/health || echo FAIL)"
echo "setup : $(curl -fsS http://127.0.0.1:12899/api/setup/status || echo FAIL)"
echo "index : $(curl -fsS http://127.0.0.1:12899/ | grep -o '<div id="root">' || echo FAIL)"
echo "formats: $(curl -fsS http://127.0.0.1:12899/sub/formats | head -c 80)"
echo
echo "=== 容器内用户与数据目录 ==="
docker exec mmx-smoke id
docker exec mmx-smoke ls -la /data
echo
echo "=== 日志 ==="
docker logs mmx-smoke 2>&1 | tail -8
docker rm -f mmx-smoke >/dev/null
echo "smoke done"
