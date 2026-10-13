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

head_ "7d2. 订阅格式矩阵"
FMT_CODE="$(curl -s -o "$WORK/formats.json" -w '%{http_code}' "$BASE/sub/formats")"
[[ "$FMT_CODE" == "200" ]] && ok "订阅格式列表 200" || bad "格式列表 HTTP $FMT_CODE"
# 用 grep 计数而不是把含中文的 JSON 再喂给 python：
# 在 ASCII locale 下 python 写 stdout 会抛 UnicodeEncodeError，管道会静默拿到空串。
FMT_N="$(grep -o '"format":' "$WORK/formats.json" | wc -l | tr -d ' ')"
[[ "$FMT_N" -ge 10 ]] && ok "已声明 $FMT_N 种订阅格式" || bad "格式数量偏少: $FMT_N"

# Surge：INI 家族，应含 [Proxy] 段与 REALITY 参数
curl -s -o "$WORK/sub-surge.conf" "$BASE/sub/$SUB_TOKEN?format=surge"
grep -q '^\[Proxy\]' "$WORK/sub-surge.conf" && ok "Surge 输出含 [Proxy] 段" || bad "Surge 段缺失"
grep -q "reality-public-key=" "$WORK/sub-surge.conf" && ok "Surge 输出含 REALITY 公钥" || bad "Surge 缺少 REALITY"
grep -q "香港-01 = vless" "$WORK/sub-surge.conf" && ok "Surge 节点行格式正确" || bad "Surge 节点行异常"

# Loon
curl -s -o "$WORK/sub-loon.conf" "$BASE/sub/$SUB_TOKEN?format=loon"
grep -q "香港-01 = VLESS" "$WORK/sub-loon.conf" && ok "Loon 节点行格式正确" || bad "Loon 节点行异常"
grep -q "public-key=" "$WORK/sub-loon.conf" && ok "Loon 输出含 REALITY 公钥" || bad "Loon 缺少 REALITY"

# Egern：YAML，用 policy_groups 而非 proxy-groups
curl -s -o "$WORK/sub-egern.yaml" "$BASE/sub/$SUB_TOKEN?format=egern"
grep -q "policy_groups:" "$WORK/sub-egern.yaml" && ok "Egern 使用 policy_groups" || bad "Egern 结构异常"
grep -q "proxy-groups" "$WORK/sub-egern.yaml" && bad "Egern 不应出现 Clash 的 proxy-groups" || ok "Egern 未混入 Clash 结构"

# UA 分流：Surge UA 应直接拿到 Surge 格式
curl -s -o "$WORK/sub-ua.conf" -A 'Surge/5.9.0' "$BASE/sub/$SUB_TOKEN"
grep -q '^\[Proxy\]' "$WORK/sub-ua.conf" && ok "按 Surge UA 自动分流到 Surge 格式" || bad "UA 分流未生效"

# 不支持 REALITY 的客户端必须明确报错，而不是给一份导入即错的配置
for f in surfboard qx; do
  code="$(curl -s -o "$WORK/sub-$f.out" -w '%{http_code}' "$BASE/sub/$SUB_TOKEN?format=$f")"
  if [[ "$code" == "400" ]] && grep -q "不支持 REALITY" "$WORK/sub-$f.out"; then
    ok "$f 对 REALITY 明确报错（不静默降级）"
  else
    bad "$f 应当拒绝 REALITY，实得 HTTP $code: $(head -c 160 "$WORK/sub-$f.out")"
  fi
done

head_ "7d3. 探针（伪装状态页）"
# 默认关闭：对外暴露面必须显式开启，关闭时统一 404（不暴露存在性）
CLOSED_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/public/probe-servers")"
[[ "$CLOSED_CODE" == "404" ]] && ok "探针默认关闭时返回 404（不暴露存在性）" || bad "关闭态返回 $CLOSED_CODE"

# 补服务器展示信息
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PATCH "$BASE/api/admin/servers/$SERVER_ID/display" \
  -H 'Content-Type: application/json' \
  -d '{"region":"🇭🇰","region_country":"中国香港","region_city":"香港","provider_name":"示例云","provider_url":"https://example.com","traffic_limit":1099511627776,"period_start_day":5}'

# 开启探针
PUT_CODE="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X PUT "$BASE/api/admin/probe/settings" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"title":"服务状态监控","theme":"auto","color_mode":"dark","show_name":false,"show_globe":true,"show_footer":true,"require_token":false,"refresh_seconds":5}')"
[[ "$PUT_CODE" == "200" ]] && ok "探针设置已保存" || bad "保存探针设置失败 HTTP $PUT_CODE"

curl -s -o "$WORK/probe.json" "$BASE/api/public/probe-servers"
python3 - "$WORK/probe.json" <<'PY' && ok "探针快照结构正确且默认不暴露服务器名" || bad "探针快照异常"
import json, sys
d = json.load(open(sys.argv[1]))
assert d['enabled'] is True, d
assert d['title'], '缺少标题'
assert d['appearance']['revision'] >= 1, '缺少外观版本号'
assert isinstance(d['servers'], list) and len(d['servers']) >= 1, '缺少服务器列表'
sv = d['servers'][0]
assert sv['name'] != 'e2e-node-01', f"show_name=false 时不应暴露真实名: {sv['name']}"
assert sv['region_country'] == '中国香港', sv
assert sv['provider_name'] == '示例云', sv
assert sv['traffic_limit'] == 1099511627776, sv
assert 'period_start' in sv and 'period_end' in sv, sv
assert sv['period_start'].endswith('-05'), f"计费起始日应为 5 号: {sv['period_start']}"
PY

# show_name=true 时应展示真实名
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PUT "$BASE/api/admin/probe/settings" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true,"title":"服务状态监控","show_name":true,"show_globe":true,"require_token":false,"refresh_seconds":5}'
curl -s -o "$WORK/probe2.json" "$BASE/api/public/probe-servers"
grep -q '"name":"e2e-node-01"' "$WORK/probe2.json" && ok "show_name=true 时展示真实服务器名" || bad "show_name=true 未生效"

# 令牌保护
curl -s -o "$WORK/probe-token.json" -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/api/admin/probe/token"
PROBE_TOKEN="$(cat "$WORK/probe-token.json" | jget token 2>/dev/null || echo '')"
[[ -n "$PROBE_TOKEN" ]] && ok "探针令牌已签发" || bad "签发探针令牌失败"

NO_TOKEN_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/public/probe-servers")"
[[ "$NO_TOKEN_CODE" == "404" ]] && ok "开启令牌保护后无令牌访问被拒（404）" || bad "无令牌竟然可访问: $NO_TOKEN_CODE"

WITH_TOKEN_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Mmwx-Probe-Token: $PROBE_TOKEN" "$BASE/api/public/probe-servers")"
[[ "$WITH_TOKEN_CODE" == "200" ]] && ok "携带正确令牌可访问（200）" || bad "正确令牌被拒: $WITH_TOKEN_CODE"

WRONG_TOKEN_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Mmwx-Probe-Token: wrong" "$BASE/api/public/probe-servers")"
[[ "$WRONG_TOKEN_CODE" == "404" ]] && ok "错误令牌被拒（404）" || bad "错误令牌竟然通过: $WRONG_TOKEN_CODE"

# 历史序列
SYS_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Mmwx-Probe-Token: $PROBE_TOKEN" \
  "$BASE/api/public/probe-series?kind=system&server_id=$SERVER_ID")"
[[ "$SYS_CODE" == "200" ]] && ok "kind=system 序列可查" || bad "system 序列 HTTP $SYS_CODE"

NOID_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Mmwx-Probe-Token: $PROBE_TOKEN" \
  "$BASE/api/public/probe-series?kind=system")"
[[ "$NOID_CODE" == "400" ]] && ok "kind=system 缺少 server_id 时明确报 400" || bad "缺少参数未校验: $NOID_CODE"

TRAF_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Mmwx-Probe-Token: $PROBE_TOKEN" \
  "$BASE/api/public/probe-series?kind=traffic")"
[[ "$TRAF_CODE" == "200" ]] && ok "kind=traffic 序列可查" || bad "traffic 序列 HTTP $TRAF_CODE"

# 未实现的 kind 必须明确报错，而不是返回空数组（空数组会让排障方向完全错）
LAT_CODE="$(curl -s -o "$WORK/lat.json" -w '%{http_code}' -H "X-Mmwx-Probe-Token: $PROBE_TOKEN" \
  "$BASE/api/public/probe-series?kind=latency")"
if [[ "$LAT_CODE" == "501" ]] && grep -q "尚未实现" "$WORK/lat.json"; then
  ok "kind=latency 明确报未实现（501）而不是返回空数组"
else
  bad "latency 应返回 501 且说明原因，实得 $LAT_CODE"
fi

# 关闭探针
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PUT "$BASE/api/admin/probe/settings" \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
CLOSED2="$(curl -s -o /dev/null -w '%{http_code}' -H "X-Mmwx-Probe-Token: $PROBE_TOKEN" "$BASE/api/public/probe-servers")"
[[ "$CLOSED2" == "404" ]] && ok "关闭后可访问性立即收回" || bad "关闭后仍可访问: $CLOSED2"

head_ "7d4. 公告 / 通知 / 备份恢复"
# ---- 公告 ----
ANN_CODE="$(curl -s -o "$WORK/ann.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/announcements" -H 'Content-Type: application/json' \
  -d '{"title":"维护通知","content":"今晚 02:00 例行维护","level":"warning","pinned":true}')"
ANN_ID="$(cat "$WORK/ann.json" | jget announcement.id 2>/dev/null || echo '')"
[[ "$ANN_CODE" == "201" && -n "$ANN_ID" ]] && ok "公告已创建" || bad "创建公告失败 HTTP $ANN_CODE: $(head -c 200 "$WORK/ann.json")"

PUB_CODE="$(curl -s -o "$WORK/ann-pub.json" -w '%{http_code}' "$BASE/api/public/announcements")"
[[ "$PUB_CODE" == "200" ]] && ok "公开公告接口 200" || bad "公开公告 HTTP $PUB_CODE"
grep -q '维护通知' "$WORK/ann-pub.json" && ok "公开公告含刚创建的条目" || bad "公开公告未包含新条目"

# 非法 level 必须被拒
BAD_LVL="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/announcements" -H 'Content-Type: application/json' \
  -d '{"title":"x","level":"critical"}')"
[[ "$BAD_LVL" == "400" ]] && ok "非法公告级别被拒（400）" || bad "非法级别未校验: $BAD_LVL"

# 停用后公开接口不应再出现
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PATCH "$BASE/api/admin/announcements/$ANN_ID" \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
curl -s -o "$WORK/ann-pub2.json" "$BASE/api/public/announcements"
grep -q '维护通知' "$WORK/ann-pub2.json" && bad "停用后仍对公开展示" || ok "停用后立即从公开接口消失"

# ---- 通知 ----
NS_CODE="$(curl -s -o "$WORK/notify.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/notify/settings")"
[[ "$NS_CODE" == "200" ]] && ok "通知设置可读" || bad "通知设置 HTTP $NS_CODE"
grep -q '"enabled":false' "$WORK/notify.json" && ok "通知默认全关闭" || bad "通知默认应为关闭"

PUT_N="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X PUT "$BASE/api/admin/notify/settings" -H 'Content-Type: application/json' \
  -d '{"telegram":{"enabled":false},"webhook":{"enabled":false,"url":"https://example.com/hook","secret":"s3cr3t"},"dedup_window_seconds":120}')"
[[ "$PUT_N" == "200" ]] && ok "通知设置已保存" || bad "保存通知设置失败 HTTP $PUT_N"

# 敏感字段不回显
curl -s -o "$WORK/notify2.json" -b "$COOKIE" "$BASE/api/admin/notify/settings"
grep -q 's3cr3t' "$WORK/notify2.json" && bad "Webhook 密钥被回显（应隐藏）" || ok "Webhook 密钥不回显"
grep -q '"has_webhook_secret":true' "$WORK/notify2.json" && ok "以 has_* 字段告知已配置" || bad "缺少 has_webhook_secret 标记"

# 未启用任何通道时测试通知应明确失败
TEST_N="$(curl -s -o "$WORK/notify-test.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/notify/test")"
[[ "$TEST_N" == "502" ]] && ok "未启用通道时测试通知明确失败（502）" || bad "测试通知应失败，实得 $TEST_N"

# ---- 备份 / 恢复 ----
BK_CODE="$(curl -s -o "$WORK/backup.zip" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/backup/download")"
[[ "$BK_CODE" == "200" ]] && ok "备份下载 200" || bad "备份下载 HTTP $BK_CODE"
python3 - "$WORK/backup.zip" <<'PY' && ok "备份包结构合法（含 manifest 与 SQLite 数据库）" || bad "备份包结构异常"
import sys, zipfile, json
z = zipfile.ZipFile(sys.argv[1])
names = z.namelist()
assert 'manifest.json' in names, names
assert 'mmx.db' in names, names
m = json.loads(z.read('manifest.json'))
assert m['app'] == 'mmx', m
assert z.read('mmx.db')[:15] == b'SQLite format 3', '数据库魔数不对'
assert m['tables'].get('users', 0) >= 1, f"表计数异常: {m.get('tables')}"
PY

# 伪造备份包必须被拒
echo "not a zip" > "$WORK/bad-backup.zip"
BAD_BK="$(curl -s -o "$WORK/bad-backup-resp.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/backup/restore" --data-binary "@$WORK/bad-backup.zip" \
  -H 'Content-Type: application/octet-stream')"
[[ "$BAD_BK" == "400" ]] && ok "非 zip 备份包被拒（400）" || bad "伪造备份包未被拒: $BAD_BK"

# 真实的备份包可以恢复（落成待应用文件，要求重启）
RST_CODE="$(curl -s -o "$WORK/restore.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/backup/restore" --data-binary "@$WORK/backup.zip" \
  -H 'Content-Type: application/octet-stream')"
[[ "$RST_CODE" == "202" ]] && ok "合法备份包被接受（202，要求重启生效）" || bad "恢复失败 HTTP $RST_CODE: $(head -c 200 "$WORK/restore.json")"
grep -q '"restart_required":true' "$WORK/restore.json" && ok "明确告知需要重启" || bad "未告知需要重启"

head_ "7d5. 订阅模板 / 规则集 / 订阅文件"
TPL_CODE="$(curl -s -o "$WORK/tpl.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/templates")"
TPL_N="$(grep -o '"builtin":true' "$WORK/tpl.json" | wc -l | tr -d ' ')"
[[ "$TPL_CODE" == "200" && "$TPL_N" -ge 3 ]] && ok "内置模板已写入（$TPL_N 个）" || bad "内置模板缺失 HTTP $TPL_CODE n=$TPL_N"
grep -q '{{PROXIES}}' "$WORK/tpl.json" && ok "接口暴露占位符说明" || bad "缺少占位符说明"

# 从内置模板复制一个
CP_CODE="$(curl -s -o "$WORK/tpl-copy.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/templates" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-custom","copy_from":"simple","description":"端到端复制"}')"
CPL_ID="$(cat "$WORK/tpl-copy.json" | jget template.id 2>/dev/null || echo '')"
[[ "$CP_CODE" == "201" && -n "$CPL_ID" ]] && ok "从内置模板复制成功" || bad "复制模板失败 HTTP $CP_CODE: $(head -c 200 "$WORK/tpl-copy.json")"

# 缺少占位符的模板必须被拒（否则渲染出空配置，用户在客户端才发现）
BADTPL_CODE="$(curl -s -o "$WORK/tpl-bad.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/templates" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-bad","content":"proxies: []\nproxy-groups: []\nrules: []\n"}')"
[[ "$BADTPL_CODE" == "400" ]] && ok "缺少节点占位符的模板被拒（400）" || bad "无效模板未被拒: $BADTPL_CODE"

# 非法 YAML 必须被拒
BADYAML_CODE="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/templates" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-badyaml","content":"proxies:\n{{PROXIES}}\n  \t- broken: [\n"}')"
[[ "$BADYAML_CODE" == "400" ]] && ok "非法 YAML 模板被拒（400）" || bad "非法 YAML 未被拒: $BADYAML_CODE"

# 内置模板不可删除
BUILTIN_ID="$(python3 -c "
import json,sys
d=json.load(open('$WORK/tpl.json'))
for t in d['templates']:
    if t['name']=='simple': print(t['id']); break" 2>/dev/null || echo '')"
DELB_CODE="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X DELETE "$BASE/api/admin/templates/$BUILTIN_ID")"
[[ "$DELB_CODE" == "400" ]] && ok "内置模板不可删除（400）" || bad "内置模板被删了: $DELB_CODE"

# 预览：渲染结果必须是合法 YAML 且含节点
curl -s -o "$WORK/tpl-preview.yaml" -b "$COOKIE" "$BASE/api/admin/templates/$CPL_ID/preview"
python3 - "$WORK/tpl-preview.yaml" <<'PY' && ok "模板预览产出合法 Clash 配置" || bad "模板预览异常"
import sys
try:
    import yaml
except ImportError:
    # 没有 pyyaml 时退化为结构检查
    txt = open(sys.argv[1]).read()
    assert 'proxies:' in txt and 'proxy-groups:' in txt and 'rules:' in txt, txt[:200]
    raise SystemExit(0)
d = yaml.safe_load(open(sys.argv[1]))
assert isinstance(d.get('proxies'), list) and d['proxies'], '缺少 proxies'
assert isinstance(d.get('proxy-groups'), list), '缺少 proxy-groups'
assert isinstance(d.get('rules'), list), '缺少 rules'
names = [p['name'] for p in d['proxies']]
assert names, '代理列表为空'
PY
grep -q '预览节点' "$WORK/tpl-preview.yaml" && ok "无节点时用占位节点保证结构可验证" || bad "预览未使用占位节点"

# 把模板绑到用户，订阅应改用该模板
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PUT "$BASE/api/admin/users/$USER_ID/template" \
  -H 'Content-Type: application/json' -d "{\"template_id\":\"$CPL_ID\"}"
curl -s -o "$WORK/sub-tpl.yaml" -b "$COOKIE" "$BASE/sub/$SUB_TOKEN?format=clash"
grep -q '香港-01' "$WORK/sub-tpl.yaml" && ok "绑定模板后订阅仍含真实节点" || bad "模板订阅缺少节点"

# 模板被删后订阅应回落内置渲染，而不是 500
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X DELETE "$BASE/api/admin/templates/$CPL_ID"
FALLBACK_CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/sub/$SUB_TOKEN?format=clash")"
[[ "$FALLBACK_CODE" == "200" ]] && ok "模板被删后订阅回落内置渲染（不 500）" || bad "模板删除后订阅失败: $FALLBACK_CODE"

# ---- 规则集 ----
RP_CODE="$(curl -s -o "$WORK/rp.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/rule-providers" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-ads","type":"http","behavior":"domain","format":"yaml","url":"https://example.com/ads.yaml","interval":86400}')"
RP_ID="$(cat "$WORK/rp.json" | jget provider.id 2>/dev/null || echo '')"
[[ "$RP_CODE" == "201" && -n "$RP_ID" ]] && ok "规则集已创建" || bad "创建规则集失败 HTTP $RP_CODE"

NORP_CODE="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/rule-providers" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-nonurl","type":"http"}')"
[[ "$NORP_CODE" == "400" ]] && ok "type=http 缺 URL 被拒（400）" || bad "缺少 URL 未校验: $NORP_CODE"

RP_LIST="$(curl -s -b "$COOKIE" "$BASE/api/admin/rule-providers")"
echo "$RP_LIST" | grep -q 'e2e-ads' && ok "规则集列表可查" || bad "规则集列表异常"
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X DELETE "$BASE/api/admin/rule-providers/$RP_ID"

# ---- 订阅文件 ----
SF_CODE="$(curl -s -o "$WORK/sf.json" -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/subscribe-files" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-file.yaml","content":"proxies: []\n"}')"
SF_ID="$(cat "$WORK/sf.json" | jget file.id 2>/dev/null || echo '')"
[[ "$SF_CODE" == "201" && -n "$SF_ID" ]] && ok "订阅文件已创建" || bad "创建订阅文件失败 HTTP $SF_CODE"

EMPTY_SF="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/subscribe-files" -H 'Content-Type: application/json' -d '{"name":"x","content":""}')"
[[ "$EMPTY_SF" == "400" ]] && ok "空内容订阅文件被拒（400）" || bad "空内容未校验: $EMPTY_SF"

# 列表接口不应把内容一起返回（避免列表响应过大）
SF_LIST="$(curl -s -b "$COOKIE" "$BASE/api/admin/subscribe-files")"
echo "$SF_LIST" | grep -q 'proxies' && bad "订阅文件列表不应携带正文" || ok "订阅文件列表不携带正文"
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X DELETE "$BASE/api/admin/subscribe-files/$SF_ID"

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

head_ "7f. 流量统计与配额"
TR_CODE="$(curl -s -o "$WORK/traffic.json" -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/traffic?days=30")"
[[ "$TR_CODE" == "200" ]] && ok "全局流量汇总 200" || bad "流量汇总 HTTP $TR_CODE"
python3 - "$WORK/traffic.json" <<'PY' && ok "汇总结构正确（days/series/总数）" || bad "汇总结构异常"
import json, sys
d = json.load(open(sys.argv[1]))
assert d['days'] == 30, d.get('days')
s = d['summary']
assert 'series' in s and isinstance(s['series'], list)
assert s['total'] == s['up'] + s['down']
PY

SRV_TR="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" "$BASE/api/admin/traffic?server_id=$SERVER_ID&days=7")"
[[ "$SRV_TR" == "200" ]] && ok "单服务器流量汇总 200" || bad "单服务器汇总 HTTP $SRV_TR"

# 配额判定：设一个上限并观察淘汰逻辑
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PATCH "$BASE/api/admin/users/$USER_ID" \
  -H 'Content-Type: application/json' -d '{"traffic_limit":1000,"traffic_used":400}'
UQ="$(curl -s -b "$COOKIE" "$BASE/api/admin/users/$USER_ID/traffic")"
python3 - "$UQ" <<'PY' && ok "配额计算正确（1000 上限 / 400 已用 → 剩 600）" || bad "配额计算错误"
import json, sys
d = json.loads(sys.argv[1])
q = d['quota']
assert q['traffic_limit'] == 1000, q
assert q['traffic_used'] == 400, q
assert q['remaining'] == 600, q
assert q['exhausted'] is False, q
assert q['unlimited'] is False, q
assert 'series' in d['summary']
PY

# 超额
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PATCH "$BASE/api/admin/users/$USER_ID" \
  -H 'Content-Type: application/json' -d '{"traffic_used":1500}'
UQ2="$(curl -s -b "$COOKIE" "$BASE/api/admin/users/$USER_ID/traffic")"
python3 - "$UQ2" <<'PY' && ok "超额后标记 exhausted 且剩余归零" || bad "超额判定错误"
import json, sys
q = json.loads(sys.argv[1])['quota']
assert q['exhausted'] is True, q
assert q['remaining'] == 0, q
PY

# 不限量
curl -s -o /dev/null -b "$COOKIE" -H "X-CSRF-Token: $CSRF" -X PATCH "$BASE/api/admin/users/$USER_ID" \
  -H 'Content-Type: application/json' -d '{"traffic_limit":0}'
UQ3="$(curl -s -b "$COOKIE" "$BASE/api/admin/users/$USER_ID/traffic")"
python3 - "$UQ3" <<'PY' && ok "上限为 0 时视为不限量" || bad "不限量判定错误"
import json, sys
q = json.loads(sys.argv[1])['quota']
assert q['unlimited'] is True, q
assert q['remaining'] == -1, q
PY

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

head_ "11. 双因子（TOTP）与接口令牌"

# 用独立账号做双因子测试：管理员的 IP+用户名已被防爆破测试锁定
TOTP_USER="e2e-totp"
TOTP_PASS="E2eTotp@12345"
TCREATE="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/api/admin/users" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\",\"nickname\":\"双因子测试\"}")"
[[ "$TCREATE" == "201" ]] && ok "已创建双因子测试账号" || bad "创建测试账号失败 HTTP $TCREATE"

TJAR="$WORK/totp.jar"
TLOGIN="$(curl -s -o /dev/null -w '%{http_code}' -c "$TJAR" -X POST "$BASE/api/login" \
  -H 'Content-Type: application/json' -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\"}")"
[[ "$TLOGIN" == "200" ]] && ok "未启用双因子时可正常登录" || bad "测试账号登录失败 HTTP $TLOGIN"
TCSRF="$(awk '/mmx_csrf/ {print $7}' "$TJAR" | tail -1)"

# --- setup ---
curl -s -o "$WORK/totp-setup.json" -b "$TJAR" -H "X-CSRF-Token: $TCSRF" \
  -X POST "$BASE/api/me/totp/setup"
TOTP_SECRET="$(cat "$WORK/totp-setup.json" | jget secret 2>/dev/null || echo '')"
TOTP_URL="$(cat "$WORK/totp-setup.json" | jget otpauth_url 2>/dev/null || echo '')"
[[ -n "$TOTP_SECRET" ]] && ok "已生成 TOTP 密钥" || bad "TOTP setup 失败: $(head -c 200 "$WORK/totp-setup.json")"
[[ "$TOTP_URL" == otpauth://totp/* ]] && ok "返回 otpauth:// 扫码地址" || bad "otpauth URL 异常: $TOTP_URL"

# 尚未输入验证码，双因子不应生效
TSTATE="$(curl -s -b "$TJAR" "$BASE/api/me/totp" | jget enabled 2>/dev/null || echo '?')"
[[ "$TSTATE" == "False" ]] && ok "未验证前双因子不生效" || bad "未验证就生效了: $TSTATE"

totp_code() {
  python3 - "$1" <<'PY'
import base64, hashlib, hmac, struct, sys, time
secret = sys.argv[1].strip().upper()
pad = '=' * ((8 - len(secret) % 8) % 8)
key = base64.b32decode(secret + pad)
counter = int(time.time()) // 30
h = hmac.new(key, struct.pack('>Q', counter), hashlib.sha1).digest()
o = h[19] & 0x0F
code = (struct.unpack('>I', h[o:o+4])[0] & 0x7FFFFFFF) % 1000000
print(f'{code:06d}')
PY
}

CODE="$(totp_code "$TOTP_SECRET")"
[[ "$CODE" =~ ^[0-9]{6}$ ]] && ok "本地算出动态码 $CODE" || bad "动态码计算失败: $CODE"

# --- enable ---
E_CODE="$(curl -s -o "$WORK/totp-enable.json" -w '%{http_code}' -b "$TJAR" -H "X-CSRF-Token: $TCSRF" \
  -X POST "$BASE/api/me/totp/enable" -H 'Content-Type: application/json' -d "{\"code\":\"$CODE\"}")"
[[ "$E_CODE" == "200" ]] && ok "双因子启用成功" || bad "启用失败 HTTP $E_CODE: $(head -c 200 "$WORK/totp-enable.json")"
RECOVERY_N="$(cat "$WORK/totp-enable.json" | jget recovery_codes | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"
[[ "$RECOVERY_N" == "8" ]] && ok "已生成 8 个一次性恢复码" || bad "恢复码数量异常: $RECOVERY_N"
RECOVERY_1="$(cat "$WORK/totp-enable.json" | jget 'recovery_codes.0' 2>/dev/null || echo '')"

TSTATE2="$(curl -s -b "$TJAR" "$BASE/api/me/totp" | jget enabled 2>/dev/null || echo '?')"
[[ "$TSTATE2" == "True" ]] && ok "双因子状态已变为启用" || bad "状态未更新: $TSTATE2"

# --- 登录必须带第二因子 ---
N1="$(curl -s -o "$WORK/login-no2fa.json" -w '%{http_code}' -X POST "$BASE/api/login" \
  -H 'Content-Type: application/json' -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\"}")"
[[ "$N1" == "401" ]] && ok "只给口令不给第二因子被拒（401）" || bad "双因子未拦截: HTTP $N1"
grep -q "totp_required" "$WORK/login-no2fa.json" && ok "错误码为 totp_required（前端可据此提示）" || bad "错误码不明确"

N2="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\",\"totp_code\":\"000000\"}")"
[[ "$N2" == "401" ]] && ok "错误动态码被拒" || bad "错误动态码竟然放行: $N2"

CODE2="$(totp_code "$TOTP_SECRET")"
N3="$(curl -s -o /dev/null -w '%{http_code}' -c "$WORK/totp2.jar" -X POST "$BASE/api/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\",\"totp_code\":\"$CODE2\"}")"
[[ "$N3" == "200" ]] && ok "带正确动态码登录成功" || bad "正确动态码登录失败: $N3"

# --- 恢复码可用且一次性 ---
N4="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\",\"totp_code\":\"$RECOVERY_1\"}")"
[[ "$N4" == "200" ]] && ok "一次性恢复码可用于登录" || bad "恢复码登录失败: $N4"
N5="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\",\"totp_code\":\"$RECOVERY_1\"}")"
[[ "$N5" == "401" ]] && ok "同一恢复码不可重复使用" || bad "恢复码被重复使用: $N5"
LEFT="$(curl -s -b "$TJAR" "$BASE/api/me/totp" | jget recovery_codes_left 2>/dev/null || echo '?')"
[[ "$LEFT" == "7" ]] && ok "恢复码计数已扣减（剩 7）" || bad "恢复码计数异常: $LEFT"

# --- 关闭双因子 ---
D_CODE="$(curl -s -o /dev/null -w '%{http_code}' -b "$TJAR" -H "X-CSRF-Token: $TCSRF" \
  -X POST "$BASE/api/me/totp/disable" -H 'Content-Type: application/json' \
  -d "{\"code\":\"$(totp_code "$TOTP_SECRET")\"}")"
[[ "$D_CODE" == "200" ]] && ok "双因子已关闭" || bad "关闭失败 HTTP $D_CODE"
N6="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$TOTP_USER\",\"password\":\"$TOTP_PASS\"}")"
[[ "$N6" == "200" ]] && ok "关闭后可只用口令登录" || bad "关闭后仍需第二因子: $N6"

head_ "12. 接口令牌（API Token）"
TC="$(curl -s -o "$WORK/atk.json" -w '%{http_code}' -b "$TJAR" -H "X-CSRF-Token: $TCSRF" \
  -X POST "$BASE/api/me/tokens" -H 'Content-Type: application/json' -d '{"name":"e2e-cli"}')"
[[ "$TC" == "201" ]] && ok "接口令牌已创建" || bad "创建令牌失败 HTTP $TC: $(head -c 200 "$WORK/atk.json")"
API_TOKEN="$(cat "$WORK/atk.json" | jget token 2>/dev/null || echo '')"
ATK_ID="$(cat "$WORK/atk.json" | jget meta.id 2>/dev/null || echo '')"
[[ "$API_TOKEN" == mmx_* ]] && ok "令牌格式为 mmx_ 前缀" || bad "令牌格式异常: $API_TOKEN"

# 用令牌访问需要鉴权的接口
H_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $API_TOKEN" "$BASE/api/me")"
[[ "$H_CODE" == "200" ]] && ok "Bearer 令牌可访问 /api/me" || bad "令牌鉴权失败: $H_CODE"

# 令牌鉴权不走 CSRF：不带 X-CSRF-Token 的写操作应当成功
# （用普通用户也有权限的端点，避免与「管理员权限」这个正交检查混淆）
W_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $API_TOKEN" \
  -X POST "$BASE/api/me/tokens" -H 'Content-Type: application/json' -d '{"name":"via-token"}')"
[[ "$W_CODE" == "201" ]] && ok "接口令牌不受 CSRF 限制（无 cookie 即无跨站风险）" || bad "令牌写操作被拒: $W_CODE"

# 权限仍然生效：普通用户的令牌不能调管理员接口
A_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $API_TOKEN" \
  -X POST "$BASE/api/admin/servers" -H 'Content-Type: application/json' -d '{"name":"should-fail"}')"
[[ "$A_CODE" == "403" ]] && ok "令牌权限等同所属用户（普通用户令牌调管理员接口 403）" || bad "越权未被拦截: $A_CODE"

# 吊销后立即失效
curl -s -o /dev/null -b "$TJAR" -H "X-CSRF-Token: $TCSRF" -X DELETE "$BASE/api/me/tokens/$ATK_ID"
R_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $API_TOKEN" "$BASE/api/me")"
[[ "$R_CODE" == "401" ]] && ok "吊销后令牌立即失效（401）" || bad "吊销未生效: $R_CODE"

BAD_CODE2="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer mmx_not-a-real-token" "$BASE/api/me")"
[[ "$BAD_CODE2" == "401" ]] && ok "伪造令牌被拒（401）" || bad "伪造令牌竟然通过: $BAD_CODE2"

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

head_ "13. 登录防爆破（放在最后，避免 IP 级锁定影响其它登录用例）"
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
