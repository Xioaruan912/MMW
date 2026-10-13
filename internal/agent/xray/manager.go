// Package xray 管理节点上的 Xray 进程与配置。
//
// 两种模式：
//
//	external —— 使用系统安装的 xray（默认 /usr/local/bin/xray），由 systemd 管理；
//	embedded —— Agent 内嵌 xray-core，直接把配置喂给内嵌内核（P6 实现）。
//
// 本包只做「进程生命周期 + 配置落地 + 健康检查」，不做协议建模。
package xray

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmx-project/mmx/pkg/agentproto"
)

// Manager 是 Xray 生命周期的抽象。
type Manager interface {
	// Mode 返回当前模式。
	Mode() string
	// Status 返回是否在运行、版本号与当前配置摘要。
	Status() Status
	// ApplyConfig 写入配置并使其生效（幂等：rev 相同时直接返回）。
	ApplyConfig(cfg *agentproto.XrayConfig, rev string) error
	// Control 执行 start / stop / restart。
	Control(action string) error
	// Uninstall 卸载。
	Uninstall() error
}

// Status 是 Xray 的运行状态快照。
type Status struct {
	Running   bool   `json:"running"`
	Version   string `json:"version"`
	ConfigRev string `json:"config_rev"`
	PID       int    `json:"pid"`
	Error     string `json:"error,omitempty"`
}

// ErrNotImplemented 表示该模式下尚未实现的能力。
var ErrNotImplemented = errors.New("该能力尚未实现（见 docs/ROADMAP.md P6）")

// ---------------------------------------------------------------------------
// external 模式：系统 xray + systemd
// ---------------------------------------------------------------------------

// ExternalManager 通过文件 + systemd 管理系统安装的 Xray。
type ExternalManager struct {
	BinPath   string
	ConfigDir string
	Service   string
}

// NewExternal 构造 external 模式的 Manager。
func NewExternal(bin, configDir, service string) *ExternalManager {
	if bin == "" {
		bin = "/usr/local/bin/xray"
	}
	if configDir == "" {
		configDir = "/usr/local/etc/xray"
	}
	if service == "" {
		service = "xray"
	}
	return &ExternalManager{BinPath: bin, ConfigDir: configDir, Service: service}
}

// Mode 返回 "external"。
func (m *ExternalManager) Mode() string { return "external" }

// ConfigPath 返回主配置文件路径。
func (m *ExternalManager) ConfigPath() string { return filepath.Join(m.ConfigDir, "config.json") }

// RevPath 返回记录当前生效 rev 的文件路径。
func (m *ExternalManager) RevPath() string { return filepath.Join(m.ConfigDir, ".mmx-rev") }

// Status 读取进程与配置状态。
func (m *ExternalManager) Status() Status {
	st := Status{}
	if pid, err := findPID("xray"); err == nil {
		st.Running = true
		st.PID = pid
	}
	if _, err := os.Stat(m.BinPath); err == nil {
		if out, err := exec.Command(m.BinPath, "version").Output(); err == nil {
			st.Version = firstLine(string(out))
		}
	}
	if b, err := os.ReadFile(m.RevPath()); err == nil {
		st.ConfigRev = strings.TrimSpace(string(b))
	}
	return st
}

// ApplyConfig 写配置并重启。rev 未变则短路。
func (m *ExternalManager) ApplyConfig(cfg *agentproto.XrayConfig, rev string) error {
	if cfg == nil {
		return errors.New("配置为空")
	}
	if cur, err := os.ReadFile(m.RevPath()); err == nil && strings.TrimSpace(string(cur)) == rev && rev != "" {
		return nil // 已经是最新
	}
	if err := os.MkdirAll(m.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	raw, err := marshalConfig(cfg)
	if err != nil {
		return err
	}
	// 先写临时文件，用 xray 自身做语法校验，通过后再原子替换
	tmp := m.ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(m.BinPath); err == nil {
		if out, err := exec.Command(m.BinPath, "run", "-test", "-config", tmp).CombinedOutput(); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("xray 配置校验失败: %s", truncate(string(out), 800))
		}
	}
	if err := os.Rename(tmp, m.ConfigPath()); err != nil {
		return err
	}
	_ = os.WriteFile(m.RevPath(), []byte(rev+"\n"), 0o644)

	if st := m.Status(); st.Running {
		return m.Control("restart")
	}
	return nil
}

// Control 执行 start / stop / restart。
func (m *ExternalManager) Control(action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("不支持的动作 %q", action)
	}
	if hasSystemctl() {
		out, err := exec.Command("systemctl", action, m.Service).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s %s 失败: %s", action, m.Service, truncate(string(out), 400))
		}
		return nil
	}
	if hasOpenRC() {
		cmd := "start"
		switch action {
		case "stop":
			cmd = "stop"
		case "restart":
			cmd = "restart"
		}
		out, err := exec.Command("rc-service", m.Service, cmd).CombinedOutput()
		if err != nil {
			return fmt.Errorf("rc-service %s 失败: %s", cmd, truncate(string(out), 400))
		}
		return nil
	}
	return errors.New("未检测到 systemd / OpenRC，无法托管 Xray 服务")
}

// Uninstall 停止并移除服务单元（不删用户配置目录）。
func (m *ExternalManager) Uninstall() error {
	if hasSystemctl() {
		_, _ = exec.Command("systemctl", "disable", "--now", m.Service).CombinedOutput()
		_ = os.Remove("/etc/systemd/system/" + m.Service + ".service")
		_, _ = exec.Command("systemctl", "daemon-reload").CombinedOutput()
	}
	return nil
}

// ---------------------------------------------------------------------------
// embedded 模式（P6 实现）
// ---------------------------------------------------------------------------

// EmbeddedManager 预留：Agent 内嵌 xray-core。
type EmbeddedManager struct {
	ConfigDir string
}

// Mode 返回 "embedded"。
func (m *EmbeddedManager) Mode() string { return "embedded" }

// Status 目前只报告未运行。
func (m *EmbeddedManager) Status() Status {
	return Status{Running: false, Error: "embedded 模式尚未实现（P6）"}
}

// ApplyConfig 尚未实现。
func (m *EmbeddedManager) ApplyConfig(*agentproto.XrayConfig, string) error {
	return ErrNotImplemented
}

// Control 尚未实现。
func (m *EmbeddedManager) Control(string) error { return ErrNotImplemented }

// Uninstall 尚未实现。
func (m *EmbeddedManager) Uninstall() error { return ErrNotImplemented }

// ---------------------------------------------------------------------------
// none 模式
// ---------------------------------------------------------------------------

// NoopManager 用于 xray_mode=none 的纯监控节点。
type NoopManager struct{}

// Mode 返回 "none"。
func (m *NoopManager) Mode() string { return "none" }

// Status 总是未运行。
func (m *NoopManager) Status() Status { return Status{} }

// ApplyConfig 忽略配置。
func (m *NoopManager) ApplyConfig(*agentproto.XrayConfig, string) error { return nil }

// Control 忽略控制。
func (m *NoopManager) Control(string) error { return nil }

// Uninstall 无操作。
func (m *NoopManager) Uninstall() error { return nil }

// New 按模式构造 Manager。
func New(mode, bin, configDir, service string) Manager {
	switch mode {
	case "embedded":
		return &EmbeddedManager{ConfigDir: configDir}
	case "none":
		return &NoopManager{}
	default:
		return NewExternal(bin, configDir, service)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// ConfigRev 计算配置的稳定摘要。
func ConfigRev(cfg *agentproto.XrayConfig) string {
	raw, err := marshalConfig(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func hasSystemctl() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "show").Run() == nil
}

func hasOpenRC() bool {
	_, err := exec.LookPath("rc-service")
	return err == nil
}

// findPID 在 /proc 中按进程名查找 PID。
func findPID(name string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if pid == "" || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", pid, "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == name {
			var n int
			if _, err := fmt.Sscanf(pid, "%d", &n); err == nil {
				return n, nil
			}
		}
	}
	return 0, errors.New("进程不存在")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}

var _ = time.Second
