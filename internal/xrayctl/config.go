// Package xrayctl 负责把「数据库里的期望状态」翻译成 Xray 配置，并计算版本摘要。
//
// 分工：
//
//	inbound  负责单个入站的协议/传输/安全层渲染
//	xrayctl  负责整份配置（log / api / stats / policy / inbounds / outbounds / routing / dns）
//	          + 稳定 rev 计算（用于幂等下发）
package xrayctl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// APITag 是 Xray 本地 API 的 tag（stats / handler 服务都挂在它上面）。
const APITag = "api"

// DefaultAPIPort 是 Xray API 的默认监听端口（仅回环）。
const DefaultAPIPort = 10085

// Options 控制生成的配置内容。
type Options struct {
	LogLevel     string // debug | info | warning | error；空则 warning
	AccessLog    string // "none" 关闭访问日志；空则 none
	EnableAPI    bool   // 是否启用 Xray 本地 API（用于统计/在线人数）
	APIPort      int    // API 监听端口，默认 10085
	BlockPrivate bool   // 是否拦截到内网地址的出站（防 SSRF / 防内网穿透）
	DNS          []string
}

// DefaultOptions 返回生产可用的默认值。
func DefaultOptions() Options {
	return Options{
		LogLevel:     "warning",
		AccessLog:    "none",
		EnableAPI:    true,
		APIPort:      10085,
		BlockPrivate: true,
	}
}

// BuildResult 是一次配置构建的产物。
type BuildResult struct {
	Config *agentproto.XrayConfig
	Rev    string
	Tags   []string // 参与构建的入站 tag（便于日志与对比）
}

// Build 根据入站与客户端生成一台服务器的完整 Xray 配置。
//
// clients 以 inbound ID 为键；未提供的入站视为没有客户端。
func Build(inbounds []storage.Inbound, clients map[string][]storage.InboundClient, opts Options) (*BuildResult, error) {
	if opts.LogLevel == "" {
		opts.LogLevel = "warning"
	}
	if opts.AccessLog == "" {
		opts.AccessLog = "none"
	}
	if opts.APIPort == 0 {
		opts.APIPort = 10085
	}

	cfg := &agentproto.XrayConfig{
		Log: map[string]any{
			"loglevel": opts.LogLevel,
			"access":   opts.AccessLog,
		},
	}

	if opts.EnableAPI {
		cfg.API = map[string]any{
			"tag":      APITag,
			"services": []string{"HandlerService", "StatsService"},
		}
		cfg.Stats = map[string]any{}
		cfg.Policy = map[string]any{
			"levels": map[string]any{
				"0": map[string]any{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
				},
			},
			"system": map[string]any{
				"statsInboundUplink":   true,
				"statsInboundDownlink": true,
			},
		}
	}

	// ---- inbounds ----
	var tags []string

	// API 本身需要一个 dokodemo-door 入站来承载请求。
	// 缺少这一段时 api/stats 段形同虚设 —— 统计与在线人数都无法查询。
	// 只监听回环地址，不对外暴露。
	if opts.EnableAPI {
		cfg.Inbounds = append(cfg.Inbounds, map[string]any{
			"tag": APITag, "listen": "127.0.0.1", "port": opts.APIPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1"},
		})
	}

	for _, in := range inbounds {
		if !in.Enabled {
			continue
		}
		obj, err := inbound.RenderXrayInbound(in, clients[in.ID])
		if err != nil {
			return nil, fmt.Errorf("渲染入站 %s（%s）失败: %w", in.Tag, in.ID, err)
		}
		cfg.Inbounds = append(cfg.Inbounds, obj)
		tags = append(tags, in.Tag)
	}

	// ---- outbounds ----
	cfg.Outbounds = []map[string]any{
		{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIP"}},
		{"tag": "block", "protocol": "blackhole", "settings": map[string]any{}},
	}

	// ---- routing ----
	rules := []map[string]any{}
	if opts.EnableAPI {
		// API 流量不走出站，直接由 Xray 内部处理
		rules = append(rules, map[string]any{
			"type": "field", "inboundTag": []string{APITag}, "outboundTag": APITag,
		})
	}
	if opts.BlockPrivate {
		rules = append(rules, map[string]any{
			"type": "field", "outboundTag": "block",
			"ip":     []string{"geoip:private", "geoip:cn"},
			"domain": []string{"geosite:category-ads-all"},
		})
	}
	cfg.Routing = map[string]any{
		"domainStrategy": "AsIs",
		"rules":          rules,
	}

	// ---- dns ----
	dnsServers := opts.DNS
	if len(dnsServers) == 0 {
		dnsServers = []string{"1.1.1.1", "8.8.8.8", "223.5.5.5"}
	}
	cfg.DNS = map[string]any{
		"hosts": map[string]any{
			"domain:googleapis.cn": "googleapis.com",
		},
		"servers": dnsServers,
	}

	rev, err := Rev(cfg)
	if err != nil {
		return nil, err
	}
	return &BuildResult{Config: cfg, Rev: rev, Tags: tags}, nil
}

// Rev 计算配置的稳定摘要。
//
// 依赖 encoding/json 对 map 键排序的特性，因此同一组期望状态永远得到同一个 rev，
// Agent 侧据此判断"要不要重新落盘"。
func Rev(cfg *agentproto.XrayConfig) (string, error) {
	raw, err := Canonical(cfg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:8]), nil
}

// Canonical 返回规范化（键有序、无 HTML 转义）的 JSON。
func Canonical(cfg *agentproto.XrayConfig) ([]byte, error) {
	var buf []byte
	{
		b, err := json.Marshal(cfg)
		if err != nil {
			return nil, err
		}
		buf = b
	}
	// 重新编码一次以确保嵌套 map 也被规范化（json.Marshal 本身已排序，这里是防御性处理）
	var generic any
	if err := json.Unmarshal(buf, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

// jsonUnmarshal 是本包内部的 JSON 解包入口（集中一处便于将来换高性能实现）。
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// APIPort 返回生成的配置中 API 的监听端口（供 Agent 查询 stats 使用）。
func (r *BuildResult) APIPort() int {
	if r == nil || r.Config == nil || r.Config.API == nil {
		return 0
	}
	for _, in := range r.Config.Inbounds {
		if in["tag"] == APITag {
			if p, ok := in["port"].(int); ok {
				return p
			}
			if p, ok := in["port"].(float64); ok {
				return int(p)
			}
		}
	}
	return 0
}
