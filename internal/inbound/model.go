// Package inbound 定义入站的协议矩阵（协议 × 传输 × 安全层）与到 Xray 配置的渲染。
//
// 设计原则：**存储层只存 JSON 参数，本包负责把它翻译成 Xray 认识的形状**。
// 这样未来加协议只需改这里，不动数据库与 API。
package inbound

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mmx-project/mmx/internal/storage"
)

// 支持的协议。
const (
	ProtoVLESS       = "vless"
	ProtoVMess       = "vmess"
	ProtoTrojan      = "trojan"
	ProtoShadowsocks = "shadowsocks"
)

// 支持的传输层。
const (
	TransportTCP         = "tcp"
	TransportWS          = "ws"
	TransportGRPC        = "grpc"
	TransportHTTPUpgrade = "httpupgrade"
)

// 支持的安全层。
const (
	SecurityNone    = "none"
	SecurityTLS     = "tls"
	SecurityReality = "reality"
)

// SupportedProtocols 返回协议白名单。
func SupportedProtocols() []string {
	return []string{ProtoVLESS, ProtoVMess, ProtoTrojan, ProtoShadowsocks}
}

// SupportedTransports 返回传输白名单。
func SupportedTransports() []string {
	return []string{TransportTCP, TransportWS, TransportGRPC, TransportHTTPUpgrade}
}

// SupportedSecurities 返回安全层白名单。
func SupportedSecurities() []string {
	return []string{SecurityNone, SecurityTLS, SecurityReality}
}

// StreamSettings 是传输层与安全层的参数集合（落库为 inbounds.stream）。
type StreamSettings struct {
	// WebSocket
	WSPath string `json:"ws_path,omitempty"`
	WSHost string `json:"ws_host,omitempty"`

	// gRPC
	ServiceName string `json:"service_name,omitempty"`

	// HTTPUpgrade
	HTTPUpgradePath string `json:"httpupgrade_path,omitempty"`
	HTTPUpgradeHost string `json:"httpupgrade_host,omitempty"`

	// TLS
	ServerName string `json:"server_name,omitempty"`
	CertFile   string `json:"cert_file,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`

	// REALITY
	RealityDest        string   `json:"reality_dest,omitempty"`
	RealityServerNames []string `json:"reality_server_names,omitempty"`
	RealityPrivateKey  string   `json:"reality_private_key,omitempty"`
	RealityShortIDs    []string `json:"reality_short_ids,omitempty"`
}

// ProtocolSettings 是协议层参数集合（落库为 inbounds.settings）。
type ProtocolSettings struct {
	Flow       string `json:"flow,omitempty"`       // vless: xtls-rprx-vision
	AlterID    int    `json:"alter_id,omitempty"`   // vmess
	Method     string `json:"method,omitempty"`     // shadowsocks 加密方式
	Password   string `json:"password,omitempty"`   // shadowsocks 服务端口令
	Decryption string `json:"decryption,omitempty"` // vless: none
}

// Spec 是一次创建/更新入站的输入。
type Spec struct {
	ServerID  string
	Tag       string
	Protocol  string
	Transport string
	Security  string
	Port      int
	Listen    string
	Remark    string
	Enabled   bool

	ProtocolSettings ProtocolSettings
	Stream           StreamSettings
}

// Normalize 填充默认值。
func (s *Spec) Normalize() {
	s.Protocol = strings.ToLower(strings.TrimSpace(s.Protocol))
	s.Transport = strings.ToLower(strings.TrimSpace(s.Transport))
	s.Security = strings.ToLower(strings.TrimSpace(s.Security))
	if s.Listen == "" {
		s.Listen = "0.0.0.0"
	}
	if s.Transport == "" {
		s.Transport = TransportTCP
	}
	if s.Security == "" {
		s.Security = SecurityNone
	}
	if s.Tag == "" {
		s.Tag = fmt.Sprintf("%s-%d", s.Protocol, s.Port)
	}
	if s.Protocol == ProtoVLESS && s.ProtocolSettings.Decryption == "" {
		s.ProtocolSettings.Decryption = "none"
	}
	if s.Protocol == ProtoShadowsocks && s.ProtocolSettings.Method == "" {
		s.ProtocolSettings.Method = "aes-256-gcm"
	}
}

// Validate 校验协议矩阵组合。
func (s Spec) Validate() error {
	if s.ServerID == "" {
		return errors.New("必须指定 server_id")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("端口必须在 1-65535 之间，得到 %d", s.Port)
	}
	if !contains(SupportedProtocols(), s.Protocol) {
		return fmt.Errorf("不支持的协议 %q", s.Protocol)
	}
	if !contains(SupportedTransports(), s.Transport) {
		return fmt.Errorf("不支持的传输 %q", s.Transport)
	}
	if !contains(SupportedSecurities(), s.Security) {
		return fmt.Errorf("不支持的安全层 %q", s.Security)
	}
	// REALITY 只能跑在 TCP 上
	if s.Security == SecurityReality && s.Transport != TransportTCP {
		return errors.New("REALITY 仅支持 TCP 传输")
	}
	if s.Security == SecurityReality {
		if s.Stream.RealityDest == "" {
			return errors.New("REALITY 必须配置 reality_dest（伪装目标，形如 www.example.com:443）")
		}
		if s.Stream.RealityPrivateKey == "" {
			return errors.New("REALITY 必须配置 reality_private_key")
		}
		if len(s.Stream.RealityServerNames) == 0 {
			return errors.New("REALITY 必须配置至少一个 reality_server_names")
		}
	}
	if s.Security == SecurityTLS && s.Stream.ServerName == "" {
		return errors.New("TLS 必须配置 server_name（SNI）")
	}
	if s.Protocol == ProtoShadowsocks && s.ProtocolSettings.Password == "" {
		return errors.New("Shadowsocks 必须配置服务端口令")
	}
	return nil
}

// StreamJSON 序列化传输/安全参数。
func (s Spec) StreamJSON() string {
	b, _ := json.Marshal(s.Stream)
	return string(b)
}

// SettingsJSON 序列化协议参数。
func (s Spec) SettingsJSON() string {
	b, _ := json.Marshal(s.ProtocolSettings)
	return string(b)
}

// ToStorage 转换为存储层结构。
func (s Spec) ToStorage() storage.Inbound {
	return storage.Inbound{
		ServerID:  s.ServerID,
		Tag:       s.Tag,
		Protocol:  s.Protocol,
		Transport: s.Transport,
		Security:  s.Security,
		Port:      s.Port,
		Listen:    s.Listen,
		Settings:  s.SettingsJSON(),
		Stream:    s.StreamJSON(),
		Sniffing:  `{"enabled":true,"destOverride":["http","tls","quic"]}`,
		Enabled:   s.Enabled,
		Remark:    s.Remark,
	}
}

// Parse 从存储结构还原为 Spec。
func Parse(in storage.Inbound) Spec {
	var ps ProtocolSettings
	var ss StreamSettings
	_ = json.Unmarshal([]byte(in.Settings), &ps)
	_ = json.Unmarshal([]byte(in.Stream), &ss)
	return Spec{
		ServerID: in.ServerID, Tag: in.Tag, Protocol: in.Protocol,
		Transport: in.Transport, Security: in.Security, Port: in.Port,
		Listen: in.Listen, Remark: in.Remark, Enabled: in.Enabled,
		ProtocolSettings: ps, Stream: ss,
	}
}

// ---------------------------------------------------------------------------
// 渲染为 Xray inbound
// ---------------------------------------------------------------------------

// RenderXrayInbound 把入站 + 客户端列表渲染成 Xray 的 inbound 对象。
func RenderXrayInbound(in storage.Inbound, clients []storage.InboundClient) (map[string]any, error) {
	spec := Parse(in)

	// 先校验：宁可构建配置时大声失败，也不要下发一份 Xray 起不来的配置。
	// （DB 里可能有历史遗留或手工改坏的记录。）
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("入站 %s 参数非法: %w", in.Tag, err)
	}

	out := map[string]any{
		"tag":      in.Tag,
		"listen":   in.Listen,
		"port":     in.Port,
		"protocol": in.Protocol,
	}

	// ---- settings（协议层） ----
	switch in.Protocol {
	case ProtoVLESS:
		cs := make([]map[string]any, 0, len(clients))
		for _, c := range clients {
			if !c.Enabled {
				continue
			}
			entry := map[string]any{"id": c.UUID, "email": clientEmail(c)}
			if spec.ProtocolSettings.Flow != "" {
				entry["flow"] = spec.ProtocolSettings.Flow
			}
			cs = append(cs, entry)
		}
		out["settings"] = map[string]any{
			"clients":    cs,
			"decryption": orDefault(spec.ProtocolSettings.Decryption, "none"),
		}

	case ProtoVMess:
		cs := make([]map[string]any, 0, len(clients))
		for _, c := range clients {
			if !c.Enabled {
				continue
			}
			cs = append(cs, map[string]any{
				"id": c.UUID, "email": clientEmail(c),
				"alterId": spec.ProtocolSettings.AlterID, "level": 0,
			})
		}
		out["settings"] = map[string]any{"clients": cs}

	case ProtoTrojan:
		// Trojan 用口令认证；我们用客户端 UUID 作为其专属口令。
		cs := make([]map[string]any, 0, len(clients))
		for _, c := range clients {
			if !c.Enabled {
				continue
			}
			cs = append(cs, map[string]any{
				"password": c.UUID, "email": clientEmail(c), "level": 0,
			})
		}
		out["settings"] = map[string]any{
			"clients":   cs,
			"fallbacks": []map[string]any{},
		}

	case ProtoShadowsocks:
		// Shadowsocks 单口令模型：所有用户共享服务端口令。
		out["settings"] = map[string]any{
			"method":   orDefault(spec.ProtocolSettings.Method, "aes-256-gcm"),
			"password": spec.ProtocolSettings.Password,
			"network":  "tcp,udp",
		}

	default:
		return nil, fmt.Errorf("不支持的协议 %q", in.Protocol)
	}

	// ---- streamSettings（传输 + 安全） ----
	stream, err := renderStream(spec)
	if err != nil {
		return nil, err
	}
	out["streamSettings"] = stream

	// ---- sniffing ----
	var sniff map[string]any
	if err := json.Unmarshal([]byte(in.Sniffing), &sniff); err != nil || len(sniff) == 0 {
		sniff = map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}}
	}
	out["sniffing"] = sniff

	return out, nil
}

func renderStream(spec Spec) (map[string]any, error) {
	network := spec.Transport
	stream := map[string]any{"network": network}

	switch network {
	case TransportTCP:
		// TCP 无需额外字段（REALITY/TLS 在安全层处理）
	case TransportWS:
		ws := map[string]any{"path": orDefault(spec.Stream.WSPath, "/")}
		if spec.Stream.WSHost != "" {
			ws["host"] = spec.Stream.WSHost
		}
		stream["wsSettings"] = ws
	case TransportGRPC:
		stream["grpcSettings"] = map[string]any{
			"serviceName": orDefault(spec.Stream.ServiceName, "mmx"),
		}
	case TransportHTTPUpgrade:
		hu := map[string]any{"path": orDefault(spec.Stream.HTTPUpgradePath, "/")}
		if spec.Stream.HTTPUpgradeHost != "" {
			hu["host"] = spec.Stream.HTTPUpgradeHost
		}
		stream["httpupgradeSettings"] = hu
	default:
		return nil, fmt.Errorf("不支持的传输 %q", network)
	}

	switch spec.Security {
	case SecurityNone:
		stream["security"] = "none"

	case SecurityTLS:
		stream["security"] = "tls"
		tls := map[string]any{"serverName": spec.Stream.ServerName}
		if spec.Stream.CertFile != "" && spec.Stream.KeyFile != "" {
			tls["certificates"] = []map[string]any{{
				"certificateFile": spec.Stream.CertFile,
				"keyFile":         spec.Stream.KeyFile,
			}}
		}
		stream["tlsSettings"] = tls

	case SecurityReality:
		stream["security"] = "reality"
		shortIDs := spec.Stream.RealityShortIDs
		if len(shortIDs) == 0 {
			shortIDs = []string{""}
		}
		stream["realitySettings"] = map[string]any{
			"show":        false,
			"dest":        spec.Stream.RealityDest,
			"xver":        0,
			"serverNames": spec.Stream.RealityServerNames,
			"privateKey":  spec.Stream.RealityPrivateKey,
			"shortIds":    shortIDs,
		}

	default:
		return nil, fmt.Errorf("不支持的安全层 %q", spec.Security)
	}

	return stream, nil
}

func clientEmail(c storage.InboundClient) string {
	if c.Email != "" {
		return c.Email
	}
	return c.UserID
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
