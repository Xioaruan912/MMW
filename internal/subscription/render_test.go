package subscription

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/storage"
)

const testRealityKey = "SB9nGmC1qLPZ0U2cJvYqW5t3Fh8kR4xN7dA6bE1sM0o="

func realityNode() storage.NodeView {
	spec := inbound.Spec{
		ServerID: "srv_1", Tag: "in-443", Protocol: inbound.ProtoVLESS,
		Transport: inbound.TransportTCP, Security: inbound.SecurityReality, Port: 443,
		Enabled: true,
		Stream: inbound.StreamSettings{
			RealityDest:        "www.example.com:443",
			RealityServerNames: []string{"www.example.com"},
			RealityPrivateKey:  testRealityKey,
			RealityShortIDs:    []string{"0123456789abcdef"},
		},
		ProtocolSettings: inbound.ProtocolSettings{Flow: "xtls-rprx-vision"},
	}
	spec.Normalize()
	in := spec.ToStorage()
	in.ID = "inb_1"

	return storage.NodeView{
		Node:       storage.Node{ID: "nod_1", InboundID: "inb_1", Name: "香港-01", Enabled: true},
		Inbound:    in,
		ServerName: "hk-01",
		ServerAddr: "1.2.3.4",
	}
}

func TestRealityPublicKeyDerivation(t *testing.T) {
	pub, err := RealityPublicKey(testRealityKey)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("公钥不是 base64url: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("公钥长度应为 32 字节，得到 %d", len(raw))
	}
	// 相同私钥必须派生相同公钥
	pub2, _ := RealityPublicKey(testRealityKey)
	if pub != pub2 {
		t.Fatal("派生结果不稳定")
	}
}

func TestRealityPublicKeyRejectsGarbage(t *testing.T) {
	if _, err := RealityPublicKey("!!!not-base64!!!"); err == nil {
		t.Fatal("非法 base64 应当报错")
	}
	if _, err := RealityPublicKey("c2hvcnQ="); err == nil {
		t.Fatal("长度不足 32 字节应当报错")
	}
}

func TestRenderVLESSURI(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	uris, err := RenderURIs(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("应有 1 条 URI，得到 %d", len(uris))
	}
	u := uris[0]
	for _, want := range []string{
		"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@1.2.3.4:443",
		"security=reality", "pbk=", "fp=chrome", "type=tcp", "flow=xtls-rprx-vision",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("URI 缺少 %q:\n%s", want, u)
		}
	}
	if !strings.Contains(u, "#") {
		t.Fatal("URI 应带 #节点名")
	}
}

func TestRenderTrojanAndShadowsocksURI(t *testing.T) {
	n := realityNode()
	n.Inbound.Protocol = inbound.ProtoTrojan
	n.Inbound.Security = inbound.SecurityTLS
	n.Inbound.Stream = `{"server_name":"a.example.com"}`

	entries := []Entry{{Node: n, Client: storage.InboundClient{UUID: "pw-uuid", Enabled: true},
		Spec: inbound.Parse(n.Inbound)}}
	uris, err := RenderURIs(entries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uris[0], "trojan://pw-uuid@1.2.3.4:443") {
		t.Fatalf("trojan URI 格式错误: %s", uris[0])
	}

	ss := realityNode()
	ss.Inbound.Protocol = inbound.ProtoShadowsocks
	ss.Inbound.Security = inbound.SecurityNone
	ss.Inbound.Stream = `{}`
	ss.Inbound.Settings = `{"method":"aes-256-gcm","password":"sspass"}`
	entries = []Entry{{Node: ss, Spec: inbound.Parse(ss.Inbound)}}
	uris, err = RenderURIs(entries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uris[0], "ss://") {
		t.Fatalf("ss URI 格式错误: %s", uris[0])
	}
	// 校验 userinfo 是 base64(method:password)
	parts := strings.SplitN(strings.TrimPrefix(uris[0], "ss://"), "@", 2)
	dec, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("ss userinfo 不是 base64url: %v", err)
	}
	if string(dec) != "aes-256-gcm:sspass" {
		t.Fatalf("ss userinfo 内容错误: %s", dec)
	}
}

func TestRenderClashContainsEssentials(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "uuid-1", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	out, err := RenderClash(entries, DefaultClashOptions())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"proxies:", "proxy-groups:", "rules:",
		"香港-01", "server: 1.2.3.4", "port: 443",
		"uuid: uuid-1", "type: vless", "reality-opts", "public-key", "short-id",
		"flow: xtls-rprx-vision",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("Clash 配置缺少 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "password: <") {
		t.Fatal("不应出现占位符")
	}
}

func TestDetectFormatByUA(t *testing.T) {
	cases := map[string]Format{
		"clash-verge/1.7.0":   FormatClash,
		"mihomo/1.18":         FormatClash,
		"Stash/2.5":           FormatClash, // Stash 兼容 Clash
		"Surfboard/2.0":       FormatClash,
		"Shadowrocket/2.2.30": FormatBase64,
		"v2rayN/6.0":          FormatBase64,
		"sing-box/1.9":        FormatSingBox,
		"":                    FormatClash,
		// 私有格式必须被识别出来，而不是被静默降级
		"Surge/5.0":   FormatSurge,
		"Loon/3.2":    FormatLoon,
		"QuantumultX": FormatQX,
	}
	for ua, want := range cases {
		if got := DetectFormat(ua); got != want {
			t.Errorf("UA %q 期望 %s 实得 %s", ua, want, got)
		}
	}
}

func TestBuildEntriesSkipsMissingCredentials(t *testing.T) {
	nodes := []storage.NodeView{realityNode()}
	entries := BuildEntries(nodes, map[string]storage.InboundClient{})
	if len(entries) != 0 {
		t.Fatalf("没有凭据时不应产出节点条目，得到 %d", len(entries))
	}
	entries = BuildEntries(nodes, map[string]storage.InboundClient{
		"inb_1": {UUID: "u", Enabled: true},
	})
	if len(entries) != 1 {
		t.Fatalf("有凭据时应产出 1 条，得到 %d", len(entries))
	}
}

func TestRenderSingBoxOutbound(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "uuid-1", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	out, err := RenderSingBox(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"type": "vless"`, `"public_key"`, `"香港-01"`, `"selector"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("sing-box 输出缺少 %q:\n%s", want, s)
		}
	}
}

// TestUnimplementedFormatsFailLoudly 回归：Surge/Loon/QX 不得被静默降级成
// Clash YAML —— 那会让用户拿到一份"看着像配置、实则不可用"的文件。
func TestUnimplementedFormatsFailLoudly(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "u", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	for f, stage := range UnimplementedFormats() {
		_, _, err := Render(entries, f)
		if err == nil {
			t.Fatalf("格式 %s 尚未实现，必须返回错误", f)
		}
		if !strings.Contains(err.Error(), stage) {
			t.Fatalf("错误信息应指出计划阶段 %s，实得: %v", stage, err)
		}
	}
	if _, _, err := Render(entries, Format("bogus")); err == nil {
		t.Fatal("未知格式必须返回错误")
	}
	if _, _, err := Render(entries, FormatStash); err != nil {
		t.Fatalf("Stash 应可用（走 Clash 渲染）: %v", err)
	}
}
