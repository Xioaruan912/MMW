package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/mmx-project/mmx/pkg/agentproto"
)

// Pull 模式参数。
const (
	pullInterval     = 30 * time.Second // 轮询间隔
	pullRetryWSAfter = 5                // 连续多少次成功轮询后尝试重新连 WS
	pullMaxBatch     = 10               // 单次最多取多少任务
)

// pullRequest / pullResponse 与主控 httpapi.handleAgentPull 对应。
type pullRequest struct {
	Hello     *agentproto.HelloPayload       `json:"hello,omitempty"`
	Heartbeat *agentproto.HeartbeatPayload   `json:"heartbeat,omitempty"`
	Metrics   *agentproto.MetricsPayload     `json:"metrics,omitempty"`
	Results   []agentproto.TaskResultPayload `json:"results,omitempty"`
}

type pullResponse struct {
	Tasks      []agentproto.TaskPayload `json:"tasks"`
	ServerTime int64                    `json:"server_time"`
}

// resultQueue 缓存已完成但尚未上报的任务结果（Pull 模式下没有反向通道）。
type resultQueue struct {
	mu    sync.Mutex
	items []agentproto.TaskResultPayload
}

func (q *resultQueue) push(r agentproto.TaskResultPayload) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= 64 {
		// 极端情况下丢弃最旧的，避免内存无界增长
		q.items = q.items[1:]
	}
	q.items = append(q.items, r)
}

func (q *resultQueue) drain(max int) []agentproto.TaskResultPayload {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	n := len(q.items)
	if max > 0 && n > max {
		n = max
	}
	out := make([]agentproto.TaskResultPayload, n)
	copy(out, q.items[:n])
	q.items = q.items[n:]
	return out
}

// pullSession 用 HTTP 轮询替代 WebSocket：上报 + 取任务 + 执行。
//
// 返回 nil 表示"应该回去重试 WS"（例如连续成功若干轮，或对端已恢复）。
func (c *Client) pullSession(ctx context.Context) error {
	c.log.Info("进入 Pull 降级模式", "interval", pullInterval.String(), "url", c.cfg.MasterHTTPBase()+"/api/agent/pull")

	tick := time.NewTicker(pullInterval)
	defer tick.Stop()

	consecutiveOK := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}

		tasks, err := c.pullOnce(ctx)
		if err != nil {
			consecutiveOK = 0
			c.log.Warn("Pull 失败", "err", err)
			// Pull 也连不上时，退避等待（由外层 Run 的退避逻辑处理更合适，
			// 但这里保持循环以免高频重试打爆主控）
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pullInterval):
			}
			continue
		}
		consecutiveOK++

		for _, t := range tasks {
			res := c.executeTask(t)
			c.results.push(res)
		}

		// 对端已恢复的迹象：连续成功若干轮后回去试 WS
		if consecutiveOK >= pullRetryWSAfter {
			c.log.Info("Pull 模式运行正常，尝试回到 WebSocket")
			return nil
		}
	}
}

// pullOnce 执行一次往返：上报心跳/指标/历史结果，取回新任务。
func (c *Client) pullOnce(ctx context.Context) ([]agentproto.TaskPayload, error) {
	// 心跳序号递增（与 WS 模式一致）
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()

	st := c.xrayMgr.Status()
	req := pullRequest{
		Hello: &agentproto.HelloPayload{
			AgentVersion:    versionString(),
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
		Heartbeat: &agentproto.HeartbeatPayload{
			XrayRunning:   st.Running,
			XrayConfigRev: st.ConfigRev,
			Seq:           seq,
		},
		Results: c.results.drain(pullMaxBatch),
	}
	if m, err := c.sampler.Collect(); err == nil {
		req.Heartbeat.UptimeSec = m.UptimeSec
		req.Metrics = &m
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.MasterHTTPBase()+"/api/agent/pull", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+c.cfg.AgentToken)

	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("token 被拒（HTTP %d）: %s", resp.StatusCode, string(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var out pullResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	if out.ServerTime > 0 {
		c.mu.Lock()
		c.clockSkew = time.Now().UnixMilli() - out.ServerTime
		c.mu.Unlock()
	}
	return out.Tasks, nil
}

var errPullUnavailable = errors.New("pull 通道不可用")
