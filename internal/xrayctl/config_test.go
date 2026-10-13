package xrayctl

import (
	"testing"

	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/storage"
)

func sampleInbound(tag string, port int) storage.Inbound {
	s := inbound.Spec{
		ServerID: "srv_1", Tag: tag, Protocol: inbound.ProtoVLESS,
		Transport: inbound.TransportTCP, Security: inbound.SecurityReality, Port: port,
		Enabled: true,
		Stream: inbound.StreamSettings{
			RealityDest:        "www.example.com:443",
			RealityServerNames: []string{"www.example.com"},
			RealityPrivateKey:  "SB9nGmC1qLPZ0U2cJvYqW5t3Fh8kR4xN7dA6bE1sM0o=",
		},
	}
	s.Normalize()
	in := s.ToStorage()
	in.ID = "inb_" + tag
	return in
}

func TestBuildProducesExpectedShape(t *testing.T) {
	in := sampleInbound("in-443", 443)
	res, err := Build([]storage.Inbound{in}, map[string][]storage.InboundClient{
		in.ID: {{UUID: "u-1", Email: "alice", Enabled: true}},
	}, DefaultOptions())
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	// 启用 API 时配置里会有 2 个入站：api（dokodemo-door）+ 业务入站。
	// 缺少 api 入站的话，stats/在线人数查询根本不可用 —— 这是曾经的真实缺陷。
	if len(res.Config.Inbounds) != 2 {
		t.Fatalf("应有 2 个入站（api + 业务），得到 %d", len(res.Config.Inbounds))
	}
	var apiInbound map[string]any
	for _, in := range res.Config.Inbounds {
		if in["tag"] == APITag {
			apiInbound = in
		}
	}
	if apiInbound == nil {
		t.Fatal("缺少 API 入站（dokodemo-door），统计功能将不可用")
	}
	if apiInbound["listen"] != "127.0.0.1" {
		t.Fatalf("API 入站必须只监听回环地址，实得 %v", apiInbound["listen"])
	}
	if apiInbound["protocol"] != "dokodemo-door" {
		t.Fatalf("API 入站协议应为 dokodemo-door，实得 %v", apiInbound["protocol"])
	}
	if res.APIPort() == 0 {
		t.Fatal("APIPort() 应当能从配置里解析出端口")
	}
	if len(res.Config.Outbounds) != 2 {
		t.Fatalf("应有 direct/block 两个出站，得到 %d", len(res.Config.Outbounds))
	}
	if res.Config.Routing == nil || res.Config.DNS == nil {
		t.Fatal("routing 与 dns 不应为空")
	}
	if res.Config.API == nil || res.Config.Stats == nil {
		t.Fatal("启用 API 时应有 api 与 stats 段")
	}
	if res.Rev == "" || len(res.Rev) != len("sha256:")+16 {
		t.Fatalf("rev 格式异常: %q", res.Rev)
	}
}

func TestRevIsStableAndSensitive(t *testing.T) {
	in := sampleInbound("in-443", 443)
	clients := map[string][]storage.InboundClient{
		in.ID: {{UUID: "u-1", Email: "alice", Enabled: true}},
	}

	a, err := Build([]storage.Inbound{in}, clients, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build([]storage.Inbound{in}, clients, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if a.Rev != b.Rev {
		t.Fatalf("相同期望状态必须产出相同 rev: %s vs %s", a.Rev, b.Rev)
	}

	// 改端口 → rev 必须变
	in2 := sampleInbound("in-443", 8443)
	c, err := Build([]storage.Inbound{in2}, clients, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if c.Rev == a.Rev {
		t.Fatal("端口变化后 rev 应当改变")
	}

	// 加客户端 → rev 必须变
	clients2 := map[string][]storage.InboundClient{
		in.ID: {
			{UUID: "u-1", Email: "alice", Enabled: true},
			{UUID: "u-2", Email: "bob", Enabled: true},
		},
	}
	d, err := Build([]storage.Inbound{in}, clients2, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if d.Rev == a.Rev {
		t.Fatal("客户端变化后 rev 应当改变")
	}
}

func TestDisabledInboundExcluded(t *testing.T) {
	in := sampleInbound("in-443", 443)
	in.Enabled = false
	res, err := Build([]storage.Inbound{in}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	// 被禁用的业务入站不应出现；api 入站与业务入站无关，始终存在
	for _, in := range res.Config.Inbounds {
		if in["tag"] == "in-443" {
			t.Fatalf("被禁用的入站不应出现在配置里：%+v", in)
		}
	}
	if len(res.Tags) != 0 {
		t.Fatalf("tags 应为空，实得 %v", res.Tags)
	}
}

func TestBuildRejectsInvalidInbound(t *testing.T) {
	// 手工构造一个 reality 但缺私钥的入站，渲染阶段应报错
	bad := storage.Inbound{
		ID: "inb_bad", ServerID: "srv_1", Tag: "bad",
		Protocol: "vless", Transport: "tcp", Security: "reality", Port: 443,
		Enabled: true, Settings: "{}",
		Stream: `{"reality_dest":"www.example.com:443","reality_server_names":["www.example.com"]}`,
	}
	if _, err := Build([]storage.Inbound{bad}, nil, DefaultOptions()); err == nil {
		t.Fatal("缺 REALITY 私钥时应报错")
	}
}

func TestBlockPrivateToggle(t *testing.T) {
	in := sampleInbound("in-443", 443)
	opts := DefaultOptions()
	opts.BlockPrivate = false
	res, err := Build([]storage.Inbound{in}, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	rules := res.Config.Routing["rules"].([]map[string]any)
	for _, r := range rules {
		if r["outboundTag"] == "block" {
			t.Fatalf("关闭 BlockPrivate 后不应有 block 规则: %+v", r)
		}
	}
}

func TestCanonicalIsOrderIndependent(t *testing.T) {
	a := sampleInbound("in-443", 443)
	b := sampleInbound("in-443", 443)
	ra, err := Build([]storage.Inbound{a}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Build([]storage.Inbound{b}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	ba, _ := Canonical(ra.Config)
	bb, _ := Canonical(rb.Config)
	if string(ba) != string(bb) {
		t.Fatal("规范化 JSON 应当逐字节一致")
	}
}

func TestAPIPortParsedFromConfig(t *testing.T) {
	opts := DefaultOptions()
	opts.APIPort = 10999
	in := sampleInbound("in-443", 443)
	res, err := Build([]storage.Inbound{in}, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.APIPort(); got != 10999 {
		t.Fatalf("APIPort 应为 10999，得到 %d", got)
	}
}

func TestAPIDisabledNoInbound(t *testing.T) {
	opts := DefaultOptions()
	opts.EnableAPI = false
	in := sampleInbound("in-443", 443)
	res, err := Build([]storage.Inbound{in}, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range res.Config.Inbounds {
		if x["tag"] == APITag {
			t.Fatal("关闭 API 后不应有 api 入站")
		}
	}
	if res.APIPort() != 0 {
		t.Fatal("关闭 API 后 APIPort 应为 0")
	}
	if res.Config.API != nil || res.Config.Stats != nil {
		t.Fatal("关闭 API 后不应有 api / stats 段")
	}
}
