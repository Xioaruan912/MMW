package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/mmx-project/mmx/internal/inbound"
)

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// RenderSingBox 生成 sing-box 配置。
//
// 只输出 outbounds 列表 + 一个 selector，便于用户并入自己的 sing-box 配置。
func RenderSingBox(entries []Entry) ([]byte, error) {
	outbounds := make([]map[string]any, 0, len(entries)+2)
	tags := make([]string, 0, len(entries))

	for _, e := range entries {
		ob, err := singBoxOutbound(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		outbounds = append(outbounds, ob)
		tags = append(tags, e.Name())
	}

	outbounds = append(outbounds,
		map[string]any{"type": "selector", "tag": "proxy", "outbounds": append([]string{"auto"}, tags...)},
		map[string]any{"type": "urltest", "tag": "auto", "outbounds": tags,
			"url": "http://www.gstatic.com/generate_204", "interval": "5m"},
		map[string]any{"type": "direct", "tag": "direct"},
	)

	doc := map[string]any{
		"log":       map[string]any{"level": "info"},
		"outbounds": outbounds,
		"route": map[string]any{
			"rules": []map[string]any{
				{"ip_is_private": true, "outbound": "direct"},
			},
			"final": "proxy",
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func singBoxOutbound(e Entry) (map[string]any, error) {
	in := e.Node.Inbound
	base := map[string]any{
		"tag":         e.Name(),
		"server":      e.Addr(),
		"server_port": in.Port,
	}

	// 传输
	switch in.Transport {
	case inbound.TransportWS:
		base["transport"] = map[string]any{
			"type":    "ws",
			"path":    orDefault(e.Spec.Stream.WSPath, "/"),
			"headers": map[string]any{"Host": firstNonEmpty(e.Spec.Stream.WSHost, e.Spec.Stream.ServerName)},
		}
	case inbound.TransportGRPC:
		base["transport"] = map[string]any{
			"type":         "grpc",
			"service_name": orDefault(e.Spec.Stream.ServiceName, "mmx"),
		}
	case inbound.TransportHTTPUpgrade:
		base["transport"] = map[string]any{
			"type": "httpupgrade",
			"path": orDefault(e.Spec.Stream.HTTPUpgradePath, "/"),
			"host": e.Spec.Stream.HTTPUpgradeHost,
		}
	}

	// TLS / REALITY
	switch in.Security {
	case inbound.SecurityTLS:
		base["tls"] = map[string]any{
			"enabled":     true,
			"server_name": e.Spec.Stream.ServerName,
		}
	case inbound.SecurityReality:
		pub, err := RealityPublicKey(e.Spec.Stream.RealityPrivateKey)
		if err != nil {
			return nil, err
		}
		sni := ""
		if len(e.Spec.Stream.RealityServerNames) > 0 {
			sni = e.Spec.Stream.RealityServerNames[0]
		}
		shortID := ""
		if len(e.Spec.Stream.RealityShortIDs) > 0 {
			shortID = e.Spec.Stream.RealityShortIDs[0]
		}
		base["tls"] = map[string]any{
			"enabled":     true,
			"server_name": sni,
			"utls":        map[string]any{"enabled": true, "fingerprint": "chrome"},
			"reality": map[string]any{
				"enabled":    true,
				"public_key": pub,
				"short_id":   shortID,
			},
		}
	}

	switch in.Protocol {
	case inbound.ProtoVLESS:
		base["type"] = "vless"
		base["uuid"] = e.Client.UUID
		if e.Spec.ProtocolSettings.Flow != "" {
			base["flow"] = e.Spec.ProtocolSettings.Flow
		}
	case inbound.ProtoVMess:
		base["type"] = "vmess"
		base["uuid"] = e.Client.UUID
		base["security"] = "auto"
		base["alter_id"] = e.Spec.ProtocolSettings.AlterID
	case inbound.ProtoTrojan:
		base["type"] = "trojan"
		base["password"] = e.Client.UUID
		if _, ok := base["tls"]; !ok {
			base["tls"] = map[string]any{"enabled": true, "server_name": e.Spec.Stream.ServerName}
		}
	case inbound.ProtoShadowsocks:
		base["type"] = "shadowsocks"
		base["method"] = orDefault(e.Spec.ProtocolSettings.Method, "aes-256-gcm")
		base["password"] = e.Spec.ProtocolSettings.Password
	default:
		return nil, fmt.Errorf("不支持的协议 %q", in.Protocol)
	}
	return base, nil
}
