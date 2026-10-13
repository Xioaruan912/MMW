// Package subscription 把「节点 + 用户凭据」渲染成各客户端可用的订阅内容。
//
// 支持两类输出：
//
//	Clash / mihomo  —— YAML 配置（proxies + proxy-groups + rules）
//	URI 列表        —— vless:// vmess:// trojan:// ss:// ，供 Shadowrocket / v2rayN 等
//
// 后续按 docs/FEATURE-MATRIX.md C2/C3/C4 扩展到 Surge / Loon / QX / Stash / SingBox 等。
package subscription

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/curve25519"

	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/storage"
)

// Entry 是一个"用户可用的节点"：节点展示信息 + 该用户的凭据 + 派生的连接参数。
type Entry struct {
	Node   storage.NodeView
	Client storage.InboundClient
	Spec   inbound.Spec
}

// Addr 返回对外连接地址（节点展示的服务器地址优先于 Agent 上报的地址）。
func (e Entry) Addr() string {
	if e.Node.ServerAddr != "" {
		return e.Node.ServerAddr
	}
	return e.Node.Inbound.Listen
}

// Name 返回客户端里显示的名字。
func (e Entry) Name() string {
	if e.Node.Name != "" {
		return e.Node.Name
	}
	return e.Node.Inbound.Tag
}

// BuildEntries 把节点列表与该用户的凭据合并；没有凭据的节点会被跳过。
//
// clients 以 inbound ID 为键。
func BuildEntries(nodes []storage.NodeView, clients map[string]storage.InboundClient) []Entry {
	out := make([]Entry, 0, len(nodes))
	for _, n := range nodes {
		if !n.Enabled || !n.Inbound.Enabled {
			continue
		}
		c, ok := clients[n.InboundID]
		if !ok || !c.Enabled {
			continue
		}
		out = append(out, Entry{Node: n, Client: c, Spec: inbound.Parse(n.Inbound)})
	}
	return out
}

// ---------------------------------------------------------------------------
// REALITY 公钥派生
// ---------------------------------------------------------------------------

// RealityPublicKey 从服务端私钥派生客户端需要的公钥。
//
// Xray 的 REALITY 用 x25519：public = X25519(private, basepoint)。
// 私钥可能是 base64url（无填充）或标准 base64，两种都接受。
func RealityPublicKey(privateKey string) (string, error) {
	priv, err := decodeB64Any(privateKey)
	if err != nil {
		return "", fmt.Errorf("REALITY 私钥解码失败: %w", err)
	}
	if len(priv) != 32 {
		return "", fmt.Errorf("REALITY 私钥长度应为 32 字节，得到 %d", len(priv))
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

func decodeB64Any(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("不是合法的 base64")
}

// ---------------------------------------------------------------------------
// URI 渲染
// ---------------------------------------------------------------------------

// RenderURIs 输出 URI 行列表。
func RenderURIs(entries []Entry) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		s, err := renderURI(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		out = append(out, s)
	}
	return out, nil
}

func renderURI(e Entry) (string, error) {
	in := e.Node.Inbound
	addr := e.Addr()
	name := e.Name()

	// 传输参数
	q := []string{"type=" + in.Transport}
	switch in.Transport {
	case inbound.TransportWS:
		q = append(q, "path="+urlEnc(orDefault(e.Spec.Stream.WSPath, "/")))
		if e.Spec.Stream.WSHost != "" {
			q = append(q, "host="+urlEnc(e.Spec.Stream.WSHost))
		}
	case inbound.TransportGRPC:
		q = append(q, "serviceName="+urlEnc(orDefault(e.Spec.Stream.ServiceName, "mmx")))
	case inbound.TransportHTTPUpgrade:
		q = append(q, "path="+urlEnc(orDefault(e.Spec.Stream.HTTPUpgradePath, "/")))
		if e.Spec.Stream.HTTPUpgradeHost != "" {
			q = append(q, "host="+urlEnc(e.Spec.Stream.HTTPUpgradeHost))
		}
	}

	// 安全参数
	switch in.Security {
	case inbound.SecurityTLS:
		q = append(q, "security=tls", "sni="+urlEnc(e.Spec.Stream.ServerName))
	case inbound.SecurityReality:
		pub, err := RealityPublicKey(e.Spec.Stream.RealityPrivateKey)
		if err != nil {
			return "", err
		}
		q = append(q, "security=reality", "pbk="+urlEnc(pub), "fp=chrome")
		if len(e.Spec.Stream.RealityServerNames) > 0 {
			q = append(q, "sni="+urlEnc(e.Spec.Stream.RealityServerNames[0]))
		}
		if len(e.Spec.Stream.RealityShortIDs) > 0 && e.Spec.Stream.RealityShortIDs[0] != "" {
			q = append(q, "sid="+urlEnc(e.Spec.Stream.RealityShortIDs[0]))
		}
	default:
		q = append(q, "security=none")
	}

	qs := strings.Join(q, "&")
	frag := "#" + urlEnc(name)

	switch in.Protocol {
	case inbound.ProtoVLESS:
		if e.Spec.ProtocolSettings.Flow != "" {
			qs += "&flow=" + urlEnc(e.Spec.ProtocolSettings.Flow)
		}
		return fmt.Sprintf("vless://%s@%s:%d?%s%s", e.Client.UUID, addr, in.Port, qs, frag), nil

	case inbound.ProtoTrojan:
		// Trojan 用 UUID 作为该用户的口令
		return fmt.Sprintf("trojan://%s@%s:%d?%s%s", e.Client.UUID, addr, in.Port, qs, frag), nil

	case inbound.ProtoVMess:
		obj := map[string]any{
			"v": "2", "ps": name, "add": addr, "port": in.Port,
			"id": e.Client.UUID, "aid": e.Spec.ProtocolSettings.AlterID, "scy": "auto",
			"net": in.Transport, "type": "none", "host": "", "path": "",
			"tls": "", "sni": "",
		}
		if in.Transport == inbound.TransportWS {
			obj["path"] = orDefault(e.Spec.Stream.WSPath, "/")
			obj["host"] = e.Spec.Stream.WSHost
		}
		if in.Transport == inbound.TransportGRPC {
			obj["path"] = orDefault(e.Spec.Stream.ServiceName, "mmx")
		}
		switch in.Security {
		case inbound.SecurityTLS:
			obj["tls"] = "tls"
			obj["sni"] = e.Spec.Stream.ServerName
		case inbound.SecurityReality:
			pub, err := RealityPublicKey(e.Spec.Stream.RealityPrivateKey)
			if err != nil {
				return "", err
			}
			obj["tls"] = "reality"
			obj["pbk"] = pub
			if len(e.Spec.Stream.RealityServerNames) > 0 {
				obj["sni"] = e.Spec.Stream.RealityServerNames[0]
			}
		}
		b, err := jsonMarshal(obj)
		if err != nil {
			return "", err
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(b), nil

	case inbound.ProtoShadowsocks:
		userinfo := base64.RawURLEncoding.EncodeToString(
			[]byte(orDefault(e.Spec.ProtocolSettings.Method, "aes-256-gcm") + ":" + e.Spec.ProtocolSettings.Password))
		return fmt.Sprintf("ss://%s@%s:%d%s", userinfo, addr, in.Port, frag), nil
	}
	return "", fmt.Errorf("不支持的协议 %q", in.Protocol)
}
