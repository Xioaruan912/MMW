package subscription

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Surge（Surge 5）
// ---------------------------------------------------------------------------
//
// 语法（[Proxy] 段）：
//
//	名称 = 协议, 主机, 端口, 凭据参数, 传输参数, 安全参数
//
// 参考 Surge 官方文档的 Proxy Policy 一节。Surge 只支持 vless / vmess / trojan / ss / hysteria2。
func RenderSurge(entries []Entry) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("#!MANAGED-CONFIG interval=3600\n")
	sb.WriteString("# 由 MMX 生成 —— 订阅内容随节点变化自动更新\n\n")
	sb.WriteString("[Proxy]\n")
	sb.WriteString("DIRECT = direct\n")

	var names []string
	for _, e := range entries {
		p, err := Extract(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		line, err := surgeProxyLine(p)
		if err != nil {
			return nil, err
		}
		sb.WriteString(line + "\n")
		names = append(names, sanitizeName(p.Name))
	}

	sb.WriteString("\n[Proxy Group]\n")
	sb.WriteString("PROXY = select, " + strings.Join(append([]string{"DIRECT"}, names...), ", ") + "\n")

	sb.WriteString("\n[Rule]\n")
	sb.WriteString("GEOIP,CN,DIRECT\n")
	sb.WriteString("FINAL,PROXY\n")
	return []byte(sb.String()), nil
}

func surgeProxyLine(p nodeParams) (string, error) {
	name := sanitizeName(p.Name)
	var b kv

	switch p.Proto {
	case "vless":
		b.pairs = append(b.pairs, "vless")
	case "vmess":
		b.pairs = append(b.pairs, "vmess")
	case "trojan":
		b.pairs = append(b.pairs, "trojan")
	case "shadowsocks":
		b.pairs = append(b.pairs, "ss")
	default:
		return "", fmt.Errorf("Surge 不支持协议 %q", p.Proto)
	}
	b.pairs = append(b.pairs, p.Server)
	b.pairs = append(b.pairs, fmt.Sprintf("%d", p.Port))

	switch p.Proto {
	case "shadowsocks":
		b.add("encrypt-method", p.Method)
		b.add("password", p.SSPass)
	default:
		b.add("username", p.Cred)
	}
	if p.Proto == "vmess" {
		b.addInt("vmess-aead", 1)
	}
	if p.Flow != "" && p.Proto == "vless" {
		b.add("flow", p.Flow)
	}

	// 传输
	switch p.Network {
	case "ws":
		b.addBool("ws", true)
		b.add("ws-path", p.WSPath)
		if p.WSHost != "" {
			b.add("ws-headers", "Host:"+p.WSHost)
		}
	case "grpc":
		b.addBool("grpc", true)
		b.add("grpc-service-name", p.ServiceName)
	case "httpupgrade":
		// Surge 无 httpupgrade，退化为 ws 语义会静默出错 —— 明确报错
		return "", fmt.Errorf("Surge 不支持 httpupgrade 传输（节点 %s）", p.Name)
	}

	// 安全层
	switch p.Security {
	case SecurityTLSName:
		b.addBool("tls", true)
		b.add("sni", p.SNI)
	case SecurityRealityName:
		b.addBool("tls", true)
		b.add("sni", p.SNI)
		b.add("reality-public-key", p.RealityPubKey)
		b.add("reality-short-id", p.RealityShortID)
	}

	return name + " = " + b.String(), nil
}

// ---------------------------------------------------------------------------
// Surfboard
// ---------------------------------------------------------------------------
//
// Surfboard 基于 mihomo，但订阅格式是 INI（与 Surge 家族接近）。
// 差异：不支持 REALITY，且用 tls/sni/ws 等相同的键名。
func RenderSurfboard(entries []Entry) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("# 由 MMX 生成（Surfboard）\n")
	sb.WriteString("[General]\nbypass-system = true\n\n[Proxy]\n")
	sb.WriteString("DIRECT = direct\n")

	var names []string
	for _, e := range entries {
		p, err := Extract(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		if p.Security == SecurityRealityName {
			// 静默降级会让用户以为连上了但其实没有 —— 明确跳过并说明
			return nil, fmt.Errorf("Surfboard 不支持 REALITY（节点 %s）；请改用 Clash/mihomo 或 Surge", p.Name)
		}
		line, err := surgeProxyLine(p)
		if err != nil {
			return nil, err
		}
		sb.WriteString(line + "\n")
		names = append(names, sanitizeName(p.Name))
	}

	sb.WriteString("\n[Proxy Group]\n")
	sb.WriteString("PROXY = select, " + strings.Join(append([]string{"DIRECT"}, names...), ", ") + "\n")
	sb.WriteString("\n[Rule]\nGEOIP,CN,DIRECT\nFINAL,PROXY\n")
	return []byte(sb.String()), nil
}

// ---------------------------------------------------------------------------
// Loon
// ---------------------------------------------------------------------------
//
// 语法（[Proxy] 段）：
//
//	名称 = 协议, 主机, 端口, 凭据, 参数...
func RenderLoon(entries []Entry) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("# 由 MMX 生成（Loon）\n")
	sb.WriteString("[Proxy]\n")
	sb.WriteString("DIRECT = direct\n")

	var names []string
	for _, e := range entries {
		p, err := Extract(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		line, err := loonProxyLine(p)
		if err != nil {
			return nil, err
		}
		sb.WriteString(line + "\n")
		names = append(names, sanitizeName(p.Name))
	}

	sb.WriteString("\n[Proxy Group]\n")
	sb.WriteString("PROXY = select, DIRECT, " + strings.Join(names, ", ") + "\n")
	sb.WriteString("\n[Rule]\nGEOIP,CN,DIRECT\nFINAL,PROXY\n")
	return []byte(sb.String()), nil
}

func loonProxyLine(p nodeParams) (string, error) {
	name := sanitizeName(p.Name)
	var b kv
	var proto string

	switch p.Proto {
	case "vless":
		proto = "VLESS"
	case "vmess":
		proto = "vmess"
	case "trojan":
		proto = "trojan"
	case "shadowsocks":
		proto = "shadowsocks"
	default:
		return "", fmt.Errorf("Loon 不支持协议 %q", p.Proto)
	}
	b.pairs = append(b.pairs, proto, p.Server, fmt.Sprintf("%d", p.Port))

	if p.Proto == "shadowsocks" {
		b.pairs = append(b.pairs, fmt.Sprintf(`"%s:%s"`, p.Method, p.SSPass))
	} else {
		b.pairs = append(b.pairs, fmt.Sprintf(`"%s"`, p.Cred))
	}

	b.add("transport", p.Network)
	switch p.Network {
	case "ws":
		b.add("path", p.WSPath)
		b.add("host", p.WSHost)
	case "grpc":
		b.add("serviceName", p.ServiceName)
	case "tcp":
		// tcp 是默认值，Loon 不需要显式声明
	}

	switch p.Security {
	case SecurityTLSName:
		b.addBool("over-tls", true)
		b.add("tls-name", p.SNI)
	case SecurityRealityName:
		b.addBool("over-tls", true)
		b.add("tls-name", p.SNI)
		b.add("public-key", p.RealityPubKey)
		b.add("short-id", p.RealityShortID)
	}
	if p.Flow != "" && p.Proto == "vless" {
		b.add("flow", p.Flow)
	}

	return name + " = " + b.String(), nil
}

// ---------------------------------------------------------------------------
// QuantumultX
// ---------------------------------------------------------------------------
//
// 语法（[server_local] 段）：
//
//	vless=主机:端口, method=none, password=uuid, obfs=over-tls, obfs-host=..., tag=名称
func RenderQuantumultX(entries []Entry) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("# 由 MMX 生成（Quantumult X）\n")
	sb.WriteString("[server_local]\n")

	for _, e := range entries {
		p, err := Extract(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		line, err := qxProxyLine(p)
		if err != nil {
			return nil, err
		}
		sb.WriteString(line + "\n")
	}

	sb.WriteString("\n[policy]\nstatic=PROXY, DIRECT\n")
	sb.WriteString("\n[filter_local]\ngeoip, cn, DIRECT\nfinal, PROXY\n")
	return []byte(sb.String()), nil
}

func qxProxyLine(p nodeParams) (string, error) {
	var b kv
	var tag string

	switch p.Proto {
	case "vless":
		tag = "vless"
		b.add("method", "none")
		b.add("password", p.Cred)
	case "vmess":
		tag = "vmess"
		b.add("method", "chacha20-poly1305")
		b.add("password", p.Cred)
	case "trojan":
		tag = "trojan"
		b.add("password", p.Cred)
	case "shadowsocks":
		tag = "shadowsocks"
		b.add("method", p.Method)
		b.add("password", p.SSPass)
	default:
		return "", fmt.Errorf("QuantumultX 不支持协议 %q", p.Proto)
	}

	// QX 的 HTTP/WS 伪装
	switch p.Network {
	case "ws":
		b.add("obfs", "wss")
		b.add("obfs-host", p.WSHost)
		b.add("obfs-uri", p.WSPath)
	case "tcp":
		if p.Security != "none" {
			b.add("obfs", "over-tls")
			b.add("obfs-host", p.SNI)
		}
	default:
		return "", fmt.Errorf("QuantumultX 不支持 %s 传输（节点 %s）", p.Network, p.Name)
	}

	if p.Security == SecurityRealityName {
		// QX 的 REALITY 支持与版本相关，且参数键名不稳定 —— 不猜，明确报错
		return "", fmt.Errorf("QuantumultX 不支持 REALITY（节点 %s）；请改用 Clash/mihomo", p.Name)
	}
	if p.Security == SecurityTLSName {
		b.addBool("tls-verification", true)
	}

	b.add("fast-open", "false")
	b.add("udp-relay", "true")
	b.add("tag", sanitizeName(p.Name))
	return tag + "=" + p.HostPort() + ", " + b.String(), nil
}
