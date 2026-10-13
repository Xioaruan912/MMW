package storage

import "time"

// 服务器在线状态。
const (
	ServerOnline   = "online"
	ServerOffline  = "offline"
	ServerDegraded = "degraded"
)

// Xray 运行模式。
const (
	XrayModeEmbedded = "embedded"
	XrayModeExternal = "external"
	XrayModeNone     = "none"
)

// 令牌类型。
const (
	TokenKindEnroll = "enroll"
	TokenKindAgent  = "agent"
)

// User 是面板账号。
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Nickname     string `json:"nickname"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	TOTPSecret   string `json:"-"`
	// 订阅凭据（用户可重置；重置后旧链接立即失效）
	SubToken string `json:"sub_token"`
	// 配额与有效期：TrafficLimit=0 表示不限量；ExpireAt=0 表示不过期
	TrafficLimit int64 `json:"traffic_limit"`
	TrafficUsed  int64 `json:"traffic_used"`
	ExpireAt     int64 `json:"expire_at"`
	// TemplateID 是该用户订阅所用的模板（空表示用默认）
	TemplateID string `json:"template_id"`
	// PackageID 是当前套餐（空表示未绑定）
	PackageID string `json:"package_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Server 是一台受管服务器。
type Server struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Address         string `json:"address"`
	Remark          string `json:"remark"`
	XrayMode        string `json:"xray_mode"`
	Status          string `json:"status"`
	AgentVersion    string `json:"agent_version"`
	ProtocolVersion int    `json:"protocol_version"`
	LastSeenAt      int64  `json:"last_seen_at"`
	LastIP          string `json:"last_ip"`
	LastError       string `json:"last_error"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`

	// 展示信息（探针页与列表页使用）
	Region          string `json:"region"`            // 地区 Emoji
	RegionCountry   string `json:"region_country"`    // 国家
	RegionCity      string `json:"region_city"`       // 城市
	ProviderName    string `json:"provider_name"`     // 服务商
	ProviderURL     string `json:"provider_url"`      // 服务商网址
	TelecomPaidPeer bool   `json:"telecom_paid_peer"` // 是否电信 163 Paid Peer
	TrafficLimit    int64  `json:"traffic_limit"`     // 计费周期流量上限（0 = 不限）
	PeriodStartDay  int    `json:"period_start_day"`  // 计费起始日（1-28）
	ProbeVisible    bool   `json:"probe_visible"`     // 是否在探针页展示
	Weight          int    `json:"weight"`            // 排序权重（越大越靠前）
}

// Online 按 last_seen 与心跳间隔判断是否在线。
func (s Server) Online(now time.Time, heartbeat time.Duration, factor int) bool {
	if s.LastSeenAt == 0 {
		return false
	}
	deadline := time.Duration(factor) * heartbeat
	return now.Sub(time.UnixMilli(s.LastSeenAt)) <= deadline
}

// AgentToken 是 enroll / agent 令牌记录。明文只在生成时返回一次。
type AgentToken struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	Kind      string `json:"kind"`
	TokenHash string `json:"-"`
	ExpiresAt int64  `json:"expires_at"`
	UsedAt    int64  `json:"used_at"`
	RevokedAt int64  `json:"revoked_at"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy string `json:"created_by"`
}

// Expired 判断令牌在给定时刻是否已失效。
func (t AgentToken) Expired(now time.Time) bool {
	if t.RevokedAt != 0 {
		return true
	}
	if t.ExpiresAt != 0 && now.UnixMilli() > t.ExpiresAt {
		return true
	}
	return false
}

// ServerMetrics 是一次指标上报。
type ServerMetrics struct {
	ServerID    string  `json:"server_id"`
	TS          int64   `json:"ts"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsed     int64   `json:"mem_used"`
	MemTotal    int64   `json:"mem_total"`
	DiskUsed    int64   `json:"disk_used"`
	DiskTotal   int64   `json:"disk_total"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	NetUp       int64   `json:"net_up"`
	NetDown     int64   `json:"net_down"`
	TCPConns    int64   `json:"tcp_conns"`
	UDPConns    int64   `json:"udp_conns"`
	Processes   int64   `json:"processes"`
	UptimeSec   int64   `json:"uptime_sec"`
	XrayRunning bool    `json:"xray_running"`
	TrafficUp   int64   `json:"traffic_up"`
	TrafficDown int64   `json:"traffic_down"`
}

// AuditLog 是一条审计记录。
type AuditLog struct {
	ID     string `json:"id"`
	TS     int64  `json:"ts"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
}

// 常用审计动作名。
const (
	AuditServerCreate     = "server.create"
	AuditServerDelete     = "server.delete"
	AuditServerEnroll     = "server.enroll"
	AuditServerConnect    = "server.connect"
	AuditServerTokenReuse = "server.agent_token_reuse"
	AuditTaskDispatch     = "task.dispatch"
	AuditLoginOK          = "auth.login.ok"
	AuditLoginFail        = "auth.login.fail"
	AuditLoginLocked      = "auth.login.locked"
)
