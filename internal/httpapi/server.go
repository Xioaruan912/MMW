package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"time"

	"github.com/mmx-project/mmx/internal/agentlink"
	"github.com/mmx-project/mmx/internal/backup"
	"github.com/mmx-project/mmx/internal/config"
	"github.com/mmx-project/mmx/internal/mcp"
	"github.com/mmx-project/mmx/internal/notify"
	"github.com/mmx-project/mmx/internal/probe"
	"github.com/mmx-project/mmx/internal/prober"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/xrayctl"
)

// Prober 是本层对 prober 包的最小依赖（接口化便于测试）。
type Prober interface {
	ProbeServer(ctx context.Context, serverID, actor string) (prober.Result, error)
	ProbeAll(ctx context.Context)
}

// Options 是构造 HTTP 层所需的依赖。
type Options struct {
	Log            *slog.Logger
	Store          *storage.Store
	Reg            *agentlink.Registry
	Hub            *agentlink.Hub
	Xray           *xrayctl.Applier
	Probe          *probe.Service
	Notifier       *notify.Notifier
	Backup         *backup.Manager
	MCP            *mcp.Server
	Prober         Prober
	Config         config.MasterConfig
	AgentBinaryDir string // Agent 二进制库存目录；为空则 /api/remote/agent/* 返回 404
	WebFS          fs.FS  // 内嵌前端（可选）
	Version        string
	StartedAt      time.Time
}

// Server 持有 HTTP 层的全部依赖。
type Server struct {
	log            *slog.Logger
	store          *storage.Store
	reg            *agentlink.Registry
	hub            *agentlink.Hub
	xray           *xrayctl.Applier
	probe          *probe.Service
	notifier       *notify.Notifier
	backup         *backup.Manager
	mcp            *mcp.Server
	prober         Prober
	cfg            config.MasterConfig
	agentBinaryDir string
	webFS          fs.FS
	version        string
	startedAt      time.Time
	limiter        *loginLimiter
}

// New 构造 HTTP 层。
func New(o Options) *Server {
	if o.StartedAt.IsZero() {
		o.StartedAt = time.Now()
	}
	return &Server{
		log:            o.Log,
		store:          o.Store,
		reg:            o.Reg,
		hub:            o.Hub,
		xray:           o.Xray,
		probe:          o.Probe,
		notifier:       o.Notifier,
		backup:         o.Backup,
		mcp:            o.MCP,
		prober:         o.Prober,
		cfg:            o.Config,
		agentBinaryDir: o.AgentBinaryDir,
		webFS:          o.WebFS,
		version:        o.Version,
		startedAt:      o.StartedAt,
		limiter:        newLoginLimiter(),
	}
}
