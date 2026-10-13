package httpapi

import (
	"io/fs"
	"log/slog"
	"time"

	"github.com/mmx-project/mmx/internal/agentlink"
	"github.com/mmx-project/mmx/internal/config"
	"github.com/mmx-project/mmx/internal/probe"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/xrayctl"
)

// Options 是构造 HTTP 层所需的依赖。
type Options struct {
	Log            *slog.Logger
	Store          *storage.Store
	Reg            *agentlink.Registry
	Hub            *agentlink.Hub
	Xray           *xrayctl.Applier
	Probe          *probe.Service
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
		cfg:            o.Config,
		agentBinaryDir: o.AgentBinaryDir,
		webFS:          o.WebFS,
		version:        o.Version,
		startedAt:      o.StartedAt,
		limiter:        newLoginLimiter(),
	}
}
