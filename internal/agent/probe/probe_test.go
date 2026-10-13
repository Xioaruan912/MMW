package probe

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startListener 起一个本地 TCP 监听用于测试可达性。
func startListener(t *testing.T) (host string, port int, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return "127.0.0.1", port, func() { _ = ln.Close() }
}

func TestTCPPingReachable(t *testing.T) {
	host, port, stop := startListener(t)
	defer stop()

	got := TCPPing(context.Background(), []Target{{Tag: "n1", Host: host, Port: port}}, Options{})
	if len(got) != 1 {
		t.Fatalf("应返回 1 条结果，实得 %d", len(got))
	}
	if !got[0].Alive {
		t.Fatalf("本地监听应可达: %+v", got[0])
	}
	if got[0].LatencyMS < 0 {
		t.Fatalf("延迟不应为负: %d", got[0].LatencyMS)
	}
	if got[0].Tag != "n1" {
		t.Fatalf("tag 未回传: %q", got[0].Tag)
	}
}

func TestTCPPingRefused(t *testing.T) {
	// 拿一个刚关闭的端口：应当得到"拒绝连接"
	host, port, stop := startListener(t)
	stop()
	time.Sleep(50 * time.Millisecond)

	got := TCPPing(context.Background(), []Target{{Tag: "n1", Host: host, Port: port}}, Options{
		Timeout: 500 * time.Millisecond,
	})
	if got[0].Alive {
		t.Fatal("已关闭的端口不应可达")
	}
	if got[0].Error == "" {
		t.Fatal("不可达时必须给出原因")
	}
}

func TestTCPPingTimeout(t *testing.T) {
	// 203.0.113.0/24 是 TEST-NET-3，正常网络下不可达 → 走超时分支
	got := TCPPing(context.Background(), []Target{{Tag: "n1", Host: "203.0.113.1", Port: 9}}, Options{
		Timeout: 300 * time.Millisecond,
	})
	if got[0].Alive {
		t.Skip("测试网络可路由到 TEST-NET，跳过")
	}
	if got[0].Error == "" {
		t.Fatal("超时必须给出原因")
	}
}

func TestTCPPingPreservesOrder(t *testing.T) {
	host, port, stop := startListener(t)
	defer stop()

	targets := []Target{
		{Tag: "a", Host: host, Port: port},
		{Tag: "b", Host: host, Port: port},
		{Tag: "c", Host: host, Port: port},
		{Tag: "d", Host: host, Port: port},
	}
	got := TCPPing(context.Background(), targets, Options{Concurrency: 4})
	for i, g := range got {
		if g.Tag != targets[i].Tag {
			t.Fatalf("结果顺序与输入不一致：位置 %d 期望 %s 实得 %s", i, targets[i].Tag, g.Tag)
		}
	}
}

func TestTCPPingEmptyTargets(t *testing.T) {
	got := TCPPing(context.Background(), nil, Options{})
	if len(got) != 0 {
		t.Fatalf("空输入应返回空结果，实得 %d", len(got))
	}
}

func TestTCPPingCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := TCPPing(ctx, []Target{{Tag: "n1", Host: "203.0.113.1", Port: 9}}, Options{
		Concurrency: 1, Timeout: 5 * time.Second,
	})
	if got[0].Alive {
		t.Fatal("已取消的上下文不应产生可达结果")
	}
}

func TestSortByLatency(t *testing.T) {
	in := []Result{
		{Tag: "slow", Alive: true, LatencyMS: 300},
		{Tag: "dead", Alive: false, Error: "不可达"},
		{Tag: "fast", Alive: true, LatencyMS: 20},
	}
	out := SortByLatency(in)
	if out[0].Tag != "fast" || out[1].Tag != "slow" || out[2].Tag != "dead" {
		t.Fatalf("排序错误: %+v", out)
	}
	// 不应修改原切片
	if in[0].Tag != "slow" {
		t.Fatal("SortByLatency 不应修改入参")
	}
}

func TestFriendlyDialError(t *testing.T) {
	cases := map[string]string{
		"dial tcp 1.2.3.4:443: connect: connection refused": "端口拒绝连接",
		"dial tcp 1.2.3.4:443: i/o timeout":                 "连接超时",
		"dial tcp: lookup nope.invalid: no such host":       "域名解析失败",
		"dial tcp: network is unreachable":                  "网络不可达",
	}
	for in, want := range cases {
		got := friendlyDialError(errStr(in))
		if !strings.Contains(got, want) {
			t.Errorf("输入 %q 期望含 %q 实得 %q", in, want, got)
		}
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }

func TestOptionsDefaults(t *testing.T) {
	var o Options
	o.apply()
	if o.Timeout != 3*time.Second {
		t.Fatalf("默认超时应为 3s，实得 %v", o.Timeout)
	}
	if o.Concurrency != 8 {
		t.Fatalf("默认并发应为 8，实得 %d", o.Concurrency)
	}
	if o.Samples != 1 {
		t.Fatalf("默认采样应为 1，实得 %d", o.Samples)
	}
}
