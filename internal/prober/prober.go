// Package prober 在主控侧周期性驱动节点探测。
//
// 设计要点：
//   - 探测**由 Agent 执行**，主控只下发目标列表并收集结果。
//     主控通常不在节点网络里，直连节点往往不通（节点可能只对特定网段开放）。
//   - 探测目标从数据库推导（服务器地址 + 入站端口），不需要手工维护。
//   - 结果落 node_latency，同一节点保留最新一条 + 历史序列。
package prober

import (
	"context"
	"log/slog"
	"time"

	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// Hub 是本包对 agentlink 的最小依赖。
type Hub interface {
	IsOnline(serverID string) bool
	DispatchTask(ctx context.Context, serverID, kind string, args agentproto.RawArgs, actor string) (agentproto.TaskResultPayload, error)
}

// Prober 周期性地对全部节点做 TCP 探测。
type Prober struct {
	store    *storage.Store
	hub      Hub
	log      *slog.Logger
	interval time.Duration
}

// New 构造 Prober。interval <= 0 时用 5 分钟。
func New(store *storage.Store, hub Hub, log *slog.Logger, interval time.Duration) *Prober {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Prober{store: store, hub: hub, log: log, interval: interval}
}

// Run 阻塞运行直到 ctx 取消。
func (p *Prober) Run(ctx context.Context) {
	// 启动后先等一会再首轮，避免与 Agent 首批接入抢资源
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.ProbeAll(ctx)
		}
	}
}

// ProbeAll 对所有在线服务器执行一轮探测。
func (p *Prober) ProbeAll(ctx context.Context) {
	servers, err := p.store.ListServers(ctx)
	if err != nil {
		p.log.Warn("探测：读取服务器列表失败", "err", err)
		return
	}
	for _, s := range servers {
		if !p.hub.IsOnline(s.ID) {
			continue
		}
		if _, err := p.ProbeServer(ctx, s.ID, "prober"); err != nil {
			p.log.Debug("探测失败", "server", s.ID, "err", err)
		}
	}
}

// Result 是一轮探测的汇总（别名，便于调用方使用简短名字）。
type Result = ProbeResult

// ProbeResult 是一轮探测的汇总。
type ProbeResult struct {
	ServerID string `json:"server_id"`
	Targets  int    `json:"targets"`
	Alive    int    `json:"alive"`
	Skipped  bool   `json:"skipped,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ProbeServer 对某台服务器上的全部节点执行一轮 TCP 探测。
//
// 节点地址取服务器地址（节点是服务器上的监听端口），端口取入站端口。
func (p *Prober) ProbeServer(ctx context.Context, serverID, actor string) (ProbeResult, error) {
	res := ProbeResult{ServerID: serverID}

	srv, err := p.store.GetServer(ctx, serverID)
	if err != nil {
		return res, err
	}
	if srv.XrayMode == storage.XrayModeNone {
		res.Skipped, res.Reason = true, "该服务器未运行 Xray"
		return res, nil
	}
	if !p.hub.IsOnline(serverID) {
		res.Skipped, res.Reason = true, "服务器离线"
		return res, nil
	}

	views, err := p.store.ListNodeViews(ctx, serverID)
	if err != nil {
		return res, err
	}
	host := srv.Address
	if host == "" {
		host = srv.LastIP
	}
	if host == "" {
		res.Skipped, res.Reason = true, "服务器未填写地址"
		return res, nil
	}

	// 组装目标列表；同时记住 tag -> nodeID 的映射用于回写
	targets := make([]map[string]any, 0, len(views))
	tagToNode := map[string]string{}
	for _, v := range views {
		if !v.Enabled || !v.Inbound.Enabled {
			continue
		}
		tag := v.ID // 用节点 ID 作 tag，避免节点名重复导致关联错乱
		tagToNode[tag] = v.ID
		targets = append(targets, map[string]any{
			"tag":  tag,
			"host": host,
			"port": v.Inbound.Port,
		})
	}
	if len(targets) == 0 {
		res.Skipped, res.Reason = true, "没有启用中的节点"
		return res, nil
	}
	res.Targets = len(targets)

	taskRes, err := p.hub.DispatchTask(ctx, serverID, agentproto.TaskProbeTCPPing,
		agentproto.RawArgs{"targets": targets}, actor)
	if err != nil {
		return res, err
	}
	if taskRes.Status != agentproto.TaskStatusOK {
		res.Reason = taskRes.Error
		return res, nil
	}

	raw, ok := taskRes.Result["results"]
	if !ok {
		res.Reason = "Agent 未返回探测结果"
		return res, nil
	}
	items, ok := raw.([]any)
	if !ok {
		res.Reason = "Agent 返回的探测结果格式异常"
		return res, nil
	}

	now := time.Now().UnixMilli()
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := m["tag"].(string)
		nodeID, ok := tagToNode[tag]
		if !ok {
			continue
		}
		alive, _ := m["alive"].(bool)
		latency := int(toInt64(m["latency_ms"]))
		errMsg, _ := m["error"].(string)
		if err := p.store.AddNodeLatency(ctx, storage.NodeLatency{
			NodeID: nodeID, ServerID: serverID, TS: now, Method: "tcp",
			Alive: alive, LatencyMS: latency, Error: errMsg,
		}); err != nil {
			p.log.Warn("写入探测结果失败", "node", nodeID, "err", err)
			continue
		}
		if alive {
			res.Alive++
		}
	}
	p.log.Info("节点探测完成", "server", serverID,
		"targets", res.Targets, "alive", res.Alive)
	return res, nil
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
