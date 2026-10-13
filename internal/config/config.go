// Package config 负责主控与 Agent 的配置加载。
//
// 优先级：命令行参数 > 环境变量 > 配置文件 > 内置默认值。
// 主控（master）与 Agent（agent）的配置结构体分开，因为它们的关注点完全不同。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Master
// ---------------------------------------------------------------------------

// MasterConfig 是主控进程的全部配置。
type MasterConfig struct {
	// 监听地址与对外基础 URL
	Listen     string // 例如 0.0.0.0:12889
	PublicURL  string // 例如 https://panel.example.com ；用于生成 Agent 安装命令
	TrustProxy bool   // 是否信任 X-Forwarded-For / X-Real-IP
	DataDir    string // 数据目录：数据库、证书、订阅文件都在这里
	Debug      bool   // 打开调试日志

	// 数据库
	DatabaseDriver string // sqlite | postgres
	DatabaseDSN    string // sqlite 时留空则用 <DataDir>/mmx.db

	// 会话与签名
	JWTSecret     string        // 会话令牌签名密钥；留空则首次启动生成并落盘
	SessionTTL    time.Duration // 会话有效期
	MasterKeyPath string        // Ed25519 主密钥（用于给 Agent 任务签名）

	// Agent 通道
	AgentHeartbeatInterval time.Duration // 期望的心跳间隔，下发给 Agent
	AgentMetricsInterval   time.Duration // 期望的指标上报间隔
	AgentOfflineFactor     int           // last_seen 超过 N 倍心跳间隔即判离线
	AgentTokenReuseLock    time.Duration // 同 token 换 IP 时的锁定窗口

	TimeZone string
}

// DefaultMasterConfig 返回可直接运行的默认配置。
func DefaultMasterConfig() MasterConfig {
	return MasterConfig{
		Listen:                 "0.0.0.0:12889",
		PublicURL:              "",
		TrustProxy:             false,
		DataDir:                "./data",
		DatabaseDriver:         "sqlite",
		JWTSecret:              "",
		SessionTTL:             720 * time.Hour,
		AgentHeartbeatInterval: 15 * time.Second,
		AgentMetricsInterval:   30 * time.Second,
		AgentOfflineFactor:     3,
		AgentTokenReuseLock:    60 * time.Second,
		TimeZone:               "Local",
	}
}

// LoadMasterConfig 从环境变量加载主控配置。
func LoadMasterConfig() (MasterConfig, error) {
	c := DefaultMasterConfig()

	if v := os.Getenv("MMX_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("PORT"); v != "" && os.Getenv("MMX_LISTEN") == "" {
		c.Listen = "0.0.0.0:" + v
	}
	c.PublicURL = strings.TrimRight(env("MMX_PUBLIC_URL", c.PublicURL), "/")
	c.TrustProxy = envBool("MMX_TRUST_PROXY", c.TrustProxy)
	c.DataDir = env("MMX_DATA_DIR", c.DataDir)
	c.Debug = envBool("MMX_DEBUG", c.Debug)
	c.DatabaseDriver = strings.ToLower(env("MMX_DB_DRIVER", c.DatabaseDriver))
	c.DatabaseDSN = env("MMX_DB_DSN", c.DatabaseDSN)
	c.JWTSecret = env("MMX_JWT_SECRET", c.JWTSecret)
	c.SessionTTL = envDuration("MMX_SESSION_TTL", c.SessionTTL)
	c.MasterKeyPath = env("MMX_MASTER_KEY", c.MasterKeyPath)
	c.AgentHeartbeatInterval = envDuration("MMX_HEARTBEAT_INTERVAL", c.AgentHeartbeatInterval)
	c.AgentMetricsInterval = envDuration("MMX_METRICS_INTERVAL", c.AgentMetricsInterval)
	c.AgentOfflineFactor = envInt("MMX_OFFLINE_FACTOR", c.AgentOfflineFactor)
	c.AgentTokenReuseLock = envDuration("MMX_TOKEN_LOCK", c.AgentTokenReuseLock)
	c.TimeZone = env("TZ", c.TimeZone)

	if abs, err := filepath.Abs(c.DataDir); err == nil {
		c.DataDir = abs
	}
	if c.DatabaseDSN == "" && c.DatabaseDriver == "sqlite" {
		c.DatabaseDSN = filepath.Join(c.DataDir, "mmx.db")
	}
	if c.MasterKeyPath == "" {
		c.MasterKeyPath = filepath.Join(c.DataDir, "master_ed25519.key")
	}

	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// Validate 做基本一致性检查。
func (c MasterConfig) Validate() error {
	if c.Listen == "" {
		return errors.New("MMX_LISTEN 不能为空")
	}
	if c.DataDir == "" {
		return errors.New("MMX_DATA_DIR 不能为空")
	}
	switch c.DatabaseDriver {
	case "sqlite":
		if c.DatabaseDSN == "" {
			return errors.New("sqlite 驱动需要 DatabaseDSN（或可通过 DataDir 推导）")
		}
	case "postgres":
		if c.DatabaseDSN == "" {
			return errors.New("postgres 驱动必须提供 MMX_DB_DSN")
		}
	default:
		return fmt.Errorf("不支持的数据库驱动: %s", c.DatabaseDriver)
	}
	if c.AgentHeartbeatInterval < time.Second {
		return errors.New("心跳间隔不能小于 1s")
	}
	if c.AgentOfflineFactor < 2 {
		return errors.New("离线判定倍数不能小于 2")
	}
	return nil
}

// BaseURL 返回用于生成安装命令的对外基址；未配置时用监听地址推导。
func (c MasterConfig) BaseURL() string {
	if c.PublicURL != "" {
		return c.PublicURL
	}
	host := c.Listen
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	}
	return "http://" + host
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

// AgentConfig 是远程 Agent 进程的全部配置。
type AgentConfig struct {
	MasterURL string // 主控基址，例如 https://panel.example.com
	Token     string // 长期 agent_token
	ServerID  string // 主控分配，enroll 后写入
	StateDir  string // 状态目录

	HeartbeatInterval time.Duration
	MetricsInterval   time.Duration

	XrayMode      string // embedded | external | none
	XrayBinPath   string // 外置模式的 xray 可执行文件
	XrayConfigDir string // Xray 配置目录

	InsecureSkipVerify bool // 仅自签证书调试用
	Debug              bool
}

// DefaultAgentConfig 返回 Agent 默认配置。
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		StateDir:          "/var/lib/mmx-agent",
		HeartbeatInterval: 15 * time.Second,
		MetricsInterval:   30 * time.Second,
		XrayMode:          "external",
		XrayBinPath:       "/usr/local/bin/xray",
		XrayConfigDir:     "/usr/local/etc/xray",
	}
}

// Validate 校验 Agent 配置。
func (c AgentConfig) Validate() error {
	if c.MasterURL == "" {
		return errors.New("master_url 不能为空")
	}
	if c.Token == "" {
		return errors.New("agent token 不能为空")
	}
	switch c.XrayMode {
	case "embedded", "external", "none":
	default:
		return fmt.Errorf("xray_mode 必须是 embedded/external/none，得到 %q", c.XrayMode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// env helpers
// ---------------------------------------------------------------------------

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
