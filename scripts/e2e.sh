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
xray_mode: "none"
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
