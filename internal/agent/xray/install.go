package xray

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstallOptions 控制安装行为。
type InstallOptions struct {
	// 版本号（不含 v 前缀），例如 "25.9.1"。为空则用 DefaultVersion。
	Version string
	// 下载基址。默认从 XTLS 官方 Release 拉取；可指向自建镜像。
	// 期望的完整 URL 形如 <BaseURL>/Xray-linux-64.zip
	BaseURL string
	// 期望的 zip sha256（小写 hex）。为空则跳过校验（仅用于自建镜像场景）。
	SHA256 string
	// 资源目录（geoip.dat / geosite.dat 的落盘位置），默认 /usr/local/share/xray
	AssetDir string
}

// DefaultVersion 是未指定版本时安装的 Xray 版本。
// 固定版本而非 latest：可复现，且避免上游突发变更导致部署失败。
const DefaultVersion = "25.9.1"

// DefaultDownloadBase 是默认下载基址。
const DefaultDownloadBase = "https://github.com/XTLS/Xray-core/releases/download"

// maxDownloadSize 防御性上限（防止镜像返回异常大文件打爆磁盘）。
const maxDownloadSize = 200 << 20

// InstallResult 是安装结果。
type InstallResult struct {
	Version     string `json:"version"`
	BinaryPath  string `json:"binary_path"`
	AssetDir    string `json:"asset_dir"`
	ServiceMode string `json:"service_mode"` // systemd | openrc | manual
	SHA256      string `json:"sha256"`
	Started     bool   `json:"started"`
}

// Install 下载并安装 Xray 内核（external 模式）。
//
// 流程：探测架构 → 下载 → 校验 → 解压 → 安装二进制与 geo 数据 → 注册服务 → 启动。
// 任何一步失败都会保留原有二进制（先写临时文件再原子替换）。
func (m *ExternalManager) Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	res := InstallResult{}

	ver := strings.TrimPrefix(strings.TrimSpace(opts.Version), "v")
	if ver == "" {
		ver = DefaultVersion
	}
	res.Version = ver

	asset, err := xrayAssetName()
	if err != nil {
		return res, err
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultDownloadBase
	}
	url := fmt.Sprintf("%s/v%s/%s", base, ver, asset)

	rc, size, err := download(ctx, url)
	if err != nil {
		return res, fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxDownloadSize))
	if err != nil {
		return res, fmt.Errorf("读取下载内容失败: %w", err)
	}
	sum := sha256.Sum256(data)
	gotSHA := hex.EncodeToString(sum[:])
	res.SHA256 = gotSHA
	if opts.SHA256 != "" && !strings.EqualFold(opts.SHA256, gotSHA) {
		return res, fmt.Errorf("sha256 校验失败：期望 %s 实得 %s", opts.SHA256, gotSHA)
	}

	assetDir := opts.AssetDir
	if assetDir == "" {
		assetDir = "/usr/local/share/xray"
	}
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		return res, fmt.Errorf("创建资源目录失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.BinPath), 0o755); err != nil {
		return res, fmt.Errorf("创建二进制目录失败: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return res, fmt.Errorf("包不是合法 zip（大小 %d）: %w", size, err)
	}

	installedBin := false
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		switch base {
		case "xray":
			if err := extractTo(f, m.BinPath, 0o755); err != nil {
				return res, fmt.Errorf("安装 xray 二进制失败: %w", err)
			}
			installedBin = true
		case "geoip.dat", "geosite.dat", "geoip-only-cn-private.dat":
			if err := extractTo(f, filepath.Join(assetDir, base), 0o644); err != nil {
				return res, fmt.Errorf("安装 %s 失败: %w", base, err)
			}
		}
	}
	if !installedBin {
		return res, fmt.Errorf("包内未找到 xray 可执行文件（%s）", asset)
	}
	res.BinaryPath = m.BinPath
	res.AssetDir = assetDir

	// 确保配置目录存在
	if err := os.MkdirAll(m.ConfigDir, 0o755); err != nil {
		return res, fmt.Errorf("创建配置目录失败: %w", err)
	}

	// 注册服务
	mode, err := m.registerService(ver)
	if err != nil {
		return res, err
	}
	res.ServiceMode = mode

	// 只有存在配置时才启动，否则等主控下发配置后再起
	if _, statErr := os.Stat(m.ConfigPath()); statErr == nil {
		if err := m.Control("restart"); err == nil {
			res.Started = true
		}
	}
	return res, nil
}

// UninstallInternal 卸载二进制与服务（保留配置目录，便于回滚）。
func (m *ExternalManager) UninstallInternal() error {
	_ = m.Uninstall()
	_ = os.Remove(m.BinPath)
	_ = os.Remove(m.RevPath())
	return nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// xrayAssetName 按当前平台返回官方 Release 的资产名。
func xrayAssetName() (string, error) {
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "Xray-linux-64.zip", nil
		case "arm64":
			return "Xray-linux-arm64-v8a.zip", nil
		case "arm":
			return "Xray-linux-arm32-v7a.zip", nil
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "Xray-macos-64.zip", nil
		case "arm64":
			return "Xray-macos-arm64-v8a.zip", nil
		}
	}
	return "", fmt.Errorf("不支持的平台 %s/%s", runtime.GOOS, runtime.GOARCH)
}

func download(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "mmx-agent")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// extractTo 把 zip 条目原子地写到目标路径。
func extractTo(f *zip.File, dst string, mode os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, maxDownloadSize)); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// registerService 注册 systemd / OpenRC 服务，返回实际使用的托管方式。
func (m *ExternalManager) registerService(version string) (string, error) {
	if hasSystemctl() {
		unit := fmt.Sprintf(`[Unit]
Description=Xray Service (managed by MMX)
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run -config %s
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`, m.BinPath, m.ConfigPath())
		if err := os.WriteFile("/etc/systemd/system/"+m.Service+".service", []byte(unit), 0o644); err != nil {
			return "", fmt.Errorf("写入 systemd unit 失败: %w", err)
		}
		_, _ = exec.Command("systemctl", "daemon-reload").CombinedOutput()
		_, _ = exec.Command("systemctl", "enable", m.Service).CombinedOutput()
		return "systemd", nil
	}

	if hasOpenRC() {
		script := fmt.Sprintf(`#!/sbin/openrc-run
name="xray"
command="%s"
command_args="run -config %s"
command_background=true
pidfile="/run/xray.pid"
output_log="/var/log/xray.log"
error_log="/var/log/xray.log"
`, m.BinPath, m.ConfigPath())
		if err := os.WriteFile("/etc/init.d/"+m.Service, []byte(script), 0o755); err != nil {
			return "", fmt.Errorf("写入 OpenRC 脚本失败: %w", err)
		}
		_, _ = exec.Command("rc-update", "add", m.Service, "default").CombinedOutput()
		return "openrc", nil
	}

	// 既没有 systemd 也没有 OpenRC：写一个前台启动脚本，主控侧仍可通过
	// xray.start/stop/restart 之外的路径管理（此时 Control 会明确报错）。
	scriptPath := filepath.Join(m.ConfigDir, "start-xray.sh")
	script := fmt.Sprintf("#!/bin/sh\nexec %s run -config %s\n", m.BinPath, m.ConfigPath())
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return "", fmt.Errorf("写入启动脚本失败: %w", err)
	}
	_ = version
	return "manual", nil
}
