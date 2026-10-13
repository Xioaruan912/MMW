package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SpeedtestOptions 控制一次测速。
type SpeedtestOptions struct {
	// BinPath 是测速程序路径（默认 mihomo）
	BinPath string
	// ConfigPath 是传给测速程序的配置文件路径
	ConfigPath string
	// Target 是待测节点（用于生成临时配置，若未提供 ConfigPath）
	Target Target
	// Timeout 整体超时
	Timeout time.Duration
	// DownloadURL / SHA256 用于自动获取测速程序（可选）
	DownloadURL string
	SHA256      string
}

// SpeedtestResult 是测速结果。
type SpeedtestResult struct {
	Alive       bool   `json:"alive"`
	DownloadBPS int64  `json:"download_bps"`
	UploadBPS   int64  `json:"upload_bps"`
	LatencyMS   int    `json:"latency_ms"`
	Raw         string `json:"raw,omitempty"`
	Error       string `json:"error,omitempty"`
}

// DefaultSpeedtestBin 是默认测速程序。
//
// 用 mihomo：它支持完整协议矩阵（含 REALITY），且是社区通用工具。
// MMX 不分发它，只在用户显式调用 speedtest.run 且提供了下载地址时获取。
const DefaultSpeedtestBin = "/usr/local/bin/mihomo"

// EnsureBinary 确保测速程序存在；不存在且提供了下载地址时自动获取并校验。
//
// 校验失败绝不落盘 —— 装一个来路不明的可执行文件是最糟的结果。
func EnsureBinary(ctx context.Context, opts SpeedtestOptions) (string, error) {
	bin := opts.BinPath
	if bin == "" {
		bin = DefaultSpeedtestBin
	}
	if st, err := os.Stat(bin); err == nil && !st.IsDir() {
		return bin, nil
	}
	if opts.DownloadURL == "" {
		return "", fmt.Errorf("测速程序不存在: %s，且未提供 download_url 自动获取", bin)
	}

	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "mmx-agent")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载测速程序失败: HTTP %d", resp.StatusCode)
	}

	tmp := bin + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	got := hex.EncodeToString(h.Sum(nil))
	if opts.SHA256 != "" && !strings.EqualFold(opts.SHA256, got) {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("测速程序 sha256 校验失败：期望 %s 实得 %s", opts.SHA256, got)
	}
	if err := os.Rename(tmp, bin); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return bin, nil
}

// RunSpeedtest 执行一次测速。
//
// 实现方式：用 mihomo 的 `-t`（连通性测试）拿延迟；若需要吞吐，
// 则用 `-d` 下载测速。参数差异较大，因此这里统一成"跑一次并把输出原样回传"，
// 由主控侧解析 —— 这样换任何测速程序都不用改 Agent。
func RunSpeedtest(ctx context.Context, opts SpeedtestOptions) (SpeedtestResult, error) {
	var res SpeedtestResult
	bin, err := EnsureBinary(ctx, opts)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	cfg := opts.ConfigPath
	if cfg == "" {
		return res, errors.New("缺少 config_path（测速需要一份指向被测节点的配置）")
	}
	if _, err := os.Stat(cfg); err != nil {
		res.Error = "配置文件不存在: " + cfg
		return res, errors.New(res.Error)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// -t 做连通性测试；-f 指定配置文件
	out, err := exec.CommandContext(cctx, bin, "-t", "-f", cfg).CombinedOutput()
	raw := string(out)
	res.Raw = truncate(raw, 4096)
	if err != nil {
		res.Error = fmt.Sprintf("测速程序退出异常: %v", err)
		return res, err
	}

	lat, dl, ul := parseMihomoOutput(raw)
	res.LatencyMS = lat
	res.DownloadBPS = dl
	res.UploadBPS = ul
	res.Alive = lat > 0 || dl > 0
	return res, nil
}

// parseMihomoOutput 从测速程序输出里提取延迟与吞吐。
//
// 不同工具输出格式不同，因此解析策略是"尽力而为"：
// 找到可识别的数字就用，找不到就只保留原始输出让主控侧判断。
func parseMihomoOutput(raw string) (latencyMS int, downBPS int64, upBPS int64) {
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimSpace(line)
		low := strings.ToLower(l)
		switch {
		case strings.Contains(low, "delay") || strings.Contains(low, "延迟"):
			if v, ok := firstInt(l); ok {
				latencyMS = v
			}
		case strings.Contains(low, "download") || strings.Contains(low, "下载"):
			if v, ok := firstInt(l); ok {
				downBPS = int64(v)
			}
		case strings.Contains(low, "upload") || strings.Contains(low, "上传"):
			if v, ok := firstInt(l); ok {
				upBPS = int64(v)
			}
		}
	}
	return latencyMS, downBPS, upBPS
}

// firstInt 提取一行里的第一个整数。
func firstInt(s string) (int, bool) {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			var n int
			if _, err := fmt.Sscanf(s[start:i], "%d", &n); err == nil {
				return n, true
			}
			start = -1
		}
	}
	if start >= 0 {
		var n int
		if _, err := fmt.Sscanf(s[start:], "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}
