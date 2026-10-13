// Package probe 在 Agent 侧执行节点探测。
//
// 两种探测：
//
//	tcp       —— 对节点地址做 TCP 握手测延迟。零依赖、立即可用，
//	             能真实反映"端口通不通、RTT 多少"，但不覆盖完整协议握手。
//	speedtest —— 调用外部测速程序走完整协议跑吞吐。程序路径与下载地址都可配置，
//	             MMX 不分发第三方二进制。
package probe

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Target 是一个待探测目标。
type Target struct {
	// Tag 是节点标识（回传给主控用于关联）
	Tag string `json:"tag"`
	// Host / Port 是节点监听地址
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Result 是一次探测结果。
type Result struct {
	Tag       string `json:"tag"`
	Alive     bool   `json:"alive"`
	LatencyMS int    `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// Options 控制探测行为。
type Options struct {
	// Timeout 是单目标超时，默认 3 秒
	Timeout time.Duration
	// Concurrency 是并发度，默认 8
	Concurrency int
	// Samples 是每个目标采样次数，取最小值（抖动时更能反映最好情况），默认 1
	Samples int
}

func (o *Options) apply() {
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.Samples <= 0 {
		o.Samples = 1
	}
}

// TCPPing 对一批目标做 TCP 握手探测。
//
// 并发执行但**保持结果顺序与输入一致** —— 主控侧按 tag 关联，
// 顺序错乱会让日志与前端展示难以比对。
func TCPPing(ctx context.Context, targets []Target, opts Options) []Result {
	opts.apply()
	out := make([]Result, len(targets))

	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(idx int, tg Target) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[idx] = Result{Tag: tg.Tag, Alive: false, Error: "已取消"}
				return
			}
			defer func() { <-sem }()
			out[idx] = pingOne(ctx, tg, opts)
		}(i, t)
	}
	wg.Wait()
	return out
}

func pingOne(ctx context.Context, t Target, opts Options) Result {
	res := Result{Tag: t.Tag}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))

	// 用独立的 success 标记而不是「best == 0」做哨兵：
	// 环回或极低延迟的连接耗时会 < 1ms，Milliseconds() 得到 0，
	// 那样会把「可达且极快」误判成「不可达」。
	var (
		best    time.Duration
		success bool
		lastErr error
	)
	for i := 0; i < opts.Samples; i++ {
		if ctx.Err() != nil {
			res.Error = "已取消"
			return res
		}
		dialCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		start := time.Now()
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		if !success || elapsed < best {
			best = elapsed
			success = true
		}
	}
	if !success {
		res.Alive = false
		if lastErr != nil {
			res.Error = friendlyDialError(lastErr)
		} else {
			res.Error = "不可达"
		}
		return res
	}
	res.Alive = true
	res.LatencyMS = int(best.Milliseconds())
	return res
}

// friendlyDialError 把底层错误转成运维能直接看懂的原因。
//
// 直接回 "dial tcp ...: connect: connection refused" 对面板使用者没有意义，
// 而"端口拒绝连接"能立刻定位到"Xray 没起"或"防火墙"。
func friendlyDialError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "端口拒绝连接（服务未启动或被防火墙拒绝）"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return "连接超时（端口未开放或被丢包）"
	case strings.Contains(msg, "no such host"):
		return "域名解析失败"
	case strings.Contains(msg, "network is unreachable"):
		return "网络不可达"
	case strings.Contains(msg, "no route to host"):
		return "无路由到目标"
	default:
		return msg
	}
}

// SortByLatency 按延迟升序排列（不可达的排在最后），便于面板展示。
func SortByLatency(rs []Result) []Result {
	out := make([]Result, len(rs))
	copy(out, rs)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Alive != b.Alive {
			return a.Alive
		}
		return a.LatencyMS < b.LatencyMS
	})
	return out
}
