package agentlink

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper 周期性地把超时未上报的服务器标记为离线，并清理过期数据。
type Sweeper struct {
	log      *slog.Logger
	reg      *Registry
	hub      *Hub
	interval time.Duration
	pruneAge time.Duration
}

// NewSweeper 构造清理器。interval 为扫描周期，pruneAge 为指标保留时长。
func NewSweeper(log *slog.Logger, reg *Registry, hub *Hub, interval, pruneAge time.Duration) *Sweeper {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if pruneAge <= 0 {
		pruneAge = 30 * 24 * time.Hour
	}
	return &Sweeper{log: log, reg: reg, hub: hub, interval: interval, pruneAge: pruneAge}
}

// Run 阻塞运行，直到 ctx 被取消。
func (s *Sweeper) Run(ctx context.Context) {
	tick := time.NewTicker(s.interval)
	defer tick.Stop()

	prune := time.NewTicker(6 * time.Hour)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.sweepOffline(ctx)
			s.expireTasks(ctx)
		case <-prune.C:
			s.prune(ctx)
		}
	}
}

func (s *Sweeper) sweepOffline(ctx context.Context) {
	srv, err := s.reg.Store().ListServers(ctx)
	if err != nil {
		s.log.Warn("离线扫描读取服务器失败", "err", err)
		return
	}
	now := time.Now()
	hb := s.reg.HeartbeatInterval()
	factor := s.reg.OfflineFactor()

	for _, sv := range srv {
		// 有活跃 WS 连接的一律算在线，避免时钟漂移误判
		if s.hub.IsOnline(sv.ID) {
			if sv.Status != "online" {
				_ = s.reg.Store().SetServerStatus(ctx, sv.ID, "online", "")
			}
			continue
		}
		online := sv.Online(now, hb, factor)
		want := "offline"
		if online {
			want = "online"
		}
		if sv.Status != want {
			_ = s.reg.Store().SetServerStatus(ctx, sv.ID, want, "")
			s.log.Info("服务器状态变更", "server", sv.ID, "name", sv.Name, "→", want)
		}
	}
}

func (s *Sweeper) prune(ctx context.Context) {
	st := s.reg.Store()
	if n, err := st.PruneMetrics(ctx, time.Now().Add(-s.pruneAge)); err == nil && n > 0 {
		s.log.Info("清理历史指标", "rows", n)
	}
	if n, err := st.PruneSessions(ctx); err == nil && n > 0 {
		s.log.Info("清理过期会话", "rows", n)
	}
	if n, err := st.PruneTasks(ctx, 7*24*time.Hour); err == nil && n > 0 {
		s.log.Info("清理历史任务", "rows", n)
	}
}

// expireTasks 把长时间未收到回执的任务结算为超时。
//
// 触发场景：Agent 掉线、主控重启、回执丢失。不做这一步就会留下永久的 dispatched 记录。
func (s *Sweeper) expireTasks(ctx context.Context) {
	n, err := s.reg.Store().ExpireStaleTasks(ctx)
	if err != nil {
		s.log.Warn("任务超时结算失败", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("已结算超时任务", "rows", n)
	}
}
