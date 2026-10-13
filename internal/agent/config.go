// Package agent 是 mmx-agent 的运行时代码。
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 Agent 的完整配置，可来自 YAML 文件。
type Config struct {
	// 连接
	MasterURL string `yaml:"master_url"`
	// 首启使用一次性入驻令牌；入驻成功后主控返回长期令牌，写入 AgentToken
	EnrollToken string `yaml:"enroll_token,omitempty"`
	AgentToken  string `yaml:"agent_token,omitempty"`
	ServerID    string `yaml:"server_id,omitempty"`

	// 运行
	StateDir          string        `yaml:"state_dir"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	MetricsInterval   time.Duration `yaml:"metrics_interval"`
	ReconnectMin      time.Duration `yaml:"reconnect_min"`
	ReconnectMax      time.Duration `yaml:"reconnect_max"`
	Debug             bool          `yaml:"debug"`

	// Xray
	XrayMode      string `yaml:"xray_mode"`       // embedded | external | none
	XrayBinPath   string `yaml:"xray_bin_path"`   // external 模式
	XrayConfigDir string `yaml:"xray_config_dir"` // 配置目录
	XrayService   string `yaml:"xray_service"`    // systemd 单元名，默认 xray

	// TLS
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		StateDir:          "/var/lib/mmx-agent",
		HeartbeatInterval: 15 * time.Second,
		MetricsInterval:   30 * time.Second,
		ReconnectMin:      1 * time.Second,
		ReconnectMax:      60 * time.Second,
		XrayMode:          "external",
		XrayBinPath:       "/usr/local/bin/xray",
		XrayConfigDir:     "/usr/local/etc/xray",
		XrayService:       "xray",
	}
}

// LoadConfig 读取 YAML 配置并套用默认值/环境变量覆盖。
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("读取配置 %s: %w", path, err)
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}
	applyEnvOverrides(&c)
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 15 * time.Second
	}
	if c.MetricsInterval <= 0 {
		c.MetricsInterval = 30 * time.Second
	}
	if c.ReconnectMin <= 0 {
		c.ReconnectMin = time.Second
	}
	if c.ReconnectMax < c.ReconnectMin {
		c.ReconnectMax = 60 * time.Second
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func applyEnvOverrides(c *Config) {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&c.MasterURL, "MMX_MASTER_URL")
	set(&c.EnrollToken, "MMX_ENROLL_TOKEN")
	set(&c.AgentToken, "MMX_AGENT_TOKEN")
	set(&c.ServerID, "MMX_SERVER_ID")
	set(&c.StateDir, "MMX_AGENT_STATE_DIR")
	set(&c.XrayMode, "MMX_XRAY_MODE")
	set(&c.XrayBinPath, "MMX_XRAY_BIN")
	set(&c.XrayConfigDir, "MMX_XRAY_CONFIG_DIR")
}

// Validate 校验必填项。
func (c Config) Validate() error {
	if strings.TrimSpace(c.MasterURL) == "" {
		return errors.New("master_url 不能为空")
	}
	if c.AgentToken == "" && c.EnrollToken == "" {
		return errors.New("agent_token 与 enroll_token 至少要有一个（首启用 enroll_token）")
	}
	switch c.XrayMode {
	case "embedded", "external", "none":
	default:
		return fmt.Errorf("xray_mode 必须是 embedded/external/none，得到 %q", c.XrayMode)
	}
	if c.StateDir == "" {
		return errors.New("state_dir 不能为空")
	}
	return nil
}

// MasterHTTPBase 返回用于 HTTP 请求的基址（去尾斜杠）。
func (c Config) MasterHTTPBase() string {
	return strings.TrimRight(c.MasterURL, "/")
}

// MasterWSURL 把 http(s) 基址换成 ws(s) 的 Agent 网关地址。
func (c Config) MasterWSURL() string {
	base := c.MasterHTTPBase()
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + "/api/agent/ws"
}

// SaveState 把入驻结果（长期令牌 + server_id）持久化回配置文件。
func (c Config) SaveState(path string) error {
	c.EnrollToken = "" // 一次性令牌用完即清
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
