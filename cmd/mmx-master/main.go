// Command mmx-master 是 MMX 的主控进程。
//
// 职责：面板 API、Agent 网关、订阅出口、探针、MCP。
//
// 启动示例：
//
//	MMX_DATA_DIR=/var/lib/mmx MMX_PUBLIC_URL=https://panel.example.com ./mmx-master
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmx-project/mmx/internal/agentlink"
	"github.com/mmx-project/mmx/internal/backup"
	"github.com/mmx-project/mmx/internal/config"
	"github.com/mmx-project/mmx/internal/httpapi"
	"github.com/mmx-project/mmx/internal/logging"
	"github.com/mmx-project/mmx/internal/mcp"
	"github.com/mmx-project/mmx/internal/notify"
	"github.com/mmx-project/mmx/internal/probe"
	"github.com/mmx-project/mmx/internal/ruleengine"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/traffic"
	"github.com/mmx-project/mmx/internal/version"
	"github.com/mmx-project/mmx/internal/webui"
	"github.com/mmx-project/mmx/internal/xrayctl"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()
	if *showVersion {
		fmt.Println("mmx-master", version.String())
		return nil
	}

	cfg, err := config.LoadMasterConfig()
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("创建数据目录: %w", err)
	}

	logRes, err := logging.Setup(logging.Options{
		Debug:  cfg.Debug,
		LogDir: cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("初始化日志: %w", err)
	}
	defer func() { _ = logRes.Close() }()
	log := logRes.Logger
	log.Info("MMX 主控启动中", "version", version.String(), "data_dir", cfg.DataDir)

	db, err := storage.Open(cfg.DatabaseDriver, cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := storage.New(ctx, db)
	if err != nil {
		return fmt.Errorf("初始化存储: %w", err)
	}
	log.Info("数据库就绪", "driver", cfg.DatabaseDriver, "dsn", cfg.DatabaseDSN)

	// 主控签名密钥：用于给下发给 Agent 的任务签名
	pub, priv, created, err := agentlink.LoadOrCreateMasterKeyIfNeeded(cfg.MasterKeyPath)
	if err != nil {
		return fmt.Errorf("加载主控密钥: %w", err)
	}
	if created {
		log.Info("已生成主控签名密钥", "path", cfg.MasterKeyPath,
			"public_key", agentlink.EncodePublicKeyShort(pub))
	}

	reg := agentlink.NewRegistry(agentlink.RegistryOptions{
		Store:         store,
		Heartbeat:     cfg.AgentHeartbeatInterval,
		Metrics:       cfg.AgentMetricsInterval,
		OfflineFactor: cfg.AgentOfflineFactor,
		PublicKey:     pub,
	})
	hub := agentlink.NewHub(log, store, reg, priv, cfg.AgentTokenReuseLock, cfg.TrustProxy)

	// 流量归集：指标里的服务器增量 + 定期向 Agent 拉取每用户用量
	trafficAgg := traffic.NewAggregator(store, log)
	hub.OnMetrics = trafficAgg.OnMetrics
	puller := traffic.NewPuller(store, hub, log, 5*time.Minute)
	go puller.Run(ctx)

	sweeper := agentlink.NewSweeper(log, reg, hub, 30*time.Second, 30*24*time.Hour)
	go sweeper.Run(ctx)

	if err := ruleengine.EnsureBuiltins(ctx, store, log); err != nil {
		return fmt.Errorf("写入内置订阅模板失败: %w", err)
	}

	probeSvc := probe.NewService(store, hub.IsOnline)
	notifier := notify.New(store, log)
	backupMgr := backup.NewManager(db.DB, cfg.DataDir, cfg.DatabaseDriver)

	// 启动时应用待恢复的数据库（备份恢复不在运行时原地替换，见 internal/backup）
	if applied, err := backup.ApplyPendingRestore(cfg.DataDir, cfg.DatabaseDSN); err != nil {
		return fmt.Errorf("应用待恢复数据库失败: %w", err)
	} else if applied {
		log.Warn("已应用待恢复的数据库备份，旧库已留档")
	}

	// 通知钩子：服务器上下线、任务失败
	hub.OnConnect = func(serverID string) {
		notifier.Notify(ctx, notify.Event{
			Kind: notify.EventServerOnline, Level: "info",
			Title: "服务器已上线", Message: "服务器 " + serverID + " 的 Agent 已接入",
			ServerID: serverID, Timestamp: time.Now(),
		})
	}

	mcpSrv := mcp.New(log, mcp.Tools(mcp.Deps{
		Store:  store,
		Hub:    hub,
		Xray:   xrayctl.NewApplier(store, hub, xrayctl.DefaultOptions()),
		Online: hub.IsOnline,
	}))

	api := httpapi.New(httpapi.Options{
		Log:            log,
		Store:          store,
		Reg:            reg,
		Hub:            hub,
		Xray:           xrayctl.NewApplier(store, hub, xrayctl.DefaultOptions()),
		Probe:          probeSvc,
		Notifier:       notifier,
		Backup:         backupMgr,
		MCP:            mcpSrv,
		Config:         cfg,
		AgentBinaryDir: os.Getenv("MMX_AGENT_BINARY_DIR"),
		WebFS:          webui.FS(), // 前端 embed 进二进制；未构建时为占位页
		Version:        version.Version,
		StartedAt:      time.Now(),
	})

	srv := &http.Server{
		Handler:           api.Routes(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// 不用 WriteTimeout：WebSocket 与 SSE 是长连接
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", cfg.Listen, err)
	}
	log.Info("HTTP 已监听",
		"addr", cfg.Listen,
		"public_url", cfg.BaseURL(),
		"health", cfg.BaseURL()+"/api/health")

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("收到退出信号，正在优雅关闭…")
	case err := <-errc:
		log.Error("HTTP 服务异常退出", "err", err)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("关闭 HTTP: %w", err)
	}
	log.Info("已退出")
	_ = slog.Default
	return nil
}
