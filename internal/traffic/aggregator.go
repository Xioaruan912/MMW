// Package traffic 负责流量归集：把 Agent 上报的增量落到「服务器日表」与「用户日表」。
//
// 两条来源：
//
//  1. 服务器总量 —— 随 metrics 上报的增量（traffic.up/down），已在 agentlink 里解析
//  2. 每用户用量 —— 通过 xray.query_stats 任务向 Agent 拉取 Xray 的 stats 计数
//
// 为什么用户用量要单独拉：Xray 的 stats 是按 email 计数的，
// 只有 Agent 能访问本地 Xray API；主控看不到进程内部计数器。
package traffic

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// Hub 是本包对 agentlink 的最小依赖。
type Hub interface {
	IsOnline(serverID string) bool
	DispatchTask(ctx context.Context, serverID, kind string, args agentproto.RawArgs, actor string) (agentproto.TaskResultPayload, error)
}

// Aggregator 把指标里的流量增量写入日表。
type Aggregator struct {
	store *storage.Store
	log   *slog.Logger
}

// NewAggregator 构造 Aggregator。
func NewAggregator(store *storage.Store, log *slog.Logger) *Aggregator {
	return &Aggregator{store: store, log: log}
}

// OnMetrics 由 agentlink 的指标钩子调用。
func (a *Aggregator) OnMetrics(serverID string, m agentproto.MetricsPayload) {
	if m.Traffic == nil || (m.Traffic.Up == 0 && m.Traffic.Down == 0) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.store.AddServerTraffic(ctx, serverID, m.Traffic.Up, m.Traffic.Down); err != nil {
		a.log.Warn("服务器流量归集失败", "server", serverID, "err", err)
	}
}

// UserUsage 是拉取回来的一次每用户用量。
type UserUsage struct {
	Email string
	Up    int64
	Down  int64
}

// statsTokenRe 同时匹配文本与 JSON 两种输出里的 name / value。
//
// 不用逐行解析：`xray api statsquery --output json` 会把 name 与 value 放在同一行，
// 而默认文本格式放在相邻两行。按出现顺序配对两种都能处理。
var (
	statsNameRe  = regexp.MustCompile(`(?:name"?\s*:\s*)"([^"]+)"`)
	statsValueRe = regexp.MustCompile(`(?:value"?\s*:\s*)"?([0-9]+)"?`)
	userKeyRe    = regexp.MustCompile(`^user>>>(.+)>>>traffic>>>(uplink|downlink)$`)
)

// ParseStatsOutput 解析 `xray api statsquery -pattern "user>>>" -reset` 的输出。
//
// 纯函数，单独测。
func ParseStatsOutput(raw string) []UserUsage {
	type token struct {
		pos   int
		name  string
		value int64
		isVal bool
	}
	var toks []token
	for _, m := range statsNameRe.FindAllStringSubmatchIndex(raw, -1) {
		toks = append(toks, token{pos: m[0], name: raw[m[2]:m[3]]})
	}
	for _, m := range statsValueRe.FindAllStringSubmatchIndex(raw, -1) {
		v, err := strconv.ParseInt(raw[m[2]:m[3]], 10, 64)
		if err != nil {
			continue
		}
		toks = append(toks, token{pos: m[0], value: v, isVal: true})
	}
	// 按位置排序后，每个 name 与其后的第一个 value 配对
	sort.Slice(toks, func(i, j int) bool { return toks[i].pos < toks[j].pos })

	agg := map[string]*UserUsage{}
	pending := ""
	for _, tk := range toks {
		if !tk.isVal {
			pending = tk.name
			continue
		}
		if pending == "" {
			continue
		}
		m := userKeyRe.FindStringSubmatch(pending)
		pending = ""
		if m == nil {
			continue // 非用户维度（inbound>>> / outbound>>> 等）直接忽略
		}
		email, dir := m[1], m[2]
		u := agg[email]
		if u == nil {
			u = &UserUsage{Email: email}
			agg[email] = u
		}
		if dir == "uplink" {
			u.Up += tk.value
		} else {
			u.Down += tk.value
		}
	}

	out := make([]UserUsage, 0, len(agg))
	for _, u := range agg {
		out = append(out, *u)
	}
	return out
}

// Puller 周期性向在线服务器拉取每用户用量并写入日表。
type Puller struct {
	store    *storage.Store
	hub      Hub
	log      *slog.Logger
	interval time.Duration
}

// NewPuller 构造 Puller。interval 建议 5 分钟（Xray stats 是累计计数，reset 后即增量）。
func NewPuller(store *storage.Store, hub Hub, log *slog.Logger, interval time.Duration) *Puller {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Puller{store: store, hub: hub, log: log, interval: interval}
}

// Run 阻塞运行直到 ctx 取消。
func (p *Puller) Run(ctx context.Context) {
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.pullOnce(ctx)
		}
	}
}

func (p *Puller) pullOnce(ctx context.Context) {
	servers, err := p.store.ListServers(ctx)
	if err != nil {
		p.log.Warn("流量拉取：读取服务器列表失败", "err", err)
		return
	}
	for _, s := range servers {
		if !p.hub.IsOnline(s.ID) {
			continue
		}
		if s.XrayMode == storage.XrayModeNone {
			continue
		}
		ctx2, cancel := context.WithTimeout(ctx, 40*time.Second)
		res, err := p.hub.DispatchTask(ctx2, s.ID, agentproto.TaskXrayQueryStats,
			agentproto.RawArgs{"reset": true}, "traffic-puller")
		cancel()
		if err != nil {
			p.log.Debug("流量拉取失败", "server", s.ID, "err", err)
			continue
		}
		if res.Status != agentproto.TaskStatusOK {
			p.log.Debug("流量拉取被拒", "server", s.ID, "status", res.Status, "err", res.Error)
			continue
		}
		raw := ""
		if res.Result != nil {
			raw = res.Result.GetString("raw")
		}
		usages := ParseStatsOutput(raw)
		if len(usages) == 0 {
			continue
		}
		p.applyUsages(ctx, s.ID, usages)
	}
}

// applyUsages 把 email 维度的用量映射到用户并落库。
func (p *Puller) applyUsages(ctx context.Context, serverID string, usages []UserUsage) {
	// 该服务器上全部入站的客户端，用于 email -> user_id 映射
	inbounds, err := p.store.ListInbounds(ctx, serverID)
	if err != nil {
		p.log.Warn("流量归集：读取入站失败", "server", serverID, "err", err)
		return
	}
	emailToUser := map[string]string{}
	for _, in := range inbounds {
		clients, err := p.store.ListClients(ctx, in.ID)
		if err != nil {
			continue
		}
		for _, c := range clients {
			if c.Email != "" {
				emailToUser[c.Email] = c.UserID
			}
		}
	}

	var applied, unmatched int
	for _, u := range usages {
		uid, ok := emailToUser[u.Email]
		if !ok {
			unmatched++
			continue
		}
		if err := p.store.AddUserTraffic(ctx, uid, u.Up, u.Down); err != nil {
			p.log.Warn("用户流量归集失败", "user", uid, "err", err)
			continue
		}
		applied++
	}
	if applied > 0 || unmatched > 0 {
		p.log.Info("用户流量已归集", "server", serverID,
			"applied", applied, "unmatched_email", unmatched)
	}
}

// ErrNoStats 表示没有取得任何统计（例如 Xray 未启用 stats）。
var ErrNoStats = errors.New("未取得 Xray 统计")
