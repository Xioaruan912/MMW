package link

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/mmx-project/mmx/internal/version"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// enrollReq / enrollResp 与主控侧 httpapi.handleAgentEnroll 对应。
type enrollReq struct {
	EnrollToken string                  `json:"enroll_token"`
	Hello       agentproto.HelloPayload `json:"hello"`
}

type enrollResp struct {
	ServerID           string `json:"server_id"`
	ServerName         string `json:"server_name"`
	AgentToken         string `json:"agent_token"`
	MasterPublicKey    string `json:"master_public_key"`
	ProtocolVersion    int    `json:"protocol_version"`
	HeartbeatIntervalS int    `json:"heartbeat_interval_sec"`
	MetricsIntervalS   int    `json:"metrics_interval_sec"`
	ServerTime         int64  `json:"server_time"`
}

// enroll 用一次性入驻令牌换取长期 agent 令牌，并把结果写回配置文件。
func (c *Client) enroll(ctx context.Context) error {
	if c.cfg.EnrollToken == "" {
		return fmt.Errorf("缺少 enroll_token，且没有可用的 agent_token")
	}
	st := c.xrayMgr.Status()
	req := enrollReq{
		EnrollToken: c.cfg.EnrollToken,
		Hello: agentproto.HelloPayload{
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
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url := c.cfg.MasterHTTPBase() + "/api/agent/enroll"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var out enrollResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("响应解析失败: %w", err)
	}
	if out.AgentToken == "" {
		return fmt.Errorf("主控未返回 agent_token")
	}

	c.cfg.AgentToken = out.AgentToken
	c.cfg.ServerID = out.ServerID
	c.cfg.EnrollToken = "" // 一次性令牌用完即弃
	if err := c.cfg.SaveState(c.cfgPath); err != nil {
		return fmt.Errorf("保存状态失败: %w", err)
	}
	c.log.Info("入驻成功", "server_id", out.ServerID, "name", out.ServerName,
		"state_saved", c.cfgPath)
	return nil
}

// applyConfig 幂等地把期望配置落到本机。
func (c *Client) applyConfig(cfg agentproto.ConfigPayload, out chan<- agentproto.Envelope) {
	st := c.xrayMgr.Status()
	if cfg.Rev != "" && cfg.Rev == st.ConfigRev {
		c.log.Debug("配置版本一致，跳过", "rev", cfg.Rev)
		return
	}
	if cfg.Xray == nil {
		c.log.Debug("配置未包含 xray 段")
		return
	}
	if err := c.xrayMgr.ApplyConfig(cfg.Xray, cfg.Rev); err != nil {
		c.log.Error("应用配置失败", "rev", cfg.Rev, "err", err)
		return
	}
	c.mu.Lock()
	c.configRev = cfg.Rev
	c.mu.Unlock()

	after := c.xrayMgr.Status()
	c.log.Info("配置已生效", "rev", cfg.Rev, "running", after.Running, "pid", after.PID)

	// 回报一个轻量 ack，让主控知道新 rev 已落地
	env, err := agentproto.NewEnvelope(agentproto.TypeAck, agentproto.AckPayload{ID: cfg.Rev})
	if err == nil {
		_ = send(out, env)
	}
}
