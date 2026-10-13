package xrayctl

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mmx-project/mmx/internal/storage"
)

// RenderOutbound 把数据库里的出站渲染成 Xray 的 outbound 对象。
func RenderOutbound(o storage.Outbound) (map[string]any, error) {
	if strings.TrimSpace(o.Tag) == "" {
		return nil, fmt.Errorf("出站 %s 缺少 tag", o.ID)
	}
	if strings.TrimSpace(o.Protocol) == "" {
		return nil, fmt.Errorf("出站 %s 缺少 protocol", o.Tag)
	}

	out := map[string]any{"tag": o.Tag, "protocol": o.Protocol}

	var settings map[string]any
	if o.Settings != "" {
		if err := json.Unmarshal([]byte(o.Settings), &settings); err != nil {
			return nil, fmt.Errorf("出站 %s 的 settings 不是合法 JSON: %w", o.Tag, err)
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	out["settings"] = settings

	// 链式：先经 ProxyTag 出站，再连目标
	if o.ProxyTag != "" {
		out["proxySettings"] = map[string]any{"tag": o.ProxyTag}
	}
	if o.SendThrough != "" {
		out["sendThrough"] = o.SendThrough
	}

	var stream map[string]any
	if o.Stream != "" && o.Stream != "{}" {
		if err := json.Unmarshal([]byte(o.Stream), &stream); err != nil {
			return nil, fmt.Errorf("出站 %s 的 stream 不是合法 JSON: %w", o.Tag, err)
		}
	}
	if len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out, nil
}

// ruleKinds 是支持的路由规则类型到 Xray 字段名的映射。
var ruleKinds = map[string]string{
	"domain":   "domain",
	"ip":       "ip",
	"port":     "port",
	"inbound":  "inboundTag",
	"protocol": "protocol",
	"source":   "source",
	"user":     "user",
}

// RenderRoutingRule 把数据库里的路由规则渲染成 Xray 的 routing rule。
func RenderRoutingRule(r storage.RoutingRule) (map[string]any, error) {
	field, ok := ruleKinds[r.Kind]
	if !ok {
		return nil, fmt.Errorf("不支持的路由规则类型 %q（可用: %s）",
			r.Kind, strings.Join(RuleKindNames(), ", "))
	}
	var values []string
	if err := json.Unmarshal([]byte(r.Values), &values); err != nil {
		return nil, fmt.Errorf("路由规则的 values 不是合法 JSON 数组: %w", err)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("路由规则 %s 的匹配值为空", r.ID)
	}
	if strings.TrimSpace(r.OutboundTag) == "" {
		return nil, fmt.Errorf("路由规则 %s 未指定出站", r.ID)
	}
	return map[string]any{
		"type":        "field",
		field:         values,
		"outboundTag": r.OutboundTag,
	}, nil
}

// RuleKindNames 返回支持的路由规则类型名（顺序稳定，便于展示与测试）。
func RuleKindNames() []string {
	return []string{"domain", "ip", "port", "inbound", "protocol", "source", "user"}
}

// ValidRuleKind 判断规则类型是否受支持。
func ValidRuleKind(kind string) bool {
	_, ok := ruleKinds[kind]
	return ok
}
