package subscription

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mmx-project/mmx/internal/inbound"
)

// nodeParams 是从 Entry 提取出的「与具体客户端无关」的节点参数。
//
// 各客户端渲染器（Clash / Surge / Loon / QX / SingBox / URI）都从这里取数，
// 避免每个渲染器各写一遍解析逻辑 —— 那样早晚会漂移出不一致的行为。
type nodeParams struct {
	Name    string
	Server  string
	Port    int
	Proto   string // vless | vmess | trojan | shadowsocks
	Cred    string // vless/vmess 的 uuid，trojan 的口令
	Method  string // shadowsocks 加密方式
	SSPass  string // shadowsocks 服务端口令
	AlterID int
	Flow    string

	Network     string // tcp | ws | grpc | httpupgrade
	WSPath      string
	WSHost      string
	ServiceName string
	HUPath      string
	HUHost      string

	Security       string // none | tls | reality
	SNI            string
	RealityPubKey  string
	RealityShortID string

	UDP bool
}

// Extract 把 Entry 规整成 nodeParams。
func Extract(e Entry) (nodeParams, error) {
	in := e.Node.Inbound
	p := nodeParams{
		Name:    e.Name(),
		Server:  e.Addr(),
		Port:    in.Port,
		Proto:   in.Protocol,
		Cred:    e.Client.UUID,
		Method:  orDefault(e.Spec.ProtocolSettings.Method, "aes-256-gcm"),
		SSPass:  e.Spec.ProtocolSettings.Password,
		AlterID: e.Spec.ProtocolSettings.AlterID,
		Flow:    e.Spec.ProtocolSettings.Flow,
		Network: in.Transport,
		// 传输
		WSPath:      orDefault(e.Spec.Stream.WSPath, "/"),
		WSHost:      e.Spec.Stream.WSHost,
		ServiceName: orDefault(e.Spec.Stream.ServiceName, "mmx"),
		HUPath:      orDefault(e.Spec.Stream.HTTPUpgradePath, "/"),
		HUHost:      e.Spec.Stream.HTTPUpgradeHost,
		// 安全
		Security: in.Security,
		SNI:      e.Spec.Stream.ServerName,
		UDP:      true,
	}
	if p.Server == "" {
		return p, errors.New("节点缺少服务器地址")
	}
	if in.Protocol == inbound.ProtoShadowsocks && p.SSPass == "" {
		return p, errors.New("Shadowsocks 节点缺少服务端口令")
	}
	if in.Protocol != inbound.ProtoShadowsocks && p.Cred == "" {
		return p, errors.New("节点缺少客户端凭据")
	}

	if in.Security == inbound.SecurityReality {
		pub, err := RealityPublicKey(e.Spec.Stream.RealityPrivateKey)
		if err != nil {
			return p, fmt.Errorf("REALITY 公钥派生失败: %w", err)
		}
		p.RealityPubKey = pub
		if len(e.Spec.Stream.RealityServerNames) > 0 {
			p.SNI = e.Spec.Stream.RealityServerNames[0]
		}
		if len(e.Spec.Stream.RealityShortIDs) > 0 {
			p.RealityShortID = e.Spec.Stream.RealityShortIDs[0]
		}
	}
	if in.Security == inbound.SecurityTLS && p.SNI == "" {
		p.SNI = p.Server
	}
	if p.WSHost == "" && p.SNI != "" {
		p.WSHost = p.SNI
	}
	return p, nil
}

// HostPort 返回 host:port。
func (p nodeParams) HostPort() string {
	return p.Server + ":" + strconv.Itoa(p.Port)
}

// isTLS 判断是否需要在客户端开启 TLS 语义（REALITY 也走 TLS 栈）。
func (p nodeParams) isTLS() bool {
	return p.Security == SecurityTLSName || p.Security == SecurityRealityName
}

// 避免与 inbound 包的常量名冲突，这里用本地别名。
const (
	SecurityTLSName     = "tls"
	SecurityRealityName = "reality"
)

// kv 是 INI 风格渲染器的辅助：按序拼接 key=value，跳过空值。
type kv struct {
	pairs []string
}

func (k *kv) add(key, val string) {
	if val == "" {
		return
	}
	k.pairs = append(k.pairs, key+"="+val)
}

func (k *kv) addBool(key string, v bool) {
	if !v {
		return
	}
	k.pairs = append(k.pairs, key+"=true")
}

func (k *kv) addInt(key string, v int) {
	if v == 0 {
		return
	}
	k.pairs = append(k.pairs, key+"="+strconv.Itoa(v))
}

func (k *kv) String() string { return strings.Join(k.pairs, ", ") }

// sanitizeName 清理客户端配置里不允许的字符（逗号 / 等号会破坏 INI 解析）。
func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, ",", " ")
	s = strings.ReplaceAll(s, "=", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
