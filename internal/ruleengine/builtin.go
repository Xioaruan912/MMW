package ruleengine

// 内置模板。它们同时也是文档：管理员可以复制后按需修改。

// BuiltinTemplates 返回内置模板列表。
//
// 每个模板都必须是**可直接使用的完整 Clash 配置**，只在节点位置留占位符。
func BuiltinTemplates() []Template {
	return []Template{
		{
			Name:        BuiltinMinimal,
			Description: "最小配置：单个选择组 + 一条 MATCH 规则。适合快速验证节点可用性。",
			Builtin:     true,
			Content:     minimalTemplate,
		},
		{
			Name:        BuiltinSimple,
			Description: "常用配置：国内直连 + 广告拦截 + 自动测速组。",
			Builtin:     true,
			Content:     simpleTemplate,
		},
		{
			Name:        BuiltinFull,
			Description: "完整配置：含 DNS 分流、规则集、按地区自动分组。需要节点带地区信息。",
			Builtin:     true,
			Content:     fullTemplate,
		},
	}
}

// FindBuiltin 按名字查找内置模板。
func FindBuiltin(name string) (Template, bool) {
	for _, t := range BuiltinTemplates() {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

const minimalTemplate = `# MMX 最小模板
mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
external-controller: 127.0.0.1:9090

proxies:
{{PROXIES}}

proxy-groups:
  - name: PROXY
    type: select
    proxies:
      {{PROXY_NAMES}}

rules:
  - GEOIP,LAN,DIRECT,no-resolve
  - MATCH,PROXY
`

const simpleTemplate = `# MMX 常用模板
mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
external-controller: 127.0.0.1:9090
unified-delay: true
tcp-concurrent: true
global-client-fingerprint: chrome

proxies:
{{PROXIES}}

proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - AUTO
      - DIRECT
      {{PROXY_NAMES}}

  - name: AUTO
    type: url-test
    url: http://www.gstatic.com/generate_204
    interval: 300
    tolerance: 50
    proxies:
      {{PROXY_NAMES}}

rules:
  - GEOSITE,category-ads-all,REJECT
  - GEOIP,LAN,DIRECT,no-resolve
  - GEOSITE,cn,DIRECT
  - GEOIP,CN,DIRECT
  - MATCH,PROXY
`

const fullTemplate = `# MMX 完整模板
mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
external-controller: 127.0.0.1:9090
unified-delay: true
tcp-concurrent: true
find-process-mode: strict
global-client-fingerprint: chrome

dns:
  enable: true
  ipv6: false
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  nameserver:
    - 223.5.5.5
    - 119.29.29.29
  fallback:
    - https://1.1.1.1/dns-query
    - https://8.8.8.8/dns-query
  fallback-filter:
    geoip: true
    geoip-code: CN

proxies:
{{PROXIES}}

proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - AUTO
      - FALLBACK
      - DIRECT
      {{PROXY_NAMES}}

  - name: AUTO
    type: url-test
    url: http://www.gstatic.com/generate_204
    interval: 300
    tolerance: 50
    proxies:
      {{PROXY_NAMES}}

  - name: FALLBACK
    type: fallback
    url: http://www.gstatic.com/generate_204
    interval: 300
    proxies:
      {{PROXY_NAMES}}

rules:
  - GEOSITE,category-ads-all,REJECT
  - GEOIP,LAN,DIRECT,no-resolve
  - GEOSITE,private,DIRECT
  - GEOSITE,cn,DIRECT
  - GEOIP,CN,DIRECT
  - MATCH,PROXY
`
