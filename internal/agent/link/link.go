// Package link 实现 Agent 与主控之间的连接、心跳、指标上报与任务执行。
package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mmx-project/mmx/internal/agent"
	"github.com/mmx-project/mmx/internal/agent/collect"
	"github.com/mmx-project/mmx/internal/agent/probe"
	"github.com/mmx-project/mmx/internal/agent/xray"
	"github.com/mmx-project/mmx/internal/version"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// readIdleTimeout 是客户端读超时：主控每 30s ping 一次，这里留 3 倍余量。
const readIdleTimeout = 90 * time.Second

// Client 是 Agent 的主循环。
type Client struct {
	cfg     agent.Config
	cfgPath string
	log     *slog.Logger
	sampler *collect.Sampler
	sysinfo collect.SysInfo
	xrayMgr xray.Manager
	http    *http.Client

	mu        sync.RWMutex
	masterPub ed25519.PublicKey
	heartbeat time.Duration
	metrics   time.Duration
	seq       uint64
	configRev string
	clockSkew int64 // 本地时钟相对主控的偏移（毫秒）
	results   resultQueue
}

// New 构造 Client。
func New(cfg agent.Config, cfgPath string, log *slog.Logger) *Client {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify}
	return &Client{
		cfg:     cfg,
		cfgPath: cfgPath,
		log:     log,
		sampler: collect.New(),
		sysinfo: collect.GatherSysInfo(),
		xrayMgr: xray.New(cfg.XrayMode, cfg.XrayBinPath, cfg.XrayConfigDir, cfg.XrayService),
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
		heartbeat: cfg.HeartbeatInterval,
		metrics:   cfg.MetricsInterval,
	}
}

// Run 阻塞运行，直到 ctx 被取消。内部自动重连，不向外抛错。
//
// 降级策略：WebSocket 连续失败 3 次 → 切到 HTTP Pull 轮询；
// Pull 连续成功若干轮后自动尝试回到 WebSocket（带宽与延迟都更优）。
func (c *Client) Run(ctx context.Context) {
	backoff := c.cfg.ReconnectMin
	wsFailures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			wsFailures++
			c.log.Warn("连接中断", "err", err, "ws_failures", wsFailures,
				"session_dur", time.Since(start).Round(time.Second).String())
		} else {
			wsFailures = 0
		}

		if wsFailures >= 3 {
			c.log.Warn("WebSocket 连续失败，切换到 Pull 降级模式", "failures", wsFailures)
			if err := c.pullSession(ctx); err != nil {
				c.log.Warn("Pull 模式退出", "err", err)
			}
			wsFailures = 0
			backoff = c.cfg.ReconnectMin
			continue
		}

		// 会话持续时间够长则重置退避，避免抖动时一路涨到上限
		if time.Since(start) > 60*time.Second {
			backoff = c.cfg.ReconnectMin
		}
		jitter := time.Duration(rand.Int63n(int64(backoff/5 + 1)))
		wait := backoff + jitter
		c.log.Info("准备重连", "in", wait.Round(time.Second).String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}
	}
}

// versionString 返回供 hello 携带的 Agent 版本号。
func versionString() string { return version.Version }

// ---------------------------------------------------------------------------
// 单次会话
// ---------------------------------------------------------------------------

func (c *Client) session(ctx context.Context) error {
	if c.cfg.AgentToken == "" {
		if err := c.enroll(ctx); err != nil {
			return fmt.Errorf("入驻失败: %w", err)
		}
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 20 * time.Second,
		TLSClientConfig:  &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.cfg.InsecureSkipVerify},
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.cfg.AgentToken)

	ws, resp, err := dialer.DialContext(ctx, c.cfg.MasterWSURL(), header)
	if err != nil {
		if resp != nil {
			body := make([]byte, 512)
			n, _ := resp.Body.Read(body)
			_ = resp.Body.Close()
			return fmt.Errorf("握手失败 HTTP %d: %s", resp.StatusCode, string(body[:n]))
		}
		return fmt.Errorf("握手失败: %w", err)
	}
	defer ws.Close()

	c.log.Info("已连接主控", "url", c.cfg.MasterURL, "server_id", c.cfg.ServerID)

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan agentproto.Envelope, 32)
	writeErr := make(chan error, 1)
	go c.writeLoop(sctx, ws, out, writeErr)

	// 首帧：hello
	if err := c.sendHello(out); err != nil {
		return err
	}

	hbTick := time.NewTicker(c.heartbeat)
	defer hbTick.Stop()
	mTick := time.NewTicker(c.metrics)
	defer mTick.Stop()

	// 读超时：主控每 30s 发一次 ping，我们在 PingHandler 里续期并回 pong。
	_ = ws.SetReadDeadline(time.Now().Add(readIdleTimeout))
	ws.SetPingHandler(func(appData string) error {
		_ = ws.SetReadDeadline(time.Now().Add(readIdleTimeout))
		return ws.WriteControl(websocket.PongMessage, []byte(appData),
			time.Now().Add(10*time.Second))
	})
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(readIdleTimeout))
	})

	readErr := make(chan error, 1)
	go func() {
		for {
			var env agentproto.Envelope
			if err := ws.ReadJSON(&env); err != nil {
				readErr <- err
				return
			}
			c.handleIncoming(env, out)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case err := <-writeErr:
			return err
		case <-hbTick.C:
			if err := c.sendHeartbeat(out); err != nil {
				return err
			}
		case <-mTick.C:
			if err := c.sendMetrics(out); err != nil {
				c.log.Warn("指标采集失败", "err", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

func (c *Client) sendHello(out chan<- agentproto.Envelope) error {
	st := c.xrayMgr.Status()
	p := agentproto.HelloPayload{
		AgentVersion:    version.Version,
		ProtocolVersion: agentproto.Version,
		Hostname:        c.sysinfo.Hostname,
		OS:              c.sysinfo.OS,
		Arch:            c.sysinfo.Arch,
		Kernel:          c.sysinfo.Kernel,
		Distro:          c.sysinfo.Distro,
		BootID:          c.sysinfo.BootID,
		XrayMode:        c.xrayMgr.Mode(),
		XrayVersion:     st.Version,
		Capabilities:    []string{"metrics", "xray_control", "probe_collect"},
	}
	env, err := agentproto.NewEnvelope(agentproto.TypeHello, p)
	if err != nil {
		return err
	}
	return send(out, env)
}

func (c *Client) sendHeartbeat(out chan<- agentproto.Envelope) error {
	st := c.xrayMgr.Status()
	var uptime int64
	if m, err := c.sampler.Collect(); err == nil {
		uptime = m.UptimeSec
	}
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()

	env, err := agentproto.NewEnvelope(agentproto.TypeHeartbeat, agentproto.HeartbeatPayload{
		UptimeSec:     uptime,
		XrayRunning:   st.Running,
		XrayConfigRev: st.ConfigRev,
		Seq:           seq,
	})
	if err != nil {
		return err
	}
	return send(out, env)
}

func (c *Client) sendMetrics(out chan<- agentproto.Envelope) error {
	m, err := c.sampler.Collect()
	if err != nil {
		return err
	}
	env, err := agentproto.NewEnvelope(agentproto.TypeMetrics, m)
	if err != nil {
		return err
	}
	return send(out, env)
}

func send(out chan<- agentproto.Envelope, env agentproto.Envelope) error {
	select {
	case out <- env:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("发送队列拥塞")
	}
}

func (c *Client) writeLoop(ctx context.Context, ws *websocket.Conn, out <-chan agentproto.Envelope, errc chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case env := <-out:
			_ = ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if err := ws.WriteJSON(env); err != nil {
				errc <- err
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 接收与任务执行
// ---------------------------------------------------------------------------

func (c *Client) handleIncoming(env agentproto.Envelope, out chan<- agentproto.Envelope) {
	switch env.Type {
	case agentproto.TypeHelloAck:
		var p agentproto.HelloAckPayload
		if err := env.Decode(&p); err != nil {
			c.log.Warn("hello_ack 解析失败", "err", err)
			return
		}
		c.applyHelloAck(p)
		c.log.Info("握手完成",
			"server", p.ServerID, "name", p.ServerName,
			"heartbeat", fmt.Sprintf("%ds", p.HeartbeatIntervalSec),
			"metrics", fmt.Sprintf("%ds", p.MetricsIntervalSec))

	case agentproto.TypeTask:
		var task agentproto.TaskPayload
		if err := env.Decode(&task); err != nil {
			c.log.Warn("任务解析失败", "err", err)
			return
		}
		go func() {
			res := c.executeTask(task)
			renv, err := agentproto.NewEnvelope(agentproto.TypeTaskResult, res)
			if err != nil {
				c.log.Warn("任务回执构造失败", "err", err)
				return
			}
			_ = send(out, renv)
		}()

	case agentproto.TypeConfig:
		var cfg agentproto.ConfigPayload
		if err := env.Decode(&cfg); err != nil {
			c.log.Warn("配置解析失败", "err", err)
			return
		}
		go c.applyConfig(cfg, out)

	case agentproto.TypeHeartbeatAck, agentproto.TypeAck:
		// 无需处理

	case agentproto.TypeError:
		var p agentproto.ErrorPayload
		_ = env.Decode(&p)
		c.log.Error("主控返回错误", "code", p.Code, "message", p.Message)

	default:
		c.log.Debug("忽略未知消息", "type", env.Type)
	}
}

func (c *Client) applyHelloAck(p agentproto.HelloAckPayload) {
	c.mu.Lock()
	if p.HeartbeatIntervalSec > 0 {
		c.heartbeat = time.Duration(p.HeartbeatIntervalSec) * time.Second
	}
	if p.MetricsIntervalSec > 0 {
		c.metrics = time.Duration(p.MetricsIntervalSec) * time.Second
	}
	if p.MasterPublicKey != "" {
		if pub, err := agentproto.DecodePublicKey(p.MasterPublicKey); err == nil {
			c.masterPub = pub
		}
	}
	if p.ServerTime > 0 {
		// 记录与主控的时钟偏差；用于日志与任务 deadline 判断
		c.clockSkew = time.Now().UnixMilli() - p.ServerTime
	}
	skew := c.clockSkew
	c.mu.Unlock()

	if skew > 30_000 || skew < -30_000 {
		c.log.Warn("本机时钟与主控偏差较大，建议校时", "skew_ms", skew)
	}

	if p.ServerID != "" && p.ServerID != c.cfg.ServerID {
		c.cfg.ServerID = p.ServerID
		if err := c.cfg.SaveState(c.cfgPath); err != nil {
			c.log.Warn("保存 server_id 失败", "err", err)
		}
	}
}

func (c *Client) executeTask(task agentproto.TaskPayload) agentproto.TaskResultPayload {
	start := time.Now().UnixMilli()
	res := agentproto.TaskResultPayload{TaskID: task.TaskID, StartedAt: start}

	defer func() {
		if r := recover(); r != nil {
			res.Status = agentproto.TaskStatusFailed
			res.Error = fmt.Sprintf("任务执行 panic: %v", r)
			res.FinishedAt = time.Now().UnixMilli()
		}
	}()

	// 1) 白名单
	if !agentproto.KnownTask(task.Kind) {
		res.Status = agentproto.TaskStatusRejected
		res.Error = "任务不在白名单内: " + task.Kind
		res.FinishedAt = time.Now().UnixMilli()
		return res
	}
	// 2) 签名（主控公钥未知时只告警不阻断，便于首次联调；生产环境应改为强制）
	c.mu.RLock()
	pub := c.masterPub
	c.mu.RUnlock()
	if len(pub) > 0 {
		if err := agentproto.VerifyTask(pub, &task); err != nil {
			c.log.Error("任务验签失败，拒绝执行", "task", task.TaskID, "err", err)
			res.Status = agentproto.TaskStatusRejected
			res.Error = "签名校验失败: " + err.Error()
			res.FinishedAt = time.Now().UnixMilli()
			return res
		}
	} else {
		c.log.Warn("尚未取得主控公钥，跳过任务验签", "task", task.TaskID)
	}
	// 3) 参数
	if err := agentproto.ValidateTask(task.Kind, task.Args); err != nil {
		res.Status = agentproto.TaskStatusRejected
		res.Error = err.Error()
		res.FinishedAt = time.Now().UnixMilli()
		return res
	}

	out, resMap, err := c.runTask(task)
	res.FinishedAt = time.Now().UnixMilli()
	res.Output = truncate(out, 8000)
	res.Result = resMap
	if err != nil {
		res.Status = agentproto.TaskStatusFailed
		res.Error = err.Error()
	} else {
		res.Status = agentproto.TaskStatusOK
	}
	c.log.Info("任务执行结束", "task", task.TaskID, "kind", task.Kind,
		"status", res.Status, "dur_ms", res.FinishedAt-res.StartedAt)
	return res
}

func (c *Client) runTask(task agentproto.TaskPayload) (string, agentproto.RawArgs, error) {
	switch task.Kind {
	case agentproto.TaskXrayStart:
		return "", nil, c.xrayMgr.Control("start")
	case agentproto.TaskXrayStop:
		return "", nil, c.xrayMgr.Control("stop")
	case agentproto.TaskXrayRestart:
		return "", nil, c.xrayMgr.Control("restart")

	case agentproto.TaskXrayApplyConfig:
		cfgMap := task.Args.GetMap("config")
		rev := task.Args.GetString("rev")
		if cfgMap == nil {
			return "", nil, errors.New("缺少 config")
		}
		raw, err := json.Marshal(cfgMap)
		if err != nil {
			return "", nil, err
		}
		var cfg agentproto.XrayConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", nil, fmt.Errorf("config 结构不符: %w", err)
		}
		if err := c.xrayMgr.ApplyConfig(&cfg, rev); err != nil {
			return "", nil, err
		}
		st := c.xrayMgr.Status()
		return "配置已应用", agentproto.RawArgs{"rev": st.ConfigRev, "running": st.Running}, nil

	case agentproto.TaskXrayInstall:
		mode := task.Args.GetString("mode")
		if mode == "embedded" && c.xrayMgr.Mode() != "embedded" {
			return "", nil, errors.New("当前 Agent 以 external 模式运行，无法切换到内嵌模式（需重新安装 Agent）")
		}
		if mode == "embedded" {
			return "", nil, xray.ErrNotImplemented
		}
		res, err := c.xrayMgr.Install(context.Background(), xray.InstallOptions{
			Version:  task.Args.GetString("version"),
			BaseURL:  task.Args.GetString("base_url"),
			SHA256:   task.Args.GetString("sha256"),
			AssetDir: task.Args.GetString("asset_dir"),
		})
		if err != nil {
			return "", nil, err
		}
		out := fmt.Sprintf("Xray %s 已安装\n  二进制: %s\n  geo 数据: %s\n  服务托管: %s\n  sha256: %s\n  已启动: %v",
			res.Version, res.BinaryPath, res.AssetDir, res.ServiceMode, res.SHA256, res.Started)
		return out, agentproto.RawArgs{
			"version": res.Version, "sha256": res.SHA256,
			"service_mode": res.ServiceMode, "started": res.Started,
		}, nil

	case agentproto.TaskXrayUninstall:
		return "", nil, c.xrayMgr.Uninstall()

	case agentproto.TaskNginxInstall, agentproto.TaskNginxUninstall:
		return "", nil, xray.ErrNotImplemented

	case agentproto.TaskXrayQueryStats:
		raw, err := c.xrayMgr.QueryStats(context.Background(), xray.StatsQueryOptions{
			Pattern: task.Args.GetString("pattern"),
			Reset:   task.Args.GetBool("reset"),
		})
		if err != nil {
			return "", nil, err
		}
		return "统计已拉取（" + fmt.Sprintf("%d 字节", len(raw)) + "）",
			agentproto.RawArgs{"raw": raw}, nil

	case agentproto.TaskProbeTCPPing:
		targets, err := parseTargets(task.Args)
		if err != nil {
			return "", nil, err
		}
		results := probe.TCPPing(context.Background(), targets, probe.Options{
			Timeout:     time.Duration(task.Args.GetInt("timeout_ms")) * time.Millisecond,
			Concurrency: int(task.Args.GetInt("concurrency")),
			Samples:     int(task.Args.GetInt("samples")),
		})
		alive := 0
		for _, r := range results {
			if r.Alive {
				alive++
			}
		}
		return fmt.Sprintf("已探测 %d 个目标，%d 个可达", len(results), alive),
			agentproto.RawArgs{"results": results}, nil

	case agentproto.TaskSpeedtestRun:
		tg := task.Args.GetMap("target")
		target := probe.Target{
			Tag:  argStr(tg, "tag"),
			Host: argStr(tg, "host"),
			Port: int(argNum(tg, "port")),
		}
		res, err := probe.RunSpeedtest(context.Background(), probe.SpeedtestOptions{
			BinPath:     task.Args.GetString("bin_path"),
			ConfigPath:  task.Args.GetString("config_path"),
			Target:      target,
			DownloadURL: task.Args.GetString("download_url"),
			SHA256:      task.Args.GetString("sha256"),
		})
		if err != nil {
			return res.Raw, agentproto.RawArgs{
				"alive": res.Alive, "error": res.Error,
			}, err
		}
		return fmt.Sprintf("测速完成：延迟 %dms / 下行 %d B/s", res.LatencyMS, res.DownloadBPS),
			agentproto.RawArgs{
				"alive": res.Alive, "latency_ms": res.LatencyMS,
				"download_bps": res.DownloadBPS, "upload_bps": res.UploadBPS,
				"raw": res.Raw,
			}, nil

	case agentproto.TaskProbeCollectNow:
		m, err := c.sampler.Collect()
		if err != nil {
			return "", nil, err
		}
		return "已采集", agentproto.RawArgs{"ts": m.TS, "cpu": m.CPUPercent}, nil

	case agentproto.TaskAgentRotateToken:
		nt := task.Args.GetString("new_token")
		if nt == "" {
			return "", nil, errors.New("缺少 new_token")
		}
		c.cfg.AgentToken = nt
		if err := c.cfg.SaveState(c.cfgPath); err != nil {
			return "", nil, err
		}
		return "令牌已更新，下次重连生效", nil, nil

	case agentproto.TaskAgentUpgrade:
		return "", nil, errors.New("自升级尚未实现（P6）")

	case agentproto.TaskACMESDeployCert:
		return "", nil, errors.New("证书部署尚未实现（P7）")

	case agentproto.TaskSysReboot:
		return "", nil, errors.New("重启需在 P6 引入二次确认后开放")

	default:
		return "", nil, errors.New("未实现的任务: " + task.Kind)
	}
}

// Ack 构造一个信封（供 keepalive 使用）。
func Ack(out chan<- agentproto.Envelope, id string) agentproto.Envelope {
	env, err := agentproto.NewEnvelope(agentproto.TypeAck, agentproto.AckPayload{ID: id})
	if err != nil {
		return agentproto.Envelope{Type: agentproto.TypeAck, V: agentproto.Version, ID: agentproto.NewID()}
	}
	return env
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}

var _ = context.Background
var _ = json.Marshal
var _ = bytes.NewReader

var (
	_ = ed25519.PublicKeySize
	_ = sync.Once{}
)

// parseTargets 从任务参数里解析探测目标列表。
func parseTargets(args agentproto.RawArgs) ([]probe.Target, error) {
	raw, ok := args["targets"]
	if !ok {
		return nil, errors.New("缺少 targets")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("targets 必须是数组")
	}
	out := make([]probe.Target, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("targets[%d] 不是对象", i)
		}
		t := probe.Target{
			Tag:  argStr(m, "tag"),
			Host: argStr(m, "host"),
			Port: int(argNum(m, "port")),
		}
		if t.Host == "" || t.Port <= 0 || t.Port > 65535 {
			return nil, fmt.Errorf("targets[%d] 的 host/port 非法", i)
		}
		out = append(out, t)
	}
	return out, nil
}

// argStr / argNum 是任务参数里读嵌套字段的小工具。
func argStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func argNum(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}
