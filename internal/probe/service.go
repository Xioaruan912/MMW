// Package probe 实现对外展示的「探针」页数据聚合。
//
// 探针是一套**伪装页**：把主控伪装成一个公开的状态监控页，让访客看不到
// 这是一套代理管理面板。因此这里的所有输出都必须是「看起来像监控数据」的东西，
// 且默认**不暴露**服务器名称（show_name 开关控制）。
package probe

import (
	"context"
	"time"

	"github.com/mmx-project/mmx/internal/storage"
)

// Settings 是探针的外观与行为配置（落 system_settings 的 probe.settings）。
type Settings struct {
	Enabled        bool   `json:"enabled"`
	Title          string `json:"title"`
	Logo           string `json:"logo"`
	Description    string `json:"description"`
	Theme          string `json:"theme"`      // auto / light / dark
	ColorMode      string `json:"color_mode"` // light / dark
	Revision       int    `json:"revision"`   // 外观版本号，前端据此判断是否要重绘
	BlockLogin     bool   `json:"block_login"`
	ShowName       bool   `json:"show_name"`
	ShowGlobe      bool   `json:"show_globe"`
	ShowFooter     bool   `json:"show_footer"`
	RequireToken   bool   `json:"require_token"`
	// Token 是独立存储的（见 TokenKey），此处仅为运行期载体，不随配置序列化。
	Token          string `json:"-"`
	RefreshSeconds int    `json:"refresh_seconds"`
}

// DefaultSettings 返回默认配置。
//
// 默认**关闭**（探针是对外暴露面，必须显式开启）；
// 默认**不显示服务器名**（最小信息披露）。
func DefaultSettings() Settings {
	return Settings{
		Enabled:        false,
		Title:          "服务状态监控",
		Theme:          "auto",
		ColorMode:      "dark",
		Revision:       1,
		ShowName:       false,
		ShowGlobe:      true,
		ShowFooter:     true,
		RefreshSeconds: 5,
	}
}

// SettingsKey 是探针配置在 system_settings 里的键名。
const SettingsKey = "probe.settings"

// TokenKey 是探针访问令牌的独立键名。
//
// 单独存而不是放在 Settings 里：Settings 需要整体序列化，
// 而令牌必须能取出来校验、又不能随配置一起回显给前端。
// 早先把 Token 标成 `json:"-"` 直接导致它**根本不会被持久化** —— 轮换后自己也用不了。
const TokenKey = "probe.token"

// ServerSnapshot 是单台服务器在探针页上的呈现。
//
// 字段名与妙妙屋X 的探针 API 文档保持一致，便于外部探针复用。
type ServerSnapshot struct {
	Name             string  `json:"name"`
	Online           bool    `json:"online"`
	Region           string  `json:"region"`
	RegionCountry    string  `json:"region_country"`
	RegionCity       string  `json:"region_city"`
	ProviderName     string  `json:"provider_name"`
	ProviderURL      string  `json:"provider_url"`
	TelecomPaidPeer  bool    `json:"telecom_paid_peer"`
	UploadSpeed      int64   `json:"upload_speed"`   // B/s
	DownloadSpeed    int64   `json:"download_speed"` // B/s
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsed       int64   `json:"memory_used"`
	MemoryTotal      int64   `json:"memory_total"`
	DiskUsed         int64   `json:"disk_used"`
	DiskTotal        int64   `json:"disk_total"`
	Load1            float64 `json:"load1"`
	UptimeSec        int64   `json:"uptime_sec"`
	TCPConns         int64   `json:"tcp_conns"`
	TrafficUsed      int64   `json:"traffic_used"`  // 计费周期计费用量
	TrafficLimit     int64   `json:"traffic_limit"` // 0 = 不限
	TrafficUsedUp    int64   `json:"traffic_used_up"`
	TrafficUsedDown  int64   `json:"traffic_used_down"`
	TrafficUsedTotal int64   `json:"traffic_used_total"`
	PeriodStart      string  `json:"period_start"` // YYYY-MM-DD（含）
	PeriodEnd        string  `json:"period_end"`   // YYYY-MM-DD（不含）
	LastSeenAt       int64   `json:"last_seen_at"`
}

// Appearance 是外观描述。
type Appearance struct {
	Theme     string `json:"theme"`
	ColorMode string `json:"color_mode"`
	Revision  int    `json:"revision"`
}

// Snapshot 是探针页的一次完整快照。
type Snapshot struct {
	Enabled     bool             `json:"enabled"`
	Title       string           `json:"title"`
	Logo        string           `json:"logo"`
	Description string           `json:"description"`
	Appearance  Appearance       `json:"appearance"`
	BlockLogin  bool             `json:"block_login"`
	ShowName    bool             `json:"show_name"`
	ShowGlobe   bool             `json:"show_globe"`
	ShowFooter  bool             `json:"show_footer"`
	Servers     []ServerSnapshot `json:"servers"`
	UpdatedAt   int64            `json:"updated_at"`
}

// Service 负责组装探针数据。
type Service struct {
	store *storage.Store
	// online 判断某台服务器当前是否有活跃 Agent 连接
	online func(serverID string) bool
}

// NewService 构造 Service。
func NewService(store *storage.Store, online func(string) bool) *Service {
	return &Service{store: store, online: online}
}

// LoadSettings 读取探针配置（不存在时返回默认值）。
func (s *Service) LoadSettings(ctx context.Context) (Settings, error) {
	st := DefaultSettings()
	if _, err := s.store.GetJSONSetting(ctx, SettingsKey, &st); err != nil {
		return st, err
	}
	if st.RefreshSeconds <= 0 {
		st.RefreshSeconds = 5
	}
	// 令牌从独立键读回
	if tok, ok, err := s.store.GetSetting(ctx, TokenKey); err != nil {
		return st, err
	} else if ok {
		st.Token = tok
	}
	return st, nil
}

// SaveSettings 保存探针配置（令牌写入独立键）。
func (s *Service) SaveSettings(ctx context.Context, st Settings) error {
	if st.Theme == "" {
		st.Theme = "auto"
	}
	if st.ColorMode == "" {
		st.ColorMode = "dark"
	}
	if st.Title == "" {
		st.Title = "服务状态监控"
	}
	if st.RefreshSeconds <= 0 || st.RefreshSeconds > 300 {
		st.RefreshSeconds = 5
	}
	st.Revision++

	token := st.Token
	st.Token = "" // 不进 JSON
	if err := s.store.SetJSONSetting(ctx, SettingsKey, st); err != nil {
		return err
	}
	if token != "" {
		return s.store.SetSetting(ctx, TokenKey, token)
	}
	return nil
}

// HasToken 报告是否已设置访问令牌。
func (s *Service) HasToken(ctx context.Context) bool {
	tok, ok, err := s.store.GetSetting(ctx, TokenKey)
	return err == nil && ok && tok != ""
}

// Snapshot 组装一次快照。
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	st, err := s.LoadSettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Enabled:     st.Enabled,
		Title:       st.Title,
		Logo:        st.Logo,
		Description: st.Description,
		Appearance: Appearance{
			Theme: st.Theme, ColorMode: st.ColorMode, Revision: st.Revision,
		},
		BlockLogin: st.BlockLogin,
		ShowName:   st.ShowName,
		ShowGlobe:  st.ShowGlobe,
		ShowFooter: st.ShowFooter,
		UpdatedAt:  time.Now().UnixMilli(),
		Servers:    []ServerSnapshot{},
	}
	if !st.Enabled {
		// 关闭时只保证返回 enabled 字段语义，不泄露任何服务器信息
		return snap, nil
	}

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for _, sv := range servers {
		if !sv.ProbeVisible {
			continue
		}
		item := ServerSnapshot{
			Online:          s.online(sv.ID),
			Region:          sv.Region,
			RegionCountry:   sv.RegionCountry,
			RegionCity:      sv.RegionCity,
			ProviderName:    sv.ProviderName,
			ProviderURL:     sv.ProviderURL,
			TelecomPaidPeer: sv.TelecomPaidPeer,
			TrafficLimit:    sv.TrafficLimit,
			LastSeenAt:      sv.LastSeenAt,
		}
		if st.ShowName {
			item.Name = sv.Name
		} else {
			item.Name = maskedName(sv)
		}

		// 计费周期
		start, end := PeriodRange(time.Now(), sv.PeriodStartDay)
		item.PeriodStart = start.Format("2006-01-02")
		item.PeriodEnd = end.Format("2006-01-02")

		if m, ok, err := s.store.LatestMetrics(ctx, sv.ID); err == nil && ok {
			item.UploadSpeed = m.NetUp
			item.DownloadSpeed = m.NetDown
			item.CPUPercent = m.CPUPercent
			item.MemoryUsed = m.MemUsed
			item.MemoryTotal = m.MemTotal
			item.DiskUsed = m.DiskUsed
			item.DiskTotal = m.DiskTotal
			item.Load1 = m.Load1
			item.UptimeSec = m.UptimeSec
			item.TCPConns = m.TCPConns
		}

		// 本计费周期的流量用量：从日表按周期区间求和
		if sum, err := s.store.ServerTrafficRange(ctx, sv.ID, start, end); err == nil {
			item.TrafficUsedUp = sum.Up
			item.TrafficUsedDown = sum.Down
			item.TrafficUsedTotal = sum.Total
			item.TrafficUsed = sum.Total
		}

		snap.Servers = append(snap.Servers, item)
	}
	return snap, nil
}

// maskedName 在 show_name=false 时给服务器一个不可反查的展示名。
//
// 不用 "服务器 1/2/3" 这类可枚举名字，而是按地区回退 —— 既保持可读，
// 又不会泄露用户给服务器起的内部标识。
func maskedName(sv storage.Server) string {
	switch {
	case sv.RegionCity != "":
		return sv.Region + " " + sv.RegionCity
	case sv.RegionCountry != "":
		return sv.Region + " " + sv.RegionCountry
	case sv.Region != "":
		return sv.Region
	default:
		return "节点"
	}
}

// PeriodRange 计算包含 now 的计费周期 [start, end)。
//
// startDay 为每月起始日（1-28，避免不同月份天数差异）。若 now 的日 >= startDay，
// 周期为「本月 startDay 到下月 startDay」；否则为「上月 startDay 到本月 startDay」。
func PeriodRange(now time.Time, startDay int) (time.Time, time.Time) {
	if startDay < 1 {
		startDay = 1
	}
	if startDay > 28 {
		startDay = 28
	}
	y, m, d := now.Date()
	loc := now.Location()

	startThisMonth := time.Date(y, m, startDay, 0, 0, 0, 0, loc)
	var start time.Time
	if d >= startDay {
		start = startThisMonth
	} else {
		start = time.Date(y, m-1, startDay, 0, 0, 0, 0, loc)
	}
	end := time.Date(start.Year(), start.Month()+1, startDay, 0, 0, 0, 0, loc)
	return start, end
}
