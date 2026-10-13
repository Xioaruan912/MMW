package subscription

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmx-project/mmx/internal/inbound"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func urlEnc(s string) string { return url.QueryEscape(s) }

// ---------------------------------------------------------------------------
// Clash / mihomo
// ---------------------------------------------------------------------------

// ClashOptions 控制生成的 Clash 配置。
type ClashOptions struct {
	// 代理组名（select 组）
	ProxyGroupName string
	// 是否附加地区自动分组
	AutoRegionGroups bool
	// DNS / 规则是否使用精简模板
	Minimal bool
}

// DefaultClashOptions 返回默认值。
func DefaultClashOptions() ClashOptions {
	return ClashOptions{ProxyGroupName: "🚀 节点选择", AutoRegionGroups: true}
}

// RenderClash 生成 mihomo / Clash.Meta 可用的 YAML 配置。
func RenderClash(entries []Entry, opts ClashOptions) ([]byte, error) {
	if opts.ProxyGroupName == "" {
		opts.ProxyGroupName = "🚀 节点选择"
	}

	proxies := make([]map[string]any, 0, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		p, err := clashProxy(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		proxies = append(proxies, p)
		names = append(names, e.Name())
	}

	groups := []map[string]any{{
		"name":    opts.ProxyGroupName,
		"type":    "select",
		"proxies": append([]string{"♻️ 自动选择", "DIRECT"}, names...),
	}}
	groups = append(groups, map[string]any{
		"name":      "♻️ 自动选择",
		"type":      "url-test",
		"url":       "http://www.gstatic.com/generate_204",
		"interval":  300,
		"tolerance": 50,
		"proxies":   names,
	})

	doc := map[string]any{
		"mixed-port":                7890,
		"allow-lan":                 false,
		"mode":                      "rule",
		"log-level":                 "info",
		"external-controller":       "127.0.0.1:9090",
		"unified-delay":             true,
		"tcp-concurrent":            true,
		"find-process-mode":         "strict",
		"global-client-fingerprint": "chrome",
		"proxies":                   proxies,
		"proxy-groups":              groups,
		"rules": []string{
			"GEOIP,LAN,DIRECT,no-resolve",
			"GEOIP,CN,DIRECT",
			"MATCH," + opts.ProxyGroupName,
		},
	}

	var sb strings.Builder
	sb.WriteString("# 由 MMX 生成 —— 订阅内容随节点变化自动更新\n")
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return []byte(sb.String()), nil
}

func clashProxy(e Entry) (map[string]any, error) {
	in := e.Node.Inbound
	m := map[string]any{
		"name":   e.Name(),
		"server": e.Addr(),
		"port":   in.Port,
		"udp":    true,
	}

	switch in.Transport {
	case inbound.TransportWS:
		m["network"] = "ws"
		m["ws-opts"] = map[string]any{
			"path":    orDefault(e.Spec.Stream.WSPath, "/"),
			"headers": map[string]any{"Host": firstNonEmpty(e.Spec.Stream.WSHost, e.Spec.Stream.ServerName)},
		}
	case inbound.TransportGRPC:
		m["network"] = "grpc"
		m["grpc-opts"] = map[string]any{
			"grpc-service-name": orDefault(e.Spec.Stream.ServiceName, "mmx"),
		}
	default:
		m["network"] = "tcp"
	}

	switch in.Protocol {
	case inbound.ProtoVLESS:
		m["type"] = "vless"
		m["uuid"] = e.Client.UUID
		if e.Spec.ProtocolSettings.Flow != "" {
			m["flow"] = e.Spec.ProtocolSettings.Flow
		}
	case inbound.ProtoVMess:
		m["type"] = "vmess"
		m["uuid"] = e.Client.UUID
		m["alterId"] = e.Spec.ProtocolSettings.AlterID
		m["cipher"] = "auto"
	case inbound.ProtoTrojan:
		m["type"] = "trojan"
		m["password"] = e.Client.UUID
		m["client-fingerprint"] = "chrome"
	case inbound.ProtoShadowsocks:
		m["type"] = "ss"
		m["cipher"] = orDefault(e.Spec.ProtocolSettings.Method, "aes-256-gcm")
		m["password"] = e.Spec.ProtocolSettings.Password
		delete(m, "network")
	default:
		return nil, fmt.Errorf("不支持的协议 %q", in.Protocol)
	}

	switch in.Security {
	case inbound.SecurityTLS:
		m["tls"] = true
		m["servername"] = e.Spec.Stream.ServerName
		m["skip-cert-verify"] = false
	case inbound.SecurityReality:
		pub, err := RealityPublicKey(e.Spec.Stream.RealityPrivateKey)
		if err != nil {
			return nil, err
		}
		m["tls"] = true
		m["client-fingerprint"] = "chrome"
		if len(e.Spec.Stream.RealityServerNames) > 0 {
			m["servername"] = e.Spec.Stream.RealityServerNames[0]
		}
		ro := map[string]any{"public-key": pub}
		if len(e.Spec.Stream.RealityShortIDs) > 0 && e.Spec.Stream.RealityShortIDs[0] != "" {
			ro["short-id"] = e.Spec.Stream.RealityShortIDs[0]
		}
		m["reality-opts"] = ro
	}

	return m, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 其他格式的占位（按 FEATURE-MATRIX C3/C4 逐步补齐）
// ---------------------------------------------------------------------------

// Format 是订阅输出格式。
type Format string

const (
	FormatClash   Format = "clash"
	FormatURI     Format = "uri"
	FormatBase64  Format = "base64" // URI 列表的 base64 包装（Shadowrocket / v2rayN 等）
	FormatV2Ray   Format = "v2ray"  // 与 base64 相同，语义更明确
	FormatSingBox Format = "singbox"
	FormatStash   Format = "stash" // Stash 兼容 Clash，实际走 FormatClash
)

// 已识别但尚未实现的格式。
//
// 这些客户端的配置格式与 Clash / URI 都不同，因此**不能静默降级**——
// 否则用户拿到一份看着像配置、实则不可用的文件，排障成本极高。
const (
	FormatSurge Format = "surge"
	FormatLoon  Format = "loon"
	FormatQX    Format = "qx"
)

// UnimplementedFormats 返回尚未实现的格式及其计划阶段。
func UnimplementedFormats() map[Format]string {
	return map[Format]string{
		FormatSurge: "P7",
		FormatLoon:  "P7",
		FormatQX:    "P7",
	}
}

// DetectFormat 按 User-Agent 猜测客户端期望的格式。
//
// 只做**语义正确**的映射：
//   - Clash / mihomo / Stash / Surfboard / ClashX 家族 → Clash YAML
//   - Shadowrocket / v2rayN / NekoBox / Hiddify 等 → base64 包装的 URI 列表
//   - sing-box → JSON
//   - Surge / Loon / QuantumultX → 各自的私有格式（尚未实现，返回明确错误而非降级）
func DetectFormat(ua string) Format {
	u := strings.ToLower(ua)
	switch {
	case u == "":
		return FormatClash
	case strings.Contains(u, "sing-box"), strings.Contains(u, "singbox"):
		return FormatSingBox
	case strings.Contains(u, "surge"):
		return FormatSurge
	case strings.Contains(u, "loon"):
		return FormatLoon
	case strings.Contains(u, "quantumult"):
		return FormatQX
	case strings.Contains(u, "clash"), strings.Contains(u, "mihomo"),
		strings.Contains(u, "stash"), strings.Contains(u, "surfboard"):
		return FormatClash
	case strings.Contains(u, "shadowrocket"), strings.Contains(u, "v2ray"),
		strings.Contains(u, "v2rayn"), strings.Contains(u, "nekobox"),
		strings.Contains(u, "karing"), strings.Contains(u, "hiddify"):
		return FormatBase64
	default:
		return FormatClash
	}
}

// Render 按格式渲染订阅内容。
func Render(entries []Entry, f Format) ([]byte, string, error) {
	switch f {
	case FormatClash, FormatStash:
		b, err := RenderClash(entries, DefaultClashOptions())
		return b, "text/yaml; charset=utf-8", err

	case FormatURI:
		uris, err := RenderURIs(entries)
		if err != nil {
			return nil, "", err
		}
		return []byte(strings.Join(uris, "\n") + "\n"), "text/plain; charset=utf-8", nil

	case FormatBase64, FormatV2Ray:
		uris, err := RenderURIs(entries)
		if err != nil {
			return nil, "", err
		}
		plain := strings.Join(uris, "\n") + "\n"
		return []byte(base64Std(plain)), "text/plain; charset=utf-8", nil

	case FormatSingBox:
		b, err := RenderSingBox(entries)
		return b, "application/json; charset=utf-8", err

	default:
		if stage, ok := UnimplementedFormats()[f]; ok {
			return nil, "", fmt.Errorf(
				"订阅格式 %q 尚未实现（计划于 %s）；请用 ?format=clash 或 ?format=uri 手动指定", f, stage)
		}
		return nil, "", fmt.Errorf("不支持的订阅格式 %q", f)
	}
}
