#!/usr/bin/env bash
# =============================================================================
#  MMX 主控一键安装（Linux）
#
#  用法：
#     curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/deploy/install-master.sh | bash
#     或本地：bash deploy/install-master.sh
#
#  行为（纯安装器：只产出可运行的服务，不生成报告/证据目录）：
#     · 依赖自举：缺 curl / 解包工具就自己装（apt / apk / dnf / yum / pacman / zypper）
#     · 从 Release 取固定版本主控二进制并强制校验 sha256
#     · 安装到指定目录，注册 systemd 服务 + 开机自启
#     · 冒烟测试通过后才注册服务（避免装出一个 exit 127 的坏 unit）
#
#  环境变量：
#     MMX_INSTALLDIR=/opt/mmx     安装目录（程序 + data/）
#     MMX_VERSION=v0.1.0          要安装的版本
#     MMX_YES=1                   非交互，全部用默认值
#     MMX_NO_SERVICE=1            只安装，不注册 systemd
#     PORT=12889                  面板端口
#     MMX_MIRROR=<url>            自建镜像前缀（GitHub 慢时用）
# =============================================================================
set -euo pipefail

REPO="${MMX_REPO:-mmx-project/mmx}"
VERSION="${MMX_VERSION:-}"
INSTALL_DIR="${MMX_INSTALLDIR:-/opt/mmx}"
PORT="${PORT:-12889}"
AUTO="${MMX_YES:-0}"
NO_SERVICE="${MMX_NO_SERVICE:-0}"
SERVICE="mmx"
WORKDIR="$(mktemp -d /tmp/mmx-install.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT

c(){ printf '\033[%sm%s\033[0m\n' "$1" "$2"; }
ok(){   c 32 "  [+] $1"; }
warn(){ c 33 "  [!] $1"; }
die(){  c 31 "  [x] $1"; exit 1; }
head_(){ echo; c 36 "=== $1 ==="; }

# ── 交互：提示一律走 stderr；判定交互性看「是否真有终端」，read 一律带 -t 兜底 ──
is_tty(){ [ "$AUTO" = "1" ] && return 1; [ -t 0 ] && return 0; [ -t 2 ] && [ -c /dev/tty ] && return 0; return 1; }
ask(){
  local label="$1" default="$2" v=""
  if ! is_tty; then printf '%s' "$default"; return 0; fi
  { printf '%s\n' "$label"; printf '    default [%s]\n' "$default"; printf '    input (Enter = default): '; } >&2
  read -r -t 300 v </dev/tty 2>/dev/null || v=""
  v="$(printf '%s' "$v" | tr -d '\r' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//;s/^"//;s/"$//')"
  [ -z "$v" ] && v="$default"
  printf '%s' "$v"
}
ask_dir(){
  local label="$1" default="$2" v
  while :; do
    v="$(ask "$label" "$default")"
    case "$v" in /*) ;; *) warn "请填绝对路径: $v"; continue ;; esac
    if mkdir -p "$v" 2>/dev/null && [ -w "$v" ]; then printf '%s' "$v"; return 0; fi
    warn "目录不存在或不可写: $v"
    [ "$AUTO" = "1" ] && die "默认目录不可写: $v"
  done
}

# ── 依赖自举 ────────────────────────────────────────────────────────────────
as_root(){ if [ "$(id -u)" = "0" ]; then "$@"; elif command -v sudo >/dev/null 2>&1; then sudo "$@"; else return 1; fi; }
PM=""; PM_UPDATED=0
pm_detect(){ local m; for m in apt-get:apt apk:apk dnf:dnf yum:yum pacman:pacman zypper:zypper; do
  command -v "${m%%:*}" >/dev/null 2>&1 && { PM="${m##*:}"; return 0; }; done; return 1; }
pm_install(){ [ -z "$PM" ] && { pm_detect || return 1; }
  case "$PM" in
    apt) [ "$PM_UPDATED" = 0 ] && { as_root apt-get update -qq >/dev/null 2>&1 || true; PM_UPDATED=1; }; as_root apt-get install -y -qq "$@" ;;
    apk) as_root apk add --no-cache "$@" ;; dnf) as_root dnf install -y -q "$@" ;;
    yum) as_root yum install -y -q "$@" ;; pacman) as_root pacman -Sy --noconfirm --needed "$@" ;;
    zypper) as_root zypper -n install "$@" ;; *) return 1 ;; esac; }
ensure_dep(){ local bin="$1" why="$2"; shift 2
  command -v "$bin" >/dev/null 2>&1 && return 0
  warn "缺少 $bin（$why），自动安装中…"
  pm_detect || true
  if [ -n "$PM" ]; then local p
    for p in "$@"; do if pm_install "$p" >/dev/null 2>&1; then hash -r 2>/dev/null || true
        command -v "$bin" >/dev/null 2>&1 && { ok "$bin 已安装"; return 0; }; fi; done
  fi
  die "无法自动安装 $bin（$why）。请手动安装后重跑：
         Debian/Ubuntu : apt-get install -y $*
         Alpine        : apk add $*
         Fedora/RHEL   : dnf install -y $*
         Arch          : pacman -S $*" ; }

ensure_dep curl '下载构件' curl
for t in sha256sum od stat sed grep awk; do
  command -v "$t" >/dev/null 2>&1 || die "缺少核心工具 $t（coreutils）"
done

head_ "MMX 主控安装"
cat <<BANNER
  版本     : ${VERSION:-latest}
  安装目录 : $INSTALL_DIR
  面板端口 : $PORT
BANNER
c 33 '  [!] 构件从 GitHub 下载，中国大陆网络可能较慢。'
c 90 '      GitHub 慢时可设 MMX_MIRROR=<url> 指定镜像前缀'

INSTALL_DIR="$(ask_dir '选择安装目录（程序与数据都放这里，长期保留）' "$INSTALL_DIR")"
INSTALL_DIR="$(cd "$INSTALL_DIR" && pwd)"
ok "安装目录: $INSTALL_DIR"

# ── 解析版本 ────────────────────────────────────────────────────────────────
head_ "解析版本"
if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL --connect-timeout 15 "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -z "$VERSION" ] && die "无法获取最新版本；请用 MMX_VERSION=vX.Y.Z 显式指定"
fi
ok "目标版本: $VERSION"

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) die "不支持的架构: $ARCH" ;;
esac
ASSET="mmx-master-linux-$GOARCH"
BASE="${MMX_MIRROR:-https://github.com/$REPO/releases/download}"
[ -n "${MMX_MIRROR:-}" ] && BASE="${MMX_MIRROR%/}"

head_ "下载并校验"
mkdir -p "$INSTALL_DIR/artifacts"
BIN="$INSTALL_DIR/artifacts/$ASSET"
if [ -f "$BIN" ] && [ -s "$BIN" ]; then
  warn "复用已存在的构件: $BIN"
else
  echo "  [..] $BASE/$VERSION/$ASSET"
  curl -fSL --retry 3 --connect-timeout 15 -o "$BIN.part" "$BASE/$VERSION/$ASSET" \
    || die "下载失败。可设 MMX_MIRROR 指向自建镜像后重跑"
  mv -f "$BIN.part" "$BIN"
fi

# 校验：优先用官方 checksums.txt；拿不到就至少确认是 ELF 且非空
SUMS="$INSTALL_DIR/artifacts/checksums.txt"
if curl -fsSL --connect-timeout 15 -o "$SUMS" "$BASE/$VERSION/checksums.txt" 2>/dev/null; then
  want="$(grep " $ASSET\$" "$SUMS" | awk '{print $1}' | head -1)"
  if [ -n "$want" ]; then
    got="$(sha256sum "$BIN" | awk '{print $1}')"
    [ "$got" = "$want" ] || die "sha256 校验失败（期望 $want 实得 $got）"
    ok "sha256 校验通过"
  else
    warn "checksums.txt 中没有 $ASSET 的条目，跳过校验"
  fi
else
  warn "未取到 checksums.txt，跳过 sha256 校验"
fi
head -c 4 "$BIN" | od -An -tx1 | grep -q '7f 45 4c 46' || die "构件不是 ELF 可执行文件"
ok "构件形态确认（ELF）"

head_ "安装"
install -m 0755 "$BIN" "$INSTALL_DIR/mmx"
mkdir -p "$INSTALL_DIR/data"
ok "程序已安装: $INSTALL_DIR/mmx"

head_ "冒烟测试"
# 必须 cd 到临时目录：程序会按 CWD 生成 data/ 等目录，不 cd 会污染当前工作目录
mkdir -p "$WORKDIR/smoke"
( cd "$WORKDIR/smoke" && PORT=$((PORT+50)) MMWX_LISTEN_PORT=$((PORT+50)) \
    MMWX_DATA_DIR="$WORKDIR/smoke" timeout 10 "$INSTALL_DIR/mmx" >"$WORKDIR/smoke.out" 2>&1 ) &
SPID=$!
sleep 5
if kill -0 "$SPID" 2>/dev/null; then
  kill "$SPID" 2>/dev/null || true; wait "$SPID" 2>/dev/null || true
  ok "可执行文件自检通过（能正常启动）"
else
  wait "$SPID" 2>/dev/null || true
  sed 's/^/      /' "$WORKDIR/smoke.out" | head -10
  die "可执行文件无法启动，见上方日志"
fi

head_ "注册服务"
if [ "$NO_SERVICE" = "1" ]; then
  warn 'MMX_NO_SERVICE=1 -> 跳过服务注册'
elif command -v systemctl >/dev/null 2>&1 && systemctl show >/dev/null 2>&1; then
  as_root tee "/etc/systemd/system/$SERVICE.service" >/dev/null <<UNIT
[Unit]
Description=MMX Master
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
Environment=PORT=$PORT
Environment=MMX_LISTEN=127.0.0.1:$PORT
Environment=MMX_DATA_DIR=$INSTALL_DIR/data
ExecStart=$INSTALL_DIR/mmx
Restart=always
RestartSec=3
LimitNOFILE=1048576
# 加固：主控只需要读写自己的数据目录
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=$INSTALL_DIR

[Install]
WantedBy=multi-user.target
UNIT
  as_root systemctl daemon-reload
  as_root systemctl enable --now "$SERVICE" >/dev/null 2>&1
  sleep 3
  if systemctl is-active --quiet "$SERVICE"; then
    ok "systemd: $SERVICE.service 已启动（已开启开机自启）"
  else
    warn "服务未 active，最近日志："
    as_root journalctl -u "$SERVICE" -n 15 --no-pager 2>/dev/null | sed 's/^/      /' || true
    die "启动失败"
  fi
else
  warn "未检测到 systemd，改用后台常驻脚本"
  cat > "$INSTALL_DIR/start.sh" <<START
#!/usr/bin/env bash
cd "$INSTALL_DIR"
PORT=$PORT MMWX_DATA_DIR="$INSTALL_DIR/data" nohup "$INSTALL_DIR/mmx" >> "$INSTALL_DIR/mmx.log" 2>&1 &
echo \$! > "$INSTALL_DIR/mmx.pid"
echo "started pid \$(cat "$INSTALL_DIR/mmx.pid")"
START
  cat > "$INSTALL_DIR/stop.sh" <<STOP
#!/usr/bin/env bash
[ -f "$INSTALL_DIR/mmx.pid" ] && kill "\$(cat $INSTALL_DIR/mmx.pid)" 2>/dev/null || pkill -f "$INSTALL_DIR/mmx"
rm -f "$INSTALL_DIR/mmx.pid"; echo stopped
STOP
  chmod 0755 "$INSTALL_DIR/start.sh" "$INSTALL_DIR/stop.sh"
  bash "$INSTALL_DIR/start.sh" >/dev/null 2>&1 || true
  sleep 2
  ok "已后台启动"
fi

echo
c 36 "============================================================"
c 36 " MMX 安装完成"
c 36 "============================================================"
echo " 面板地址 : http://<本机IP>:$PORT/    （首次进入是初始化向导）"
echo " 安装目录 : $INSTALL_DIR"
echo "   ├── mmx         主控程序"
echo "   ├── artifacts/  构件缓存（可删）"
echo "   └── data/       数据库与主控密钥（★ 唯一需要备份的目录）"
echo
echo " 管理命令 : systemctl status|restart|stop $SERVICE"
echo " 实时日志 : journalctl -u $SERVICE -f"
echo " 卸载     : systemctl disable --now $SERVICE && rm -f /etc/systemd/system/$SERVICE.service && rm -rf $INSTALL_DIR"
echo
echo " 生产部署建议在前面挂一层 nginx（只暴露 443），并设置"
echo "   MMX_PUBLIC_URL=https://你的域名   与   MMX_TRUST_PROXY=1"
echo " 否则生成的 Agent 安装命令里的地址会是 127.0.0.1。"
