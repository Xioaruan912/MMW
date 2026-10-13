package inbound

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mmx-project/mmx/internal/storage"
)

const testRealityKey = "SB9nGmC1qLPZ0U2cJvYqW5t3Fh8kR4xN7dA6bE1sM0o="

func baseSpec() Spec {
	return Spec{
		ServerID: "srv_1", Tag: "in-443", Protocol: ProtoVLESS,
		Transport: TransportTCP, Security: SecurityReality, Port: 443, Enabled: true,
		Stream: StreamSettings{
			RealityDest:        "www.example.com:443",
			RealityServerNames: []string{"www.example.com"},
			RealityPrivateKey:  testRealityKey,
			RealityShortIDs:    []string{"0123456789abcdef"},
		},
	}
}

func TestSpecNormalizeDefaults(t *testing.T) {
	s := Spec{ServerID: "srv_1", Protocol: ProtoVLESS, Port: 443}
	s.Normalize()
	if s.Transport != TransportTCP {
		t.Fatalf("transport 默认值应为 tcp，得到 %s", s.Transport)
	}
	if s.Security != SecurityNone {
		t.Fatalf("security 默认值应为 none，得到 %s", s.Security)
	}
	if s.Listen != "0.0.0.0" {
		t.Fatalf("listen 默认值应为 0.0.0.0，得到 %s", s.Listen)
	}
	if s.ProtocolSettings.Decryption != "none" {
		t.Fatalf("vless 的 decryption 应默认 none")
	}
	if s.Tag == "" {
		t.Fatal("tag 应自动生成")
	}
}

func TestSpecValidateMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{"合法 REALITY", func(s *Spec) {}, false},
		{"REALITY 必须 TCP", func(s *Spec) { s.Transport = TransportWS }, true},
		{"REALITY 缺 dest", func(s *Spec) { s.Stream.RealityDest = "" }, true},
		{"REALITY 缺私钥", func(s *Spec) { s.Stream.RealityPrivateKey = "" }, true},
		{"REALITY 缺 serverNames", func(s *Spec) { s.Stream.RealityServerNames = nil }, true},
		{"TLS 缺 serverName", func(s *Spec) { s.Security = SecurityTLS }, true},
		{"TLS 合法", func(s *Spec) {
			s.Security = SecurityTLS
			s.Stream.ServerName = "a.example.com"
		}, false},
		{"非法协议", func(s *Spec) { s.Protocol = "wireguard" }, true},
		{"非法传输", func(s *Spec) { s.Transport = "quic" }, true},
		{"非法安全层", func(s *Spec) { s.Security = "mtls" }, true},
		{"端口越界", func(s *Spec) { s.Port = 70000 }, true},
		{"缺少 server", func(s *Spec) { s.ServerID = "" }, true},
		{"SS 缺口令", func(s *Spec) {
			s.Protocol = ProtoShadowsocks
			s.Security = SecurityNone
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := baseSpec()
			c.mutate(&s)
			s.Normalize()
			err := s.Validate()
			if c.wantErr && err == nil {
				t.Fatal("期望报错，实际通过")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("期望通过，实际报错: %v", err)
			}
		})
	}
}

func TestRenderVLESSReality(t *testing.T) {
	s := baseSpec()
	s.Normalize()
	in := s.ToStorage()
	in.ID = "inb_1"
	out, err := RenderXrayInbound(in, []storage.InboundClient{
		{ID: "c1", InboundID: "inb_1", UserID: "u1", UUID: "11111111-2222-3333-4444-555555555555", Email: "alice", Enabled: true},
	})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if out["protocol"] != "vless" {
		t.Fatalf("protocol 错误: %v", out["protocol"])
	}
	settings := out["settings"].(map[string]any)
	if settings["decryption"] != "none" {
		t.Fatal("vless 必须显式声明 decryption=none")
	}
	clients := settings["clients"].([]map[string]any)
	if len(clients) != 1 || clients[0]["id"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("clients 渲染错误: %+v", clients)
	}
	stream := out["streamSettings"].(map[string]any)
	if stream["security"] != "reality" {
		t.Fatalf("security 应为 reality: %v", stream["security"])
	}
	rs := stream["realitySettings"].(map[string]any)
	if rs["dest"] != "www.example.com:443" {
		t.Fatalf("reality dest 错误: %v", rs["dest"])
	}
	if rs["show"] != false {
		t.Fatal("realitySettings.show 必须为 false")
	}
}

func TestRenderSkipsDisabledClients(t *testing.T) {
	s := baseSpec()
	s.Normalize()
	in := s.ToStorage()
	out, err := RenderXrayInbound(in, []storage.InboundClient{
		{UUID: "enabled-uuid", Email: "a", Enabled: true},
		{UUID: "disabled-uuid", Email: "b", Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	clients := out["settings"].(map[string]any)["clients"].([]map[string]any)
	if len(clients) != 1 || clients[0]["id"] != "enabled-uuid" {
		t.Fatalf("被禁用的客户端不应出现在配置里: %+v", clients)
	}
}

func TestRenderTrojanUsesUUIDAsPassword(t *testing.T) {
	s := Spec{
		ServerID: "srv_1", Tag: "tj", Protocol: ProtoTrojan,
		Transport: TransportTCP, Security: SecurityTLS, Port: 8443,
		Stream: StreamSettings{ServerName: "a.example.com"},
	}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := RenderXrayInbound(s.ToStorage(), []storage.InboundClient{
		{UUID: "my-uuid", Email: "alice", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	clients := out["settings"].(map[string]any)["clients"].([]map[string]any)
	if clients[0]["password"] != "my-uuid" {
		t.Fatalf("trojan 应用 uuid 作为口令: %+v", clients[0])
	}
}

func TestRenderShadowsocksIsSingleCredential(t *testing.T) {
	s := Spec{
		ServerID: "srv_1", Tag: "ss", Protocol: ProtoShadowsocks,
		Transport: TransportTCP, Security: SecurityNone, Port: 8388,
		ProtocolSettings: ProtocolSettings{Method: "aes-256-gcm", Password: "secret"},
	}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := RenderXrayInbound(s.ToStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := out["settings"].(map[string]any)
	if settings["password"] != "secret" || settings["method"] != "aes-256-gcm" {
		t.Fatalf("SS 应使用服务端单一凭据: %+v", settings)
	}
}

func TestRenderTransportRendering(t *testing.T) {
	cases := []struct {
		transport string
		field     string
		mutate    func(*Spec)
	}{
		{TransportWS, "wsSettings", func(s *Spec) { s.Stream.WSPath = "/v2ray" }},
		{TransportGRPC, "grpcSettings", func(s *Spec) { s.Stream.ServiceName = "svc" }},
		{TransportHTTPUpgrade, "httpupgradeSettings", func(s *Spec) { s.Stream.HTTPUpgradePath = "/upgrade" }},
	}
	for _, c := range cases {
		t.Run(c.transport, func(t *testing.T) {
			s := Spec{
				ServerID: "srv_1", Tag: "t", Protocol: ProtoVLESS,
				Transport: c.transport, Security: SecurityNone, Port: 443,
			}
			c.mutate(&s)
			s.Normalize()
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			out, err := RenderXrayInbound(s.ToStorage(), nil)
			if err != nil {
				t.Fatal(err)
			}
			stream := out["streamSettings"].(map[string]any)
			if stream["network"] != c.transport {
				t.Fatalf("network 应为 %s: %v", c.transport, stream["network"])
			}
			if _, ok := stream[c.field]; !ok {
				t.Fatalf("缺少 %s: %+v", c.field, stream)
			}
		})
	}
}

func TestParseRoundTrip(t *testing.T) {
	s := baseSpec()
	s.Normalize()
	in := s.ToStorage()
	got := Parse(in)
	if got.Protocol != s.Protocol || got.Port != s.Port || got.Security != s.Security {
		t.Fatalf("往返后基础字段不一致: %+v vs %+v", got, s)
	}
	if got.Stream.RealityDest != s.Stream.RealityDest {
		t.Fatalf("往返后 stream 不一致: %+v", got.Stream)
	}
	if got.ProtocolSettings.Flow != s.ProtocolSettings.Flow {
		t.Fatalf("往返后 settings 不一致: %+v", got.ProtocolSettings)
	}
}

func TestSniffingDefaultWhenEmpty(t *testing.T) {
	s := baseSpec()
	s.Normalize()
	in := s.ToStorage()
	in.Sniffing = ""
	out, err := RenderXrayInbound(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out["sniffing"])
	if !strings.Contains(string(b), "destOverride") {
		t.Fatalf("空 sniffing 应回落为默认值: %s", b)
	}
}
