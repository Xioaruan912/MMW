// Command mmx-agent 是部署在受管服务器上的 Agent。
//
// 行为：
//   - 出站连主控（WebSocket），从不监听端口
//   - 周期上报心跳与系统指标
//   - 只执行主控下发的**白名单任务**，且校验 Ed25519 签名
//
// 启动示例：
//
//	mmx-agent -config /var/lib/mmx-agent/config.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mmx-project/mmx/internal/agent"
	"github.com/mmx-project/mmx/internal/agent/link"
	"github.com/mmx-project/mmx/internal/logging"
	"github.com/mmx-project/mmx/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agent 退出:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "/var/lib/mmx-agent/config.yaml", "配置文件路径")
		showVersion = flag.Bool("version", false, "打印版本后退出")
		masterURL   = flag.String("master", "", "主控地址（覆盖配置文件）")
		token       = flag.String("token", "", "一次性入驻令牌（覆盖配置文件）")
		xrayMode    = flag.String("xray-mode", "", "embedded/external/none（覆盖配置文件）")
		debug       = flag.Bool("debug", false, "调试日志")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mmx-agent", version.String())
		return nil
	}

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}
	if *masterURL != "" {
		cfg.MasterURL = *masterURL
	}
	if *token != "" {
		cfg.EnrollToken = *token
	}
	if *xrayMode != "" {
		cfg.XrayMode = *xrayMode
	}
	if *debug {
		cfg.Debug = true
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logRes, err := logging.Setup(logging.Options{
		Debug:    cfg.Debug,
		LogDir:   cfg.StateDir,
		FileName: "agent.log",
	})
	if err != nil {
		// 状态目录不可写时不阻塞启动，退回仅 stdout
		logRes, err = logging.Setup(logging.Options{Debug: cfg.Debug})
		if err != nil {
			return err
		}
	}
	defer func() { _ = logRes.Close() }()
	log := logRes.Logger

	log.Info("MMX Agent 启动中",
		"version", version.String(),
		"master", cfg.MasterURL,
		"xray_mode", cfg.XrayMode,
		"state_dir", cfg.StateDir,
		"enrolled", cfg.AgentToken != "")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	c := link.New(cfg, *configPath, log)
	c.Run(ctx)

	log.Info("Agent 已退出")
	_ = slog.Default
	_ = strings.TrimSpace
	return nil
}
