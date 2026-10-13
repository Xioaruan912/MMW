package agentproto

// 本文件定义全部载荷结构。字段一旦发布只能追加，不能改名或删除。

// ---------------------------------------------------------------------------
// 握手
// ---------------------------------------------------------------------------

// HelloPayload 由 Agent 在连接建立后立即发送。
type HelloPayload struct {
	AgentVersion    string   `json:"agent_version"`
	ProtocolVersion int      `json:"protocol_version"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Kernel          string   `json:"kernel,omitempty"`
	Distro          string   `json:"distro,omitempty"`
	BootID          string   `json:"boot_id,omitempty"`
	XrayMode        string   `json:"xray_mode"`
	XrayVersion     string   `json:"xray_version,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
}

// HelloAckPayload 由主控返回，携带心跳间隔、主控公钥与能力开关。
type HelloAckPayload struct {
	ServerID             string          `json:"server_id"`
	ServerName           string          `json:"server_name"`
	HeartbeatIntervalSec int             `json:"heartbeat_interval_sec"`
	MetricsIntervalSec   int             `json:"metrics_interval_sec"`
	MasterPublicKey      string          `json:"master_public_key,omitempty"`
	Features             map[string]bool `json:"features,omitempty"`
	ServerTime           int64           `json:"server_time"`
}

// ---------------------------------------------------------------------------
// 心跳与指标
// ---------------------------------------------------------------------------

// HeartbeatPayload 是轻量保活，不带重指标。
type HeartbeatPayload struct {
	UptimeSec     int64   `json:"uptime_sec"`
	XrayRunning   bool    `json:"xray_running"`
	XrayConfigRev string  `json:"xray_config_rev,omitempty"`
	AgentLoad     float64 `json:"agent_load,omitempty"`
	Seq           uint64  `json:"seq"`
}

// TrafficDelta 是自上次上报以来的流量增量（字节）。
type TrafficDelta struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// MetricsPayload 是周期性的系统指标。
type MetricsPayload struct {
	TS          int64         `json:"ts"`
	CPUPercent  float64       `json:"cpu_percent"`
	MemUsed     int64         `json:"mem_used"`
	MemTotal    int64         `json:"mem_total"`
	SwapUsed    int64         `json:"swap_used,omitempty"`
	SwapTotal   int64         `json:"swap_total,omitempty"`
	DiskUsed    int64         `json:"disk_used"`
	DiskTotal   int64         `json:"disk_total"`
	Load1       float64       `json:"load1"`
	Load5       float64       `json:"load5"`
	Load15      float64       `json:"load15"`
	NetUp       int64         `json:"net_up"`
	NetDown     int64         `json:"net_down"`
	TCPConns    int64         `json:"tcp_conns"`
	UDPConns    int64         `json:"udp_conns"`
	Processes   int64         `json:"processes"`
	UptimeSec   int64         `json:"uptime_sec"`
	XrayRunning bool          `json:"xray_running"`
	XrayRSS     int64         `json:"xray_rss,omitempty"`
	Traffic     *TrafficDelta `json:"traffic,omitempty"`
}

// ---------------------------------------------------------------------------
// 任务
// ---------------------------------------------------------------------------

// TaskPayload 是主控下发的任务。签名覆盖 task_id|kind|sha256(args)|nonce。
//
// 注意：Args **不能**加 omitempty —— 否则空参数任务在传输后会被解成 nil，
// 与签名时的空 map 摘要不一致，导致验签失败（已由 protocol_test.go 回归覆盖）。
type TaskPayload struct {
	TaskID     string  `json:"task_id"`
	Kind       string  `json:"kind"`
	Nonce      string  `json:"nonce"`
	Signature  string  `json:"signature,omitempty"`
	DeadlineMS int64   `json:"deadline_ms"`
	Args       RawArgs `json:"args"`
}

// TaskResultPayload 是 Agent 的任务回执。
type TaskResultPayload struct {
	TaskID     string  `json:"task_id"`
	Status     string  `json:"status"` // ok | failed | rejected
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at"`
	Error      string  `json:"error,omitempty"`
	Output     string  `json:"output,omitempty"`
	Result     RawArgs `json:"result,omitempty"`
}

// 任务执行状态。
const (
	TaskStatusOK       = "ok"
	TaskStatusFailed   = "failed"
	TaskStatusRejected = "rejected"
)

// ---------------------------------------------------------------------------
// 配置下发
// ---------------------------------------------------------------------------

// ConfigPayload 是"这台服务器应该长什么样"的完整描述。
type ConfigPayload struct {
	Rev   string      `json:"rev"`
	Xray  *XrayConfig `json:"xray,omitempty"`
	Nginx *NginxSpec  `json:"nginx,omitempty"`
}

// XrayConfig 是 Xray 配置的强类型子集（用 map 承载未知字段，保证前向兼容）。
type XrayConfig struct {
	Log       map[string]any   `json:"log,omitempty"`
	API       map[string]any   `json:"api,omitempty"`
	Stats     map[string]any   `json:"stats,omitempty"`
	Policy    map[string]any   `json:"policy,omitempty"`
	DNS       map[string]any   `json:"dns,omitempty"`
	Inbounds  []map[string]any `json:"inbounds,omitempty"`
	Outbounds []map[string]any `json:"outbounds,omitempty"`
	Routing   map[string]any   `json:"routing,omitempty"`
}

// NginxSpec 是反代侧的期望状态。
type NginxSpec struct {
	Enabled bool   `json:"enabled"`
	Conf    string `json:"conf,omitempty"`
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// AckPayload 通用确认。
type AckPayload struct {
	ID string `json:"id"`
}
