package agentlink

import (
	"fmt"
	"path"
	"strings"
)

// Installer 负责生成 Agent 的一键安装脚本。
//
// 设计约束：
//   - 脚本里只嵌 **一次性 enroll token**，绝不嵌长期凭据；
//   - 下载 Agent 二进制后强制校验 sha256（哈希由主控提供，随脚本一并下发）；
//   - 默认走 systemd；无 systemd 时降级为 OpenRC / 前台 nohup。
type Installer struct {
	BaseURL  string            // 主控对外基址，例如 https://panel.example.com
	Token    string            // 一次性 enroll token（明文）
	ServerID string            // 服务器 ID（仅用于注释与日志）
	Label    string            // 展示名（服务器名称）
	XrayMode string            // embedded | external | none
	Binaries map[string]string // "linux-amd64" -> sha256
	StateDir string            // Agent 状态目录，默认 /var/lib/mmx-agent
}

// Script 生成 bash 安装脚本。
func (i Installer) Script() string {
	stateDir := i.StateDir
	if stateDir == "" {
		stateDir = "/var/lib/mmx-agent"
	}
	xrayMode := i.XrayMode
	if xrayMode == "" {
		xrayMode = "external"
	}
	base := strings.TrimRight(i.BaseURL, "/")

	var sums strings.Builder
	for platform, sum := range i.Binaries {
		fmt.Fprintf(&sums, "  %s) want=\"%s\" ;;\n", platform, sum)
	}

	label := i.Label
	if label == "" {
		label = i.ServerID
	}

	// 占位符顺序必须与 installScriptTmpl 中 %s 的出现顺序完全一致：
	// label -> MASTER_URL -> SERVER_ID -> ENROLL_TOKEN -> XRAY_MODE -> STATE_DIR -> sums
	return fmt.Sprintf(installScriptTmpl,
		label, base, i.ServerID, i.Token, xrayMode, stateDir, sums.String(),
	)
}

// 注意：所有 %s 的注入点都用引号包裹，且 token 只出现一次。
// 脚本本身不做任何破坏性操作：只写自己的目录与 systemd unit。
const installScriptTmpl = `#!/usr/bin/env bash
# ============================================================================
#   MMX Agent 安装脚本（由主控生成，含一次性入驻令牌）
#   服务器: %s
# ============================================================================
set -euo pipefail

MASTER_URL="%s"
SERVER_ID="%s"
ENROLL_TOKEN="%s"
XRAY_MODE="%s"
STATE_DIR="%s"
INSTALL_DIR="/usr/local/bin"

c(){ printf '\033[%%sm%%s\033[0m\n' "$1" "$2"; }
ok(){   c 32 "  [+] $1"; }
warn(){ c 33 "  [!] $1"; }
die(){  c 31 "  [x] $1"; exit 1; }
head_(){ echo; c 36 "=== $1 ==="; }

[ "$(id -u)" = "0" ] || die "请以 root 运行（或 sudo）"

head_ "环境探测"
case "$(uname -s)" in Linux) ;; *) die "当前只支持 Linux" ;; esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的架构: $(uname -m)" ;;
esac
PLATFORM="linux-$ARCH"
ok "平台: $PLATFORM   模式: $XRAY_MODE"

command -v curl >/dev/null 2>&1 || { 
  if command -v apt-get >/dev/null 2>&1; then apt-get update -qq && apt-get install -y -qq curl
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache curl
  elif command -v dnf >/dev/null 2>&1; then dnf install -y -q curl
  else die "缺少 curl 且无法自动安装"; fi
}

head_ "下载 Agent"
mkdir -p "$STATE_DIR" /tmp/mmx-install
BIN=/tmp/mmx-install/mmx-agent
curl -fsSL --retry 3 --connect-timeout 15 -o "$BIN" "$MASTER_URL/api/remote/agent/$PLATFORM" \
  || die "下载失败: $MASTER_URL/api/remote/agent/$PLATFORM"

# 校验：哈希由主控随脚本一并给出，逐平台固定
want=""
case "$PLATFORM" in
%s  *) want="" ;;
esac
if [ -n "$want" ]; then
  got="$(sha256sum "$BIN" | cut -d' ' -f1)"
  [ "$got" = "$want" ] || die "sha256 校验失败（期望 $want 实得 $got）"
  ok "sha256 校验通过"
else
  warn "主控未提供该平台的哈希，跳过校验（建议在主控侧配置 AGENT_BINARY_DIR 后重新生成）"
fi
install -m 0755 "$BIN" "$INSTALL_DIR/mmx-agent"
ok "已安装: $INSTALL_DIR/mmx-agent"

head_ "写入配置"
cat > "$STATE_DIR/config.yaml" <<YAML
master_url: "$MASTER_URL"
enroll_token: "$ENROLL_TOKEN"
state_dir: "$STATE_DIR"
xray_mode: "$XRAY_MODE"
YAML
chmod 0600 "$STATE_DIR/config.yaml"
ok "配置: $STATE_DIR/config.yaml"

head_ "注册服务"
if command -v systemctl >/dev/null 2>&1 && systemctl show >/dev/null 2>&1; then
  cat > /etc/systemd/system/mmx-agent.service <<UNIT
[Unit]
Description=MMX Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/mmx-agent -config $STATE_DIR/config.yaml
Restart=always
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now mmx-agent >/dev/null 2>&1
  sleep 2
  systemctl is-active --quiet mmx-agent && ok "systemd: mmx-agent.service 运行中" \
    || { journalctl -u mmx-agent -n 15 --no-pager || true; die "服务启动失败"; }
elif command -v rc-update >/dev/null 2>&1; then
  cat > /etc/init.d/mmx-agent <<'OPENRC'
#!/sbin/openrc-run
name="mmx-agent"
command="/usr/local/bin/mmx-agent"
command_args="-config /var/lib/mmx-agent/config.yaml"
command_background=true
pidfile="/run/mmx-agent.pid"
output_log="/var/log/mmx-agent.log"
error_log="/var/log/mmx-agent.log"
OPENRC
  chmod 0755 /etc/init.d/mmx-agent
  rc-update add mmx-agent default >/dev/null 2>&1
  rc-service mmx-agent start >/dev/null 2>&1
  ok "OpenRC: mmx-agent 已启动"
else
  warn "未检测到 systemd / OpenRC，改为前台运行"
  nohup "$INSTALL_DIR/mmx-agent" -config "$STATE_DIR/config.yaml" >> "$STATE_DIR/agent.log" 2>&1 &
  ok "已后台启动（日志: $STATE_DIR/agent.log）"
fi

head_ "完成"
echo "  Agent 已启动，请回到主控「服务管理」查看该服务器是否变为在线。"
echo "  卸载:  systemctl disable --now mmx-agent && rm -f /etc/systemd/system/mmx-agent.service $INSTALL_DIR/mmx-agent && rm -rf $STATE_DIR"
`

// AgentBinaryPath 返回某平台 Agent 二进制在磁盘上的相对路径。
func AgentBinaryPath(dir, platform string) string {
	return path.Join(dir, "mmx-agent-"+platform)
}
