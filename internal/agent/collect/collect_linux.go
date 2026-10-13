//go:build linux

// Package collect 采集 Linux 主机的系统指标。
//
// 实现选择：**直接读 /proc 与 statfs，纯 Go、无 cgo、无外部命令**。
// 这样 Agent 可以是完全静态的静态二进制，不依赖 gopsutil 之类的重量级库。
package collect

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mmx-project/mmx/pkg/agentproto"
)

// Sampler 保存上一轮采样，用于计算速率型指标（CPU%、网速）。
type Sampler struct {
	mu       sync.Mutex
	prevCPU  cpuTimes
	prevNet  netCounters
	prevAt   time.Time
	prevUp   int64
	prevDown int64
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

type netCounters struct {
	rx uint64
	tx uint64
}

// New 构造采样器。
func New() *Sampler { return &Sampler{} }

// Collect 采集一次指标。第一次调用时 CPU%/网速为 0（没有基线）。
func (s *Sampler) Collect() (agentproto.MetricsPayload, error) {
	now := time.Now()
	var m agentproto.MetricsPayload
	m.TS = now.UnixMilli()

	cpu, err := readCPUStat()
	if err == nil {
		s.mu.Lock()
		if s.prevAt.IsZero() || cpu.total <= s.prevCPU.total {
			m.CPUPercent = 0
		} else {
			dt := float64(cpu.total - s.prevCPU.total)
			di := float64(cpu.idle - s.prevCPU.idle)
			m.CPUPercent = clamp(100*(1-di/dt), 0, 100)
		}
		s.prevCPU = cpu
		s.mu.Unlock()
	}

	if mem, err := readMemInfo(); err == nil {
		m.MemTotal = mem.total
		m.MemUsed = mem.total - mem.avail
		m.SwapTotal = mem.swapTotal
		m.SwapUsed = mem.swapTotal - mem.swapFree
	}

	if l1, l5, l15, err := readLoadAvg(); err == nil {
		m.Load1, m.Load5, m.Load15 = l1, l5, l15
	}

	if total, free, err := diskUsage("/"); err == nil {
		m.DiskTotal = total
		m.DiskUsed = total - free
	}

	if nc, err := readNetDev(); err == nil {
		s.mu.Lock()
		if s.prevAt.IsZero() || nc.rx < s.prevNet.rx || nc.tx < s.prevNet.tx {
			m.NetUp, m.NetDown = 0, 0
		} else {
			elapsed := now.Sub(s.prevAt).Seconds()
			if elapsed > 0 {
				m.NetUp = int64(float64(nc.tx-s.prevNet.tx) / elapsed)
				m.NetDown = int64(float64(nc.rx-s.prevNet.rx) / elapsed)
			}
		}
		s.prevUp += int64(nc.tx) - int64(s.prevNet.tx)
		s.prevDown += int64(nc.rx) - int64(s.prevNet.rx)
		s.prevNet = nc
		s.mu.Unlock()
	}

	if up, err := readUptime(); err == nil {
		m.UptimeSec = up
	}
	if tc, uc, err := countSockets(); err == nil {
		m.TCPConns, m.UDPConns = tc, uc
	}
	if n, err := countProcesses(); err == nil {
		m.Processes = n
	}
	if rss, ok := processRSSByName("xray"); ok {
		m.XrayRunning = true
		m.XrayRSS = rss
	}

	s.mu.Lock()
	m.Traffic = &agentproto.TrafficDelta{Up: s.prevUp, Down: s.prevDown}
	s.prevUp, s.prevDown = 0, 0
	s.prevAt = now
	s.mu.Unlock()

	return m, nil
}

// ---------------------------------------------------------------------------
// /proc 解析
// ---------------------------------------------------------------------------

func readCPUStat() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuTimes{}, errors.New("/proc/stat 为空")
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, errors.New("/proc/stat 格式异常")
	}
	var total, idle uint64
	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		total += n
		// 第 3 个是 idle，第 4 个是 iowait
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return cpuTimes{total: total, idle: idle}, nil
}

type memInfo struct {
	total, avail, swapTotal, swapFree int64
}

func readMemInfo() (memInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()
	var mi memInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val := parseKB(rest)
		switch key {
		case "MemTotal":
			mi.total = val
		case "MemAvailable":
			mi.avail = val
		case "MemFree":
			if mi.avail == 0 {
				mi.avail = val
			}
		case "SwapTotal":
			mi.swapTotal = val
		case "SwapFree":
			mi.swapFree = val
		}
	}
	return mi, nil
}

func parseKB(s string) int64 {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

func readLoadAvg() (float64, float64, float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("/proc/loadavg 格式异常")
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15, nil
}

func readUptime() (int64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, errors.New("/proc/uptime 格式异常")
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(f), nil
}

func readNetDev() (netCounters, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return netCounters{}, err
	}
	defer f.Close()
	var nc netCounters
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, ":") {
			continue
		}
		iface, rest, _ := strings.Cut(line, ":")
		name := strings.TrimSpace(iface)
		if name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		nc.rx += rx
		nc.tx += tx
	}
	return nc, nil
}

func countSockets() (tcp int64, udp int64, err error) {
	count := func(path string) (int64, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		n := int64(strings.Count(string(b), "\n"))
		if n > 0 {
			n-- // 去掉表头
		}
		return n, nil
	}
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if n, e := count(p); e == nil {
			tcp += n
		}
	}
	for _, p := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		if n, e := count(p); e == nil {
			udp += n
		}
	}
	return tcp, udp, nil
}

func countProcesses() (int64, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	var n int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		n++
	}
	return n, nil
}

// processRSSByName 在 /proc/*/comm 里找指定进程名，返回其 RSS（字节）。
func processRSSByName(want string) (int64, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if pid == "" || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", pid, "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name != want {
			continue
		}
		if rss, ok := readRSS(pid); ok {
			return rss, true
		}
		return 0, true
	}
	return 0, false
}

func readRSS(pid string) (int64, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", pid, "statm"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}

// diskUsage 返回挂载点的总容量与可用容量（字节）。
func diskUsage(path string) (total int64, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
