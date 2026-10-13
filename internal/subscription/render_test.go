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

// TestAllSupportedFormatsRender 遍历全部已声明支持的格式，确保都能产出内容。
// 这条测试的价值在于：新增格式时忘了接进 Render 会立刻被抓到。
// tlsNode 返回一个所有客户端都支持的节点（VLESS + TCP + TLS）。
// REALITY 并非每个客户端都支持（Surfboard / QX 明确不支持），
// 所以「全格式可渲染」这条测试必须用最通用的组合。
func tlsNode() storage.NodeView {
	n := realityNode()
	n.Inbound.Security = inbound.SecurityTLS
	n.Inbound.Stream = `{"server_name":"a.example.com"}`
	return n
}

func entryOf(n storage.NodeView) Entry {
	return Entry{
		Node:   n,
		Client: storage.InboundClient{UUID: "uuid-1", Email: "alice", Enabled: true},
		Spec:   inbound.Parse(n.Inbound),
	}
}

// TestAllSupportedFormatsRender 遍历全部已声明支持的格式。
// 价值：新增格式时忘了接进 Render 会立刻被抓到。
func TestAllSupportedFormatsRender(t *testing.T) {
	entries := []Entry{entryOf(tlsNode())}
	for _, f := range SupportedFormats() {
		body, ctype, err := Render(entries, f)
		if err != nil {
			t.Errorf("格式 %s 渲染失败: %v", f, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("格式 %s 输出为空", f)
		}
		if ctype == "" {
			t.Errorf("格式 %s 未返回 Content-Type", f)
		}
	}
	if _, _, err := Render(entries, Format("bogus")); err == nil {
		t.Fatal("未知格式必须返回错误")
	}
}

// TestRealitySupportMatrix 明确记录哪些客户端支持 REALITY。
// 不支持的要**报错而不是静默降级** —— 静默降级会让用户以为连上了其实没有。
func TestRealitySupportMatrix(t *testing.T) {
	entries := []Entry{entryOf(realityNode())}

	supports := []Format{FormatClash, FormatEgern, FormatURI, FormatBase64, FormatSurge, FormatLoon, FormatSingBox}
	for _, f := range supports {
		if _, _, err := Render(entries, f); err != nil {
			t.Errorf("格式 %s 应当支持 REALITY，实得错误: %v", f, err)
		}
	}

	rejects := []Format{FormatSurfboard, FormatQX}
	for _, f := range rejects {
		if _, _, err := Render(entries, f); err == nil {
			t.Errorf("格式 %s 不支持 REALITY，必须报错（不得静默降级）", f)
		}
	}
}

func TestSurgeRendering(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "uuid-1", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	out, err := RenderSurge(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"[Proxy]", "[Proxy Group]", "[Rule]",
		"香港-01 = vless, 1.2.3.4, 443",
		"username=uuid-1",
		"tls=true",
		"sni=www.example.com",
		"reality-public-key=",
		"reality-short-id=0123456789abcdef",
		"flow=xtls-rprx-vision",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("Surge 输出缺少 %q:\n%s", want, s)
		}
	}
}

func TestSurgeRejectsUnsupported(t *testing.T) {
	// httpupgrade 在 Surge 里没有对应语义 —— 必须报错而不是静默降级
	e := Entry{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "u", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}
	e.Node.Inbound.Transport = inbound.TransportHTTPUpgrade
	e.Spec = inbound.Parse(e.Node.Inbound)
	e.Spec.Stream.HTTPUpgradePath = "/x"
	if _, err := RenderSurge([]Entry{e}); err == nil {
		t.Fatal("Surge 不支持 httpupgrade，应当报错")
	}
}

func TestSurfboardRejectsReality(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "u", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	if _, err := RenderSurfboard(entries); err == nil {
		t.Fatal("Surfboard 不支持 REALITY，应当报错而不是静默降级")
	}
}

func TestLoonRendering(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "uuid-1", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	out, err := RenderLoon(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"[Proxy]",
		"香港-01 = VLESS, 1.2.3.4, 443",
		`"uuid-1"`,
		"over-tls=true",
		"tls-name=www.example.com",
		"public-key=",
		"short-id=0123456789abcdef",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("Loon 输出缺少 %q:\n%s", want, s)
		}
	}
}

func TestQuantumultXRendering(t *testing.T) {
	// QX 不支持 REALITY：换成一个 TLS 节点来验证正常路径
	n := realityNode()
	n.Inbound.Security = inbound.SecurityTLS
	n.Inbound.Stream = `{"server_name":"a.example.com"}`
	n.Inbound.Protocol = inbound.ProtoVLESS
	entries := []Entry{{
		Node: n, Client: storage.InboundClient{UUID: "uuid-1", Enabled: true},
		Spec: inbound.Parse(n.Inbound),
	}}
	out, err := RenderQuantumultX(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"[server_local]", "vless=1.2.3.4:443", "password=uuid-1", "obfs=over-tls", "obfs-host=a.example.com", "tag=香港-01"} {
		if !strings.Contains(s, want) {
			t.Fatalf("QX 输出缺少 %q:\n%s", want, s)
		}
	}

	// REALITY 必须被拒
	r := Entry{
		Node: realityNode(), Client: storage.InboundClient{UUID: "u", Enabled: true},
		Spec: inbound.Parse(realityNode().Inbound),
	}
	if _, err := RenderQuantumultX([]Entry{r}); err == nil {
		t.Fatal("QX 不支持 REALITY，应当报错")
	}
}

func TestEgernRendering(t *testing.T) {
	entries := []Entry{{
		Node:   realityNode(),
		Client: storage.InboundClient{UUID: "uuid-1", Enabled: true},
		Spec:   inbound.Parse(realityNode().Inbound),
	}}
	out, err := RenderEgern(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"proxies:", "policy_groups:", "policies:", "香港-01", "reality-opts"} {
		if !strings.Contains(s, want) {
			t.Fatalf("Egern 输出缺少 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "proxy-groups") {
		t.Fatal("Egern 使用 policy_groups，不应出现 Clash 的 proxy-groups")
	}
}

func TestDetectFormatByUAAll(t *testing.T) {
	cases := map[string]Format{
		"clash-verge/1.7.0":   FormatClash,
		"mihomo/1.18":         FormatClash,
		"Stash/2.5":           FormatClash,
		"Surge/5.0":           FormatSurge,
		"Surfboard/2.0":       FormatSurfboard,
		"Loon/3.2":            FormatLoon,
		"Quantumult X/1.0":    FormatQX,
		"Egern/1.0":           FormatEgern,
		"Shadowrocket/2.2.30": FormatShadowrocket,
		"v2rayN/6.0":          FormatBase64,
		"sing-box/1.9":        FormatSingBox,
		"":                    FormatClash,
	}
	for ua, want := range cases {
		if got := DetectFormat(ua); got != want {
			t.Errorf("UA %q 期望 %s 实得 %s", ua, want, got)
		}
	}
}
