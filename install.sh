#!/usr/bin/env bash
# =============================================================================
#  MMX 一键安装（Docker）
#
#  一条命令：
#     curl -fsSL https://raw.githubusercontent.com/Xioaruan912/MMW/main/install.sh | bash
#
#  它会：
#     1. 检查 / 自动安装 Docker
#     2. 优先拉取预构建镜像；拉不到就在本地从源码构建（只依赖 Docker）
#     3. 启动容器（自动重启 + 数据卷持久化）
#     4. 等健康检查通过后打印面板地址与管理命令
#
#  环境变量（都可选）：
#     MMX_PORT=12889              对外端口
#     MMX_DATA_DIR=/opt/mmx/data  数据目录（数据库与主控密钥，★ 唯一需要备份的）
#     MMX_PUBLIC_URL=             公网地址，用于生成 Agent 安装命令
#     MMX_TRUST_PROXY=1           部署在反向代理之后时打开
#     MMX_IMAGE=                  指定镜像（默认优先 ghcr.io，失败则本地构建）
#     MMX_YES=1                   非交互，全部用默认值
# =============================================================================
set -euo pipefail

REPO="${MMX_REPO:-Xioaruan912/MMW}"
GHCR_IMAGE="${MMX_IMAGE:-ghcr.io/$(echo "$REPO" | tr '[:upper:]' '[:lower:]'):latest}"
LOCAL_IMAGE="mmx:local"
CONTAINER="mmx"
PORT="${MMX_PORT:-12889}"
DATA_DIR="${MMX_DATA_DIR:-/opt/mmx/data}"
PUBLIC_URL="${MMX_PUBLIC_URL:-}"
TRUST_PROXY="${MMX_TRUST_PROXY:-0}"
AUTO="${MMX_YES:-0}"

c(){ printf '\033[%sm%s\033[0m\n' "$1" "$2"; }
ok(){   c 32 "  [✓] $1"; }
warn(){ c 33 "  [!] $1"; }
die(){  c 31 "  [✗] $1"; exit 1; }
step(){ echo; c 36 "▸ $1"; }

# ── 交互：提示一律走 stderr；无终端时直接用默认值 ──────────────────────────
is_tty(){ [ "$AUTO" = "1" ] && return 1; [ -t 0 ] && return 0; [ -t 2 ] && [ -c /dev/tty ] && return 0; return 1; }
ask(){
  local label="$1" default="$2" v=""
  if ! is_tty; then printf '%s' "$default"; return 0; fi
  { printf '%s\n' "$label"; printf '    默认 [%s]\n' "$default"; printf '    直接回车使用默认值: '; } >&2
  read -r -t 300 v </dev/tty 2>/dev/null || v=""
  v="$(printf '%s' "$v" | tr -d '\r' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  [ -z "$v" ] && v="$default"
  printf '%s' "$v"
}

cat <<'BANNER'

  ┌──────────────────────────────────────────────┐
  │   MMX · 自托管代理节点管理与订阅分发平台      │
  │   一键安装（Docker）                          │
  └──────────────────────────────────────────────┘

BANNER

# ── 1. Docker ───────────────────────────────────────────────────────────────
step "检查 Docker"

as_root(){
  if [ "$(id -u)" = "0" ]; then "$@"
  elif command -v sudo >/dev/null 2>&1; then sudo "$@"
  else return 1; fi
}
PM=""; PM_UPDATED=0
pm_detect(){
  local m
  for m in apt-get:apt apk:apk dnf:dnf yum:yum pacman:pacman zypper:zypper; do
    command -v "${m%%:*}" >/dev/null 2>&1 && { PM="${m##*:}"; return 0; }
  done
  return 1
}
pm_install(){
  [ -z "$PM" ] && { pm_detect || return 1; }
  case "$PM" in
    apt) [ "$PM_UPDATED" = 0 ] && { as_root apt-get update -qq >/dev/null 2>&1 || true; PM_UPDATED=1; }
         as_root apt-get install -y -qq "$@" ;;
    apk) as_root apk add --no-cache "$@" ;;
    dnf) as_root dnf install -y -q "$@" ;;
    yum) as_root yum install -y -q "$@" ;;
    pacman) as_root pacman -Sy --noconfirm --needed "$@" ;;
    zypper) as_root zypper -n install "$@" ;;
    *) return 1 ;;
  esac
}

if ! command -v docker >/dev/null 2>&1; then
  warn "未检测到 Docker，正在自动安装…"
  pm_detect || die "无法识别包管理器，请手动安装 Docker 后重跑：
    https://docs.docker.com/engine/install/"
  if [ "$PM" = "apt" ]; then
    # 官方脚本在 Debian/Ubuntu 上最省事，也自带 docker compose 插件
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL https://get.docker.com -o /tmp/get-docker.sh && as_root sh /tmp/get-docker.sh
      rm -f /tmp/get-docker.sh
    else
      pm_install docker.io || die "Docker 安装失败"
    fi
  else
    pm_install docker || die "Docker 安装失败"
  fi
  hash -r 2>/dev/null || true
  command -v docker >/dev/null 2>&1 || die "Docker 安装后仍不可用"
fi
ok "Docker 可用：$(docker --version 2>/dev/null | head -1)"

# 当前用户能否直接用 docker（还是必须 sudo）
DOCKER="docker"
if ! docker info >/dev/null 2>&1; then
  if as_root docker info >/dev/null 2>&1; then
    DOCKER="sudo docker"
    warn "当前用户不在 docker 组，后续命令会带 sudo"
    warn "（想免 sudo：sudo usermod -aG docker \$USER 后重新登录）"
  else
    if command -v systemctl >/dev/null 2>&1; then
      warn "Docker 守护进程未运行，尝试启动…"
      as_root systemctl enable --now docker >/dev/null 2>&1 || true
      sleep 2
    fi
    docker info >/dev/null 2>&1 || as_root docker info >/dev/null 2>&1 \
      || die "Docker 守护进程不可用，请先启动 docker 服务"
    docker info >/dev/null 2>&1 || DOCKER="sudo docker"
  fi
fi
ok "Docker 守护进程正常"

# ── 2. 询问参数 ─────────────────────────────────────────────────────────────
step "配置"
PORT="$(ask '面板端口' "$PORT")"
DATA_DIR="$(ask '数据目录（数据库与主控密钥，唯一需要备份的目录）' "$DATA_DIR")"
[ -n "$PUBLIC_URL" ] || PUBLIC_URL="$(ask '公网地址（留空则用 http://<本机IP>:端口；有域名请填 https://...）' "")"

case "$PORT" in ''|*[!0-9]*) die "端口必须是数字: $PORT" ;; esac
[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "端口越界: $PORT"

# ── 3. 取得镜像 ─────────────────────────────────────────────────────────────
step "取得镜像"

IMAGE=""
if [ -n "${MMX_IMAGE:-}" ]; then
  IMAGE="$MMX_IMAGE"
  echo "  使用指定镜像：$IMAGE"
  $DOCKER pull "$IMAGE" || die "拉取指定镜像失败：$IMAGE"
else
  echo "  尝试拉取预构建镜像：$GHCR_IMAGE"
  if $DOCKER pull "$GHCR_IMAGE" 2>/dev/null; then
    IMAGE="$GHCR_IMAGE"
    ok "已拉取预构建镜像"
  else
    warn "预构建镜像不可用，改为在本地从源码构建（首次约 2-5 分钟）"
    command -v git >/dev/null 2>&1 || {
      warn "缺少 git，正在自动安装…"
      pm_install git || die "无法安装 git，请手动安装后重跑"
    }
    BUILD_DIR="$(mktemp -d /tmp/mmx-build.XXXXXX)"
    trap 'rm -rf "$BUILD_DIR"' EXIT
    echo "  克隆源码到 $BUILD_DIR"
    git clone --depth 1 "https://github.com/$REPO.git" "$BUILD_DIR/mmx" \
      || die "克隆失败（网络问题？可设 MMX_IMAGE 直接指定镜像）"
    echo "  构建镜像 $LOCAL_IMAGE"
    $DOCKER build -t "$LOCAL_IMAGE" --build-arg VERSION=local "$BUILD_DIR/mmx" \
      || die "镜像构建失败"
    IMAGE="$LOCAL_IMAGE"
    ok "本地构建完成"
  fi
fi

# ── 4. 启动容器 ─────────────────────────────────────────────────────────────
step "启动服务"

mkdir -p "$DATA_DIR"
# 容器内以 uid 100 运行，数据目录必须可写
if [ "$(id -u)" = "0" ]; then
  chown -R 100:101 "$DATA_DIR" 2>/dev/null || true
else
  as_root chown -R 100:101 "$DATA_DIR" 2>/dev/null || true
fi

# 幂等：先移除旧容器（数据在宿主目录里，不会丢）
$DOCKER rm -f "$CONTAINER" >/dev/null 2>&1 || true

RUN_ARGS=(
  -d --name "$CONTAINER"
  --restart unless-stopped
  -p "$PORT:12889"
  -v "$DATA_DIR:/data"
  -e "TZ=${TZ:-Asia/Shanghai}"
  -e "MMX_TRUST_PROXY=$TRUST_PROXY"
)
[ -n "$PUBLIC_URL" ] && RUN_ARGS+=( -e "MMX_PUBLIC_URL=$PUBLIC_URL" )

$DOCKER run "${RUN_ARGS[@]}" "$IMAGE" >/dev/null || die "容器启动失败"

printf '  等待服务就绪'
READY=0
for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then READY=1; break; fi
  printf '.'
  sleep 1
done
echo
[ "$READY" = "1" ] || {
  warn "服务未在 40 秒内就绪，容器日志："
  $DOCKER logs --tail 30 "$CONTAINER" 2>&1 | sed 's/^/      /'
  die "启动失败"
}
ok "健康检查通过"

# 探测本机可访问的地址
HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -z "$HOST_IP" ] && HOST_IP="127.0.0.1"
ACCESS="http://$HOST_IP:$PORT/"
[ -n "$PUBLIC_URL" ] && ACCESS="$PUBLIC_URL/"

echo
c 36 "════════════════════════════════════════════════════════════"
c 32 " 安装完成"
c 36 "════════════════════════════════════════════════════════════"
cat <<EOF

  面板地址   : $ACCESS
               （首次打开是初始化向导，创建管理员账号）

  数据目录   : $DATA_DIR
               ★ 唯一需要备份的目录：数据库 + 主控密钥

  常用命令
     查看日志 : $DOCKER logs -f $CONTAINER
     重启     : $DOCKER restart $CONTAINER
     停止     : $DOCKER stop $CONTAINER
     启动     : $DOCKER start $CONTAINER
     升级     : 重跑本安装脚本即可（$DATA_DIR 不受影响）
     卸载     : $DOCKER rm -f $CONTAINER && rm -rf $DATA_DIR

  下一步：登录面板 → 服务管理 → 添加服务器
          面板会生成一条 Agent 一键安装命令，复制到节点机上执行即可。

EOF
if [ -z "$PUBLIC_URL" ]; then
  c 33 "  提示：面板生成的 Agent 安装命令里用的是上面这个地址。"
  c 33 "        若节点机在外网，请在路由器/防火墙放行 $PORT，或用域名 + 反代"
  c 33 "        并重跑脚本时填上「公网地址」。"
  echo
fi
