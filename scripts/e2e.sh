#!/usr/bin/env bash
# ============================================================================
# MMX 端到端联调：主控 + Agent 全链路验证
#
#   验证内容：
#     1. 主控启动、首启初始化管理员
#     2. 新建服务器 → 拿到一次性入驻令牌与安装命令
#     3. Agent 用入驻令牌 enroll，换回长期 agent_token
#     4. Agent 接入 WebSocket，主控可见「在线」
#     5. Agent 上报指标并入库
#     6. 主控下发白名单任务，Agent 执行并回执
#     7. 非白名单任务被拒绝
#
#   全程只需 bash + curl + python3
# ============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18989}"
BASE="http://127.0.0.1:${PORT}"
WORK="$(mktemp -d /tmp/mmx-e2e.XXXXXX)"
COOKIE="$WORK/cookie.txt"
MASTER_LOG="$WORK/master.log"
AGENT_LOG="$WORK/agent.log"
PASS=0
FAIL=0

c(){ printf '\033[%sm%s\033[0m\n' "$1" "$2"; }
ok(){ PASS=$((PASS+1)); c 32 "  [PASS] $1"; }
bad(){ FAIL=$((FAIL+1)); c 31 "  [FAIL] $1"; }
head_(){ echo; c 36 "=== $1 ==="; }
cleanup(){
  [[ -n "${AGENT_PID:-}" ]] && kill "$AGENT_PID" 2>/dev/null
  [[ -n "${MASTER_PID:-}" ]] && kill "$MASTER_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

jget(){ python3 -c "import sys,json;d=json.load(sys.stdin);
import functools
p='$1'.split('.')
for k in p:
    d = d[int(k)] if k.isdigit() else d[k]
print(d if not isinstance(d,(dict,list)) else json.dumps(d))" 2>/dev/null; }

head_ "0. 编译"
make -s build || { bad "编译失败"; exit 1; }
[[ -x bin/mmx-master && -x bin/mmx-agent ]] && ok "bin/mmx-master、bin/mmx-agent 已就绪" || { bad "二进制缺失"; exit 1; }

head_ "1. 启动主控 :$PORT"
mkdir -p "$WORK/data"
MMX_DATA_DIR="$WORK/data" MMX_LISTEN="127.0.0.1:$PORT" MMX_DEBUG=1 \
  ./bin/mmx-master > "$MASTER_LOG" 2>&1 &
MASTER_PID=$!

for i in $(seq 1 40); do
  if curl -fsS -m 2 "$BASE/api/health" >/dev/null 2>&1; then break; fi
  sleep 0.25
done
HEALTH="$(curl -fsS -m 3 "$BASE/api/health" || true)"
if [[ "$HEALTH" == *'"status":"ok"'* ]]; then ok "健康检查通过: $HEALTH"; else bad "主控未就绪"; cat "$MASTER_LOG"; exit 1; fi

head_ "2. 首启初始化管理员"
NEEDS="$(curl -fsS "$BASE/api/setup/status" | jget needs_init)"
[[ "$NEEDS" == "True" ]] && ok "系统未初始化（needs_init=true）" || bad "needs_init 期望 True 实得 $NEEDS"

INIT_CODE="$(curl -s -o "$WORK/init.json" -w '%{http_code}' -c "$COOKIE" -X POST "$BASE/api/setup/init" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"Admin@123456","nickname":"管理员"}')"
[[ "$INIT_CODE" == "200" ]] && ok "管理员创建成功" || { bad "初始化失败 HTTP $INIT_CODE: $(cat "$WORK/init.json")"; exit 1; }

ME_CODE="$(curl -s -o "$WORK/me.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/me")"
[[ "$ME_CODE" == "200" ]] && ok "会话有效 (/api/me 200)" || bad "/api/me 返回 $ME_CODE"

CSRF="$(awk '/mmx_csrf/ {print $7}' "$COOKIE" | tail -1)"
[[ -n "$CSRF" ]] && ok "CSRF 令牌已下发" || bad "未收到 mmx_csrf cookie"

# 负例：带会话但不带 CSRF 头，状态变更必须被拒
NO_CSRF_CODE="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -X POST "$BASE/api/admin/servers" \
  -H 'Content-Type: application/json' -d '{"name":"csrf-probe"}')"
[[ "$NO_CSRF_CODE" == "403" ]] && ok "缺少 CSRF 头的变更请求被拒（403）" || bad "CSRF 防护失效：HTTP $NO_CSRF_CODE"

head_ "2b. 登录防爆破"
LOCKED=""
for i in $(seq 1 7); do
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/login" \
    -H 'Content-Type: application/json' -d '{"username":"admin","password":"wrong-password"}')"
  if [[ "$code" == "429" ]]; then LOCKED="yes"; break; fi
done
[[ "$LOCKED" == "yes" ]] && ok "连续错误口令触发限流（429）" || bad "登录限流未生效"
GOOD_CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/login" \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"Admin@123456"}')"
[[ "$GOOD_CODE" == "429" ]] && ok "锁定期内正确口令也被拒（429）" || bad "锁定期内竟然放行了：HTTP $GOOD_CODE"

head_ "3. 新建服务器并取入驻令牌"
CREATE_CODE="$(curl -s -o "$WORK/srv.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/api/admin/servers" \
  -H 'Content-Type: application/json' \
  -d '{"name":"e2e-node-01","address":"127.0.0.1","remark":"端到端测试","xray_mode":"external"}')"
[[ "$CREATE_CODE" == "201" ]] || { bad "新建服务器失败 HTTP $CREATE_CODE: $(cat "$WORK/srv.json")"; exit 1; }
SERVER_ID="$(cat "$WORK/srv.json" | jget server.id)"
ENROLL_TOKEN="$(cat "$WORK/srv.json" | jget enroll_token)"
INSTALL_CMD="$(cat "$WORK/srv.json" | jget install_cmd)"
ok "服务器已建: $SERVER_ID"
ok "入驻令牌已签发: ${ENROLL_TOKEN:0:12}…"
ok "安装命令: $INSTALL_CMD"

head_ "4. 安装脚本可生成"
SCRIPT_CODE="$(curl -s -o "$WORK/install.sh" -w '%{http_code}' "$BASE/api/remote/install.sh?token=$ENROLL_TOKEN&xray_mode=external")"
[[ "$SCRIPT_CODE" == "200" ]] && ok "GET /api/remote/install.sh 200" || bad "安装脚本 HTTP $SCRIPT_CODE"
grep -q "MASTER_URL=\"$BASE\"" "$WORK/install.sh" && ok "脚本内嵌主控地址正确" || bad "脚本未正确内嵌主控地址"
grep -q 'systemctl' "$WORK/install.sh" && ok "脚本包含 systemd 集成" || bad "脚本缺少 systemd 集成"

head_ "5. Agent 入驻（enroll）+ 接入"
mkdir -p "$WORK/agent"
cat > "$WORK/agent/config.yaml" <<YAML
master_url: "$BASE"
enroll_token: "$ENROLL_TOKEN"
state_dir: "$WORK/agent"
xray_mode: "external"
xray_bin_path: "$WORK/no-such-xray-binary"
xray_config_dir: "$WORK/agent/xray"
heartbeat_interval: 2s
metrics_interval: 2s
debug: true
YAML
./bin/mmx-agent -config "$WORK/agent/config.yaml" > "$AGENT_LOG" 2>&1 &
AGENT_PID=$!
sleep 3

grep -q "入驻成功" "$AGENT_LOG" && ok "Agent 完成 enroll" || { bad "Agent 未入驻"; tail -20 "$AGENT_LOG"; }
grep -q "握手完成" "$AGENT_LOG" && ok "Agent 完成 WebSocket 握手" || { bad "Agent 未握手"; tail -20 "$AGENT_LOG"; }

head_ "6. 主控侧可见在线 + 指标"
ONLINE="false"
for i in $(seq 1 20); do
  curl -s -b "$COOKIE" "$BASE/api/admin/servers" -o "$WORK/list.json"
  if [[ "$(cat "$WORK/list.json" | jget 'servers.0.online')" == "True" ]]; then ONLINE="true"; break; fi
  sleep 1
done
[[ "$ONLINE" == "true" ]] && ok "服务器显示为在线" || { bad "服务器未在线"; cat "$WORK/list.json"; }

MET_CODE="$(curl -s -o "$WORK/metrics.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/servers/$SERVER_ID/metrics")"
POINTS="$(cat "$WORK/metrics.json" | jget points | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"
[[ "$MET_CODE" == "200" && "$POINTS" -gt 0 ]] && ok "指标已入库（$POINTS 个采样点）" || bad "指标未入库 (HTTP $MET_CODE, points=$POINTS)"

CPU="$(cat "$WORK/list.json" | jget 'servers.0.metrics.cpu_percent' 2>/dev/null || echo '?')"
MEMPCT="$(python3 -c "
import json
d=json.load(open('$WORK/list.json'))
m=d['servers'][0].get('metrics') or {}
t=m.get('mem_total',0); u=m.get('mem_used',0)
print(f'{u/1048576:.0f}MB/{t/1048576:.0f}MB' if t else 'n/a')" 2>/dev/null)"
ok "最新指标: cpu=${CPU}% mem=$MEMPCT"

head_ "7. 任务下发（白名单）"
TASK_CODE="$(curl -s -o "$WORK/task.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/api/admin/servers/$SERVER_ID/tasks" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"probe.collect_now","args":{}}')"
TASK_STATUS="$(cat "$WORK/task.json" | jget 'result.status' 2>/dev/null || echo '?')"
if [[ "$TASK_CODE" == "200" && "$TASK_STATUS" == "ok" ]]; then
  ok "probe.collect_now 执行成功"
else
  bad "任务失败 HTTP $TASK_CODE status=$TASK_STATUS: $(head -c 200 "$WORK/task.json")"
fi

head_ "7b. 建入站 + 用户 + 凭据"
REALITY_KEY="$(python3 -c 'import os,base64;print(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))')"

INB_CODE="$(curl -s -o "$WORK/inbound.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/inbounds" -H 'Content-Type: application/json' -d "{
    \"server_id\": \"$SERVER_ID\",
    \"tag\": \"vless-reality-443\",
    \"protocol\": \"vless\",
    \"transport\": \"tcp\",
    \"security\": \"reality\",
    \"port\": 24443,
    \"remark\": \"e2e 主节点\",
    \"node_name\": \"香港-01\",
    \"stream\": {
      \"reality_dest\": \"www.example.com:443\",
      \"reality_server_names\": [\"www.example.com\"],
      \"reality_private_key\": \"$REALITY_KEY\",
      \"reality_short_ids\": [\"0123456789abcdef\"]
    },
    \"settings\": { \"flow\": \"xtls-rprx-vision\" }
  }")"
INBOUND_ID="$(cat "$WORK/inbound.json" | jget inbound.id 2>/dev/null || echo '')"
[[ "$INB_CODE" == "201" && -n "$INBOUND_ID" ]] && ok "入站已建: $INBOUND_ID (vless/tcp/reality)" \
  || { bad "建入站失败 HTTP $INB_CODE: $(head -c 300 "$WORK/inbound.json")"; }

NODE_ID="$(cat "$WORK/inbound.json" | jget node.id 2>/dev/null || echo '')"
[[ -n "$NODE_ID" ]] && ok "展示节点自动生成: $NODE_ID" || bad "未生成展示节点"

USER_CODE="$(curl -s -o "$WORK/user.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/users" -H 'Content-Type: application/json' \
  -d '{"username":"e2e-alice","password":"Alice@123456","nickname":"Alice"}')"
USER_ID="$(cat "$WORK/user.json" | jget user.id 2>/dev/null || echo '')"
SUB_TOKEN="$(cat "$WORK/user.json" | jget user.sub_token 2>/dev/null || echo '')"
[[ "$USER_CODE" == "201" && -n "$USER_ID" ]] && ok "用户已建: $USER_ID（自动签发订阅凭据）" \
  || bad "建用户失败 HTTP $USER_CODE: $(head -c 300 "$WORK/user.json")"

CLI_CODE="$(curl -s -o "$WORK/client.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/inbounds/$INBOUND_ID/clients" -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"$USER_ID\",\"email\":\"alice@e2e\"}")"
CLIENT_UUID="$(cat "$WORK/client.json" | jget client.uuid 2>/dev/null || echo '')"
[[ "$CLI_CODE" == "201" && -n "$CLIENT_UUID" ]] && ok "凭据已签发 uuid=${CLIENT_UUID:0:8}…" \
  || bad "签发凭据失败 HTTP $CLI_CODE: $(head -c 300 "$WORK/client.json")"

head_ "7c. Xray 配置生成与下发"
PREV_CODE="$(curl -s -o "$WORK/preview.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/servers/$SERVER_ID/xray-preview")"
PREV_REV="$(cat "$WORK/preview.json" | jget rev 2>/dev/null || echo '')"
[[ "$PREV_CODE" == "200" && -n "$PREV_REV" ]] && ok "配置预览生成 rev=$PREV_REV" \
  || bad "配置预览失败 HTTP $PREV_CODE: $(head -c 300 "$WORK/preview.json")"
python3 - "$WORK/preview.json" <<'PY' >/dev/null 2>&1 && ok "配置结构校验通过（inbounds/outbounds/routing 齐备）" || bad "配置结构不完整"
import json,sys
c=json.load(open(sys.argv[1]))['config']
assert c.get('inbounds') and c.get('outbounds') and c.get('routing'), 'missing sections'
assert any(i.get('tag')=='vless-reality-443' for i in c['inbounds']), 'inbound tag missing'
ib=[i for i in c['inbounds'] if i['tag']=='vless-reality-443'][0]
assert ib['streamSettings']['security']=='reality', 'security not reality'
assert ib['settings']['clients'][0]['id'], 'client uuid missing'
PY

APPLY_CODE="$(curl -s -o "$WORK/apply.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/servers/$SERVER_ID/apply")"
APPLIED="$(cat "$WORK/apply.json" | jget result.applied 2>/dev/null || echo 'false')"
APPLY_REV="$(cat "$WORK/apply.json" | jget result.rev 2>/dev/null || echo '')"
[[ "$APPLY_CODE" == "200" && "$APPLIED" == "True" ]] && ok "配置已下发并由 Agent 落地（rev=$APPLY_REV）" \
  || bad "配置下发失败 HTTP $APPLY_CODE: $(head -c 300 "$WORK/apply.json")"

# Agent 侧真实落地验证
sleep 1
CFG_FILE="$WORK/agent/xray/config.json"
REV_FILE="$WORK/agent/xray/.mmx-rev"
if [[ -f "$CFG_FILE" ]]; then
  ok "Agent 已写出配置文件（$(stat -c %s "$CFG_FILE") 字节）"
  python3 - "$CFG_FILE" "$REV_FILE" "$APPLY_REV" <<'PY' && ok "落地配置内容与 rev 一致" || bad "落地配置内容不一致"
import json, sys
cfg_path, rev_path, want_rev = sys.argv[1], sys.argv[2], sys.argv[3]
c = json.load(open(cfg_path))
assert any(i.get('tag') == 'vless-reality-443' for i in c['inbounds']), 'inbound 未写入'
ib = [i for i in c['inbounds'] if i['tag'] == 'vless-reality-443'][0]
assert ib['streamSettings']['security'] == 'reality', 'reality 配置丢失'
assert ib['settings']['clients'], 'clients 丢失'
got_rev = open(rev_path).read().strip()
assert got_rev == want_rev, f'rev 不匹配: {got_rev} != {want_rev}'
PY
else
  bad "Agent 未写出配置文件: $CFG_FILE"
fi

head_ "7c2. Xray 内核安装（本地镜像，离线可复现）"
export WORK
python3 - <<'PY'
import os, zipfile
work = os.environ['WORK']
dst = os.path.join(work, 'mirror', 'v99.0.0')
os.makedirs(dst, exist_ok=True)
with zipfile.ZipFile(os.path.join(dst, 'Xray-linux-64.zip'), 'w') as z:
    z.writestr('xray', '#!/bin/sh\necho "Xray 99.0.0 (MMX e2e fake)"\n')
    z.writestr('geoip.dat', b'\0' * 1024)
    z.writestr('geosite.dat', b'\0' * 1024)
PY
ZIP_SHA="$(sha256sum "$WORK/mirror/v99.0.0/Xray-linux-64.zip" | cut -d' ' -f1)"

MIRROR_PORT=18990
( cd "$WORK/mirror" && python3 -m http.server "$MIRROR_PORT" --bind 127.0.0.1 >/dev/null 2>&1 & echo $! > "$WORK/mirror.pid" )
sleep 1
curl -sf -o /dev/null "http://127.0.0.1:$MIRROR_PORT/v99.0.0/Xray-linux-64.zip" \
  && ok "本地镜像已就绪（$MIRROR_PORT）" || bad "本地镜像未启动"

INSTALL_CODE="$(curl -s -o "$WORK/install-xray.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/servers/$SERVER_ID/tasks" -H 'Content-Type: application/json' -d "{
    \"kind\": \"xray.install\",
    \"args\": {
      \"mode\": \"external\",
      \"version\": \"99.0.0\",
      \"base_url\": \"http://127.0.0.1:$MIRROR_PORT\",
      \"sha256\": \"$ZIP_SHA\",
      \"asset_dir\": \"$WORK/agent/xray-assets\"
    }
  }")"
INSTALL_STATUS="$(cat "$WORK/install-xray.json" | jget 'result.status' 2>/dev/null || echo '?')"
[[ "$INSTALL_CODE" == "200" && "$INSTALL_STATUS" == "ok" ]] \
  && ok "xray.install 执行成功" \
  || bad "xray.install 失败 HTTP $INSTALL_CODE: $(head -c 400 "$WORK/install-xray.json")"

XRAY_BIN="$WORK/no-such-xray-binary"
if [[ -x "$XRAY_BIN" ]]; then
  ok "Xray 二进制已安装且可执行（$(stat -c %s "$XRAY_BIN") 字节）"
  "$XRAY_BIN" >/dev/null 2>&1 && ok "二进制可运行，版本输出: $("$XRAY_BIN")" || bad "二进制无法运行"
else
  bad "Xray 二进制未安装到 $XRAY_BIN"
fi
[[ -f "$WORK/agent/xray-assets/geoip.dat" ]] && ok "geoip.dat 已落盘" || bad "geoip.dat 缺失"
[[ -f "$WORK/agent/xray-assets/geosite.dat" ]] && ok "geosite.dat 已落盘" || bad "geosite.dat 缺失"

# 负例：sha256 不匹配必须拒绝，且不得覆盖已装二进制
BAD_CODE="$(curl -s -o "$WORK/install-bad.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/servers/$SERVER_ID/tasks" -H 'Content-Type: application/json' -d "{
    \"kind\": \"xray.install\",
    \"args\": {
      \"mode\": \"external\",
      \"version\": \"99.0.0\",
      \"base_url\": \"http://127.0.0.1:$MIRROR_PORT\",
      \"sha256\": \"0000000000000000000000000000000000000000000000000000000000000000\"
    }
  }")"
BAD_STATUS="$(cat "$WORK/install-bad.json" | jget 'result.status' 2>/dev/null || echo '?')"
[[ "$BAD_STATUS" == "failed" ]] && ok "sha256 不匹配时安装被拒（status=failed）" \
  || bad "sha256 校验未生效: HTTP $BAD_CODE status=$BAD_STATUS"
grep -q "sha256 校验失败" "$WORK/install-bad.json" && ok "错误信息明确指出 sha256 校验失败" \
  || bad "错误信息不明确"

kill "$(cat "$WORK/mirror.pid" 2>/dev/null)" 2>/dev/null || true

head_ "7d. 订阅输出"
SUB_CODE="$(curl -s -o "$WORK/sub-clash.yaml" -w '%{http_code}' -A 'clash-verge/1.7' "$BASE/sub/$SUB_TOKEN")"
[[ "$SUB_CODE" == "200" ]] && ok "Clash 订阅 200" || bad "Clash 订阅 HTTP $SUB_CODE"
grep -q "香港-01" "$WORK/sub-clash.yaml" && ok "订阅含节点名" || bad "订阅缺少节点名"
grep -q "$CLIENT_UUID" "$WORK/sub-clash.yaml" && ok "订阅含该用户的 uuid" || bad "订阅缺少用户 uuid"
grep -q "reality-opts" "$WORK/sub-clash.yaml" && ok "订阅含 REALITY 参数" || bad "订阅缺少 REALITY 参数"

# Shadowrocket 类客户端期望 base64 包装的 URI 列表
curl -s -o "$WORK/sub-b64.txt" -A 'Shadowrocket/2.2' "$BASE/sub/$SUB_TOKEN"
python3 - "$WORK/sub-b64.txt" <<'PY' > "$WORK/sub-uri.txt" && ok "URI 订阅（Shadowrocket UA）为 base64 包装的 vless://" || bad "URI 订阅格式错误"
import base64, sys
raw = open(sys.argv[1], 'rb').read().strip()
try:
    plain = base64.b64decode(raw).decode()
except Exception:
    plain = raw.decode(errors='replace')
assert plain.startswith('vless://'), f'期望 vless:// 开头，实得 {plain[:40]!r}'
sys.stdout.write(plain)
PY
grep -q "pbk=" "$WORK/sub-uri.txt" && ok "URI 含 REALITY 公钥（pbk=）" || bad "URI 缺少 REALITY 公钥"

# 显式指定 format=uri 时应返回明文
curl -s -o "$WORK/sub-plain.txt" "$BASE/sub/$SUB_TOKEN?format=uri"
grep -q "^vless://" "$WORK/sub-plain.txt" && ok "format=uri 返回明文 URI" || bad "format=uri 未生效"

curl -s -o /dev/null -w '%{http_code}' "$BASE/sub/not-a-real-token" > "$WORK/bad.tmp"
[[ "$(cat "$WORK/bad.tmp")" == "404" ]] && ok "无效订阅凭据返回 404" || bad "无效凭据未拒绝"

head_ "7e. 任务持久化与 Pull 降级通道"
TASKS_CODE="$(curl -s -o "$WORK/tasks.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/tasks?server_id=$SERVER_ID")"
TASKS_N="$(cat "$WORK/tasks.json" | jget total 2>/dev/null || echo 0)"
[[ "$TASKS_CODE" == "200" && "$TASKS_N" -ge 3 ]] \
  && ok "任务历史已持久化（$TASKS_N 条，含 probe/xray.apply_config/xray.install）" \
  || bad "任务历史异常 HTTP $TASKS_CODE total=$TASKS_N"
grep -q '"status":"ok"' "$WORK/tasks.json" && ok "任务记录含成功状态" || bad "任务记录缺少成功状态"

# 取出 Agent 的长期令牌（enroll 后写回了配置文件）
AGENT_TOKEN="$(awk '/^agent_token:/ {print $2}' "$WORK/agent/config.yaml" | tr -d '"')"
[[ -n "$AGENT_TOKEN" ]] && ok "已从 Agent 配置读取长期令牌" || bad "未能读取 Agent 令牌"

# 让服务器离线，这样新任务会停留在队列里而不是被 WS 立即消费
kill "$AGENT_PID" 2>/dev/null || true
wait "$AGENT_PID" 2>/dev/null || true
sleep 2

QUEUE_CODE="$(curl -s -o "$WORK/queued.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/servers/$SERVER_ID/tasks" -H 'Content-Type: application/json' \
  -d '{"kind":"probe.collect_now","args":{}}')"
[[ "$QUEUE_CODE" == "502" ]] && ok "离线服务器下发任务返回 502（任务已入队）" \
  || bad "离线下发应返回 502，实得 $QUEUE_CODE"

PULL_CODE="$(curl -s -o "$WORK/pull.json" -w '%{http_code}' -X POST "$BASE/api/agent/pull" \
  -H "Authorization: Bearer $AGENT_TOKEN" -H 'Content-Type: application/json' \
  -d '{"heartbeat":{"seq":1,"xray_running":false}}')"
[[ "$PULL_CODE" == "200" ]] && ok "Pull 通道可用（HTTP 200）" || bad "Pull 通道 HTTP $PULL_CODE"

python3 - "$WORK/pull.json" <<'PY' && ok "Pull 成功取回队列中的任务，且带签名与 nonce" || bad "Pull 未取回任务或签名缺失"
import json, sys
d = json.load(open(sys.argv[1]))
tasks = d.get('tasks') or []
assert tasks, 'Pull 返回的任务列表为空'
t = tasks[0]
assert t.get('kind') == 'probe.collect_now', f"任务类型不符: {t.get('kind')}"
assert t.get('signature'), '任务缺少 Ed25519 签名'
assert t.get('nonce'), '任务缺少 nonce'
assert t.get('task_id'), '任务缺少 task_id'
PY

# 再拉一次应当为空（已标记 dispatched，不会重复下发）
curl -s -o "$WORK/pull2.json" -X POST "$BASE/api/agent/pull" \
  -H "Authorization: Bearer $AGENT_TOKEN" -H 'Content-Type: application/json' \
  -d '{"heartbeat":{"seq":2,"xray_running":false}}'
PULL2_N="$(cat "$WORK/pull2.json" | jget tasks | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))' 2>/dev/null || echo -1)"
[[ "$PULL2_N" == "0" ]] && ok "已投递任务不会重复下发（幂等取用）" || bad "任务被重复下发: $PULL2_N"

# 无效令牌必须被拒
NOAUTH_CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/agent/pull" \
  -H "Authorization: Bearer not-a-real-token" -H 'Content-Type: application/json' -d '{}')"
[[ "$NOAUTH_CODE" == "401" ]] && ok "Pull 通道拒绝无效令牌（401）" || bad "Pull 鉴权失效: $NOAUTH_CODE"

head_ "8. 非白名单任务被拒"
REJ_CODE="$(curl -s -o "$WORK/rej.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/api/admin/servers/$SERVER_ID/tasks" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"shell.exec","args":{"cmd":"id"}}')"
[[ "$REJ_CODE" == "400" ]] && ok "shell.exec 被拒（HTTP 400）" || bad "任意命令未被拒绝（HTTP $REJ_CODE）"

head_ "9. 内嵌前端"
FE_CODE="$(curl -s -o "$WORK/index.html" -w '%{http_code}' "$BASE/")"
[[ "$FE_CODE" == "200" ]] && ok "GET / 返回 200" || bad "前端首页 HTTP $FE_CODE"
grep -qi '<div id="root"' "$WORK/index.html" && ok "首页是前端挂载点" || bad "首页不是前端页面"
SPA_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/servers")"
[[ "$SPA_CODE" == "200" ]] && ok "SPA 路由回落正常（/servers → 200）" || bad "SPA 回落失败 HTTP $SPA_CODE"
ASSET="$(grep -o '/assets/[A-Za-z0-9._-]*\.js' "$WORK/index.html" | head -1)"
if [[ -n "$ASSET" ]]; then
  A_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE$ASSET")"
  [[ "$A_CODE" == "200" ]] && ok "前端资源可加载: $ASSET" || bad "前端资源 HTTP $A_CODE: $ASSET"
else
  bad "首页未引用任何 /assets/*.js（前端没构建？）"
fi

head_ "10. 审计日志"
curl -s -b "$COOKIE" "$BASE/api/admin/audit" -o "$WORK/audit.json"
AUDIT_N="$(cat "$WORK/audit.json" | jget total 2>/dev/null || echo 0)"
if [[ "$AUDIT_N" -ge 3 ]]; then ok "审计记录 $AUDIT_N 条"; else bad "审计记录过少: $AUDIT_N"; fi
python3 - "$WORK/audit.json" <<'PY' 2>/dev/null
import json,sys
d=json.load(open(sys.argv[1]))['logs']
for x in d[:5]:
    print(f"      · {x['action']:<24} actor={x['actor'] or '-'} target={x['target'][:20]}")
PY

echo
c 36 "============================================================"
if [[ "$FAIL" -eq 0 ]]; then
  c 32 " 端到端全部通过：$PASS 项"
  c 36 "============================================================"
  exit 0
else
  c 31 " 失败 $FAIL 项 / 通过 $PASS 项"
  c 36 "============================================================"
  echo "主控日志末尾:"; tail -20 "$MASTER_LOG"
  echo "Agent 日志末尾:"; tail -20 "$AGENT_LOG"
  exit 1
fi
