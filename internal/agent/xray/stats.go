package xray

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIPort 是 Xray API 的默认监听端口（与 xrayctl 生成配置时保持一致）。
const DefaultAPIPort = 10085

// StatsQueryOptions 控制一次统计查询。
type StatsQueryOptions struct {
	// APIPort 为 0 时用最近一次应用配置里解析出的端口，再退回 DefaultAPIPort。
	APIPort int
	// Pattern 是 stats 名称前缀，默认 "user>>>"（只看用户维度）。
	Pattern string
	// Reset 为 true 时查询后清零计数（这样每次取到的就是增量）。
	Reset bool
}

// QueryStats 调用 `xray api statsquery` 拉取统计。
//
// 为什么走官方 CLI 而不是自己实现 gRPC 客户端：
//   - 不需要把 xray-core 的 protobuf 依赖拖进 Agent（二进制体积与构建时长）
//   - CLI 与内核版本天然匹配，不会出现 proto 版本漂移
//   - 输出格式稳定，解析逻辑可单测
func (m *ExternalManager) QueryStats(ctx context.Context, opts StatsQueryOptions) (string, error) {
	if !m.Status().Running {
		return "", errors.New("Xray 未运行，无统计可查")
	}
	port := opts.APIPort
	if port == 0 {
		port = m.currentAPIPort()
	}
	if port == 0 {
		port = DefaultAPIPort
	}
	pattern := opts.Pattern
	if pattern == "" {
		pattern = "user>>>"
	}

	args := []string{
		"api", "statsquery",
		"--server=127.0.0.1:" + strconv.Itoa(port),
		"--pattern", pattern,
	}
	if opts.Reset {
		args = append(args, "--reset")
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, m.BinPath, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("xray statsquery 失败: %w（输出: %s）", err,
			strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// rememberAPIPort 从配置里解析 API 端口并记住。
func (m *ExternalManager) rememberAPIPort(cfg any) {
	port := extractAPIPort(cfg)
	if port <= 0 {
		return
	}
	m.mu.Lock()
	m.apiPort = port
	m.mu.Unlock()
}

func (m *ExternalManager) currentAPIPort() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apiPort
}

// extractAPIPort 从配置对象里找 tag=api 的入站端口。
func extractAPIPort(cfg any) int {
	inbounds := inboundsOf(cfg)
	for _, in := range inbounds {
		tag, _ := in["tag"].(string)
		if tag != "api" {
			continue
		}
		switch p := in["port"].(type) {
		case int:
			return p
		case int64:
			return int(p)
		case float64:
			return int(p)
		}
	}
	return 0
}

func inboundsOf(cfg any) []map[string]any {
	switch c := cfg.(type) {
	case []map[string]any:
		return c
	case map[string]any:
		if raw, ok := c["inbounds"].([]map[string]any); ok {
			return raw
		}
		if raw, ok := c["inbounds"].([]any); ok {
			out := make([]map[string]any, 0, len(raw))
			for _, x := range raw {
				if m, ok := x.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return nil
}
