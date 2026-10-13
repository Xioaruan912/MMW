package xrayctl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mmx-project/mmx/internal/storage"
)

func sampleOutbound(tag, protocol, proxyTag string) storage.Outbound {
	return storage.Outbound{
		ID: "out_" + tag, ServerID: "srv_1", Tag: tag, Protocol: protocol,
		Settings: `{"servers":[{"address":"1.2.3.4","port":443}]}`,
		Stream:   `{}`, ProxyTag: proxyTag, Enabled: true,
	}
}

func sampleRule(kind string, values []string, tag string) storage.RoutingRule {
	b, _ := json.Marshal(values)
	return storage.RoutingRule{
		ID: "rtr_" + kind, ServerID: "srv_1", Kind: kind, Values: string(b),
		OutboundTag: tag, Enabled: true,
	}
}

func TestRenderOutboundBasic(t *testing.T) {
	obj, err := RenderOutbound(sampleOutbound("relay-jp", "vless", ""))
	if err != nil {
		t.Fatal(err)
	}
	if obj["tag"] != "relay-jp" || obj["protocol"] != "vless" {
		t.Fatalf("基础字段错误: %+v", obj)
	}
	if _, ok := obj["settings"]; !ok {
		t.Fatal("缺少 settings")
	}
	if _, ok := obj["proxySettings"]; ok {
		t.Fatal("无 proxy_tag 时不应有 proxySettings")
	}
}

func TestRenderOutboundChained(t *testing.T) {
	obj, err := RenderOutbound(sampleOutbound("relay-2", "freedom", "relay-jp"))
	if err != nil {
		t.Fatal(err)
	}
	ps, ok := obj["proxySettings"].(map[string]any)
	if !ok {
		t.Fatal("链式出站必须带 proxySettings")
	}
	if ps["tag"] != "relay-jp" {
		t.Fatalf("proxySettings.tag 错误: %v", ps["tag"])
	}
}

func TestRenderOutboundRejectsBadJSON(t *testing.T) {
	o := sampleOutbound("bad", "vless", "")
	o.Settings = "{not json"
	if _, err := RenderOutbound(o); err == nil {
		t.Fatal("非法 settings JSON 必须被拒")
	}
	o2 := sampleOutbound("bad2", "vless", "")
	o2.Stream = "[1,2,3]"
	if _, err := RenderOutbound(o2); err == nil {
		t.Fatal("stream 不是对象时必须被拒")
	}
}

func TestRenderOutboundRejectsEmptyTag(t *testing.T) {
	o := sampleOutbound("", "vless", "")
	if _, err := RenderOutbound(o); err == nil {
		t.Fatal("空 tag 必须被拒")
	}
}

func TestRenderRoutingRuleAllKinds(t *testing.T) {
	want := map[string]string{
		"domain": "domain", "ip": "ip", "port": "port",
		"inbound": "inboundTag", "protocol": "protocol",
		"source": "source", "user": "user",
	}
	for kind, field := range want {
		r := sampleRule(kind, []string{"a", "b"}, "direct")
		obj, err := RenderRoutingRule(r)
		if err != nil {
			t.Errorf("类型 %s 渲染失败: %v", kind, err)
			continue
		}
		if obj["type"] != "field" {
			t.Errorf("类型 %s 的 type 应为 field", kind)
		}
		if _, ok := obj[field]; !ok {
			t.Errorf("类型 %s 应映射到字段 %s，实得 %+v", kind, field, obj)
		}
		if obj["outboundTag"] != "direct" {
			t.Errorf("类型 %s 的出站错误", kind)
		}
	}
}

func TestRenderRoutingRuleRejectsBadInput(t *testing.T) {
	if _, err := RenderRoutingRule(sampleRule("nonsense", []string{"x"}, "direct")); err == nil {
		t.Fatal("未知规则类型必须被拒")
	}
	if _, err := RenderRoutingRule(sampleRule("domain", nil, "direct")); err == nil {
		t.Fatal("空匹配值必须被拒")
	}
	if _, err := RenderRoutingRule(sampleRule("domain", []string{"x"}, "")); err == nil {
		t.Fatal("缺少出站必须被拒")
	}
	r := sampleRule("domain", []string{"x"}, "direct")
	r.Values = "not-json"
	if _, err := RenderRoutingRule(r); err == nil {
		t.Fatal("values 不是 JSON 数组时必须被拒")
	}
}

func TestBuildIncludesCustomOutboundsAndRules(t *testing.T) {
	in := sampleInbound("in-443", 443)
	res, err := BuildInput(Input{
		Inbounds:  []storage.Inbound{in},
		Outbounds: []storage.Outbound{sampleOutbound("relay-jp", "vless", "")},
		Rules:     []storage.RoutingRule{sampleRule("domain", []string{"geosite:netflix"}, "relay-jp")},
	}, DefaultOptions())
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	// 自定义出站应在 direct/block 之前
	if len(res.Config.Outbounds) != 3 {
		t.Fatalf("应有 3 个出站（自定义 + direct + block），实得 %d", len(res.Config.Outbounds))
	}
	if res.Config.Outbounds[0]["tag"] != "relay-jp" {
		t.Fatalf("自定义出站应排在最前: %+v", res.Config.Outbounds[0])
	}
	if res.Config.Outbounds[1]["tag"] != "direct" || res.Config.Outbounds[2]["tag"] != "block" {
		t.Fatal("direct / block 应固定在后")
	}

	// 自定义规则应在内置规则之前
	rules := res.Config.Routing["rules"].([]map[string]any)
	if len(rules) == 0 {
		t.Fatal("缺少路由规则")
	}
	first := rules[0]
	if first["outboundTag"] != "relay-jp" {
		t.Fatalf("自定义规则应优先匹配: %+v", first)
	}
	if _, ok := first["domain"]; !ok {
		t.Fatalf("domain 规则字段缺失: %+v", first)
	}
}

func TestBuildRejectsDanglingProxyTag(t *testing.T) {
	in := sampleInbound("in-443", 443)
	// 链式出站指向一个不存在的前置
	_, err := BuildInput(Input{
		Inbounds:  []storage.Inbound{in},
		Outbounds: []storage.Outbound{sampleOutbound("relay-2", "vless", "nonexistent")},
	}, DefaultOptions())
	if err == nil {
		t.Fatal("链式引用不存在的前置出站必须被拒（否则 Xray 启动即失败）")
	}
	if !strings.Contains(err.Error(), "前置出站") {
		t.Fatalf("错误信息应指出前置出站问题: %v", err)
	}
}

func TestBuildRejectsDanglingRuleOutbound(t *testing.T) {
	in := sampleInbound("in-443", 443)
	_, err := BuildInput(Input{
		Inbounds: []storage.Inbound{in},
		Rules:    []storage.RoutingRule{sampleRule("domain", []string{"x"}, "ghost")},
	}, DefaultOptions())
	if err == nil {
		t.Fatal("规则指向不存在的出站必须被拒")
	}
}

func TestBuildRejectsDuplicateOutboundTag(t *testing.T) {
	in := sampleInbound("in-443", 443)
	_, err := BuildInput(Input{
		Inbounds: []storage.Inbound{in},
		Outbounds: []storage.Outbound{
			sampleOutbound("dup", "vless", ""),
			sampleOutbound("dup", "trojan", ""),
		},
	}, DefaultOptions())
	if err == nil {
		t.Fatal("重复的 outbound tag 必须被拒")
	}
}

func TestBuildIgnoresDisabledOutboundsAndRules(t *testing.T) {
	in := sampleInbound("in-443", 443)
	o := sampleOutbound("relay-jp", "vless", "")
	o.Enabled = false
	r := sampleRule("domain", []string{"x"}, "relay-jp")
	r.Enabled = false

	res, err := BuildInput(Input{
		Inbounds: []storage.Inbound{in}, Outbounds: []storage.Outbound{o}, Rules: []storage.RoutingRule{r},
	}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	// 只剩 direct + block
	if len(res.Config.Outbounds) != 2 {
		t.Fatalf("被禁用的出站不应出现，实得 %d 个", len(res.Config.Outbounds))
	}
	rules := res.Config.Routing["rules"].([]map[string]any)
	for _, rule := range rules {
		if rule["outboundTag"] == "relay-jp" {
			t.Fatal("被禁用的规则不应出现")
		}
	}
}

func TestRuleKindNamesStable(t *testing.T) {
	names := RuleKindNames()
	if len(names) != 7 {
		t.Fatalf("应有 7 种规则类型，实得 %d", len(names))
	}
	for _, n := range names {
		if !ValidRuleKind(n) {
			t.Fatalf("RuleKindNames 返回了不被支持的类型 %s", n)
		}
	}
	if ValidRuleKind("bogus") {
		t.Fatal("未知类型不应被接受")
	}
}
