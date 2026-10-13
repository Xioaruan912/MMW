package subscription

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderEgern 生成 Egern 配置。
//
// Egern 的代理条目与 mihomo 兼容，但组与规则的键名不同
// （policy_groups / policies，而不是 proxy-groups / proxies）。
// 因此这里复用 clashProxy 构造条目，只替换外层结构。
func RenderEgern(entries []Entry) ([]byte, error) {
	proxies := make([]map[string]any, 0, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		p, err := clashProxy(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		proxies = append(proxies, p)
		names = append(names, sanitizeName(e.Name()))
	}

	groups := []map[string]any{
		{
			"name":     "PROXY",
			"type":     "select",
			"policies": append([]string{"AUTO", "DIRECT"}, names...),
		},
		{
			"name":     "AUTO",
			"type":     "url_test",
			"url":      "http://www.gstatic.com/generate_204",
			"interval": 300,
			"policies": names,
		},
	}

	doc := map[string]any{
		"proxies":       proxies,
		"policy_groups": groups,
		"rules": []string{
			"GEOIP,CN,DIRECT",
			"FINAL,PROXY",
		},
	}

	var sb strings.Builder
	sb.WriteString("# 由 MMX 生成（Egern）\n")
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return []byte(sb.String()), nil
}
