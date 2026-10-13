// Package agentlink 是主控侧的 Agent 编排核心。
//
// 职责：
//   - 服务器与 Agent 令牌的生命周期（registry.go）
//   - WebSocket 连接注册表、任务派发、配置下发（hub.go）
//   - 与 Agent 的握手与 IP 独占校验（ws.go）
//   - 一键安装脚本生成（installer.go）
//   - 在线/离线判定（sweeper.go）
package agentlink

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mmx-project/mmx/internal/auth"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// EnrollTokenTTL 是一次性入驻令牌的有效期。
const EnrollTokenTTL = 30 * time.Minute

// RegistryOptions 构造 Registry 所需的依赖。
type RegistryOptions struct {
	Store         *storage.Store
	Heartbeat     time.Duration
	Metrics       time.Duration
	OfflineFactor int
	PublicKey     ed25519.PublicKey
}

// Registry 管理服务器与令牌的业务规则。
type Registry struct {
	store         *storage.Store
	heartbeat     time.Duration
	metrics       time.Duration
	offlineFactor int
	publicKey     ed25519.PublicKey
}

// NewRegistry 构造 Registry。
func NewRegistry(o RegistryOptions) *Registry {
	return &Registry{
		store:         o.Store,
		heartbeat:     o.Heartbeat,
		metrics:       o.Metrics,
		offlineFactor: o.OfflineFactor,
		publicKey:     o.PublicKey,
	}
}

// Store 暴露底层存储。
func (r *Registry) Store() *storage.Store { return r.store }

// HeartbeatInterval 返回心跳间隔。
func (r *Registry) HeartbeatInterval() time.Duration { return r.heartbeat }

// MetricsInterval 返回指标上报间隔。
func (r *Registry) MetricsInterval() time.Duration { return r.metrics }

// OfflineFactor 返回离线判定倍数。
func (r *Registry) OfflineFactor() int { return r.offlineFactor }

// PublicKey 返回主控签名公钥。
func (r *Registry) PublicKey() ed25519.PublicKey { return r.publicKey }

// ---------------------------------------------------------------------------
// 服务器 + 令牌
// ---------------------------------------------------------------------------

// CreatedServer 是"新建服务器"的结果，仅在这里返回一次性 token 明文。
type CreatedServer struct {
	Server      storage.Server
	EnrollToken string // 明文，只返回一次
	ExpiresAt   int64
}

// CreateServer 新建服务器，并同时签发一枚一次性入驻令牌。
func (r *Registry) CreateServer(ctx context.Context, name, address, remark, xrayMode, actor string) (CreatedServer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CreatedServer{}, errors.New("服务器名称不能为空")
	}
	switch xrayMode {
	case "", storage.XrayModeEmbedded, storage.XrayModeExternal, storage.XrayModeNone:
	default:
		return CreatedServer{}, fmt.Errorf("xray_mode 非法: %q", xrayMode)
	}

	srv, err := r.store.CreateServer(ctx, name, address, remark, xrayMode)
	if err != nil {
		return CreatedServer{}, err
	}

	plain, _, exp, err := r.IssueEnrollToken(ctx, srv.ID, actor)
	if err != nil {
		return CreatedServer{}, err
	}
	r.store.Audit(ctx, storage.AuditServerCreate, actor, srv.ID,
		fmt.Sprintf("name=%s address=%s xray_mode=%s", name, address, srv.XrayMode), "")
	return CreatedServer{Server: srv, EnrollToken: plain, ExpiresAt: exp}, nil
}

// IssueEnrollToken 为已存在的服务器签发一次性入驻令牌，并吊销旧的同类型令牌。
func (r *Registry) IssueEnrollToken(ctx context.Context, serverID, actor string) (plain string, tokenID string, expiresAt int64, err error) {
	if _, err := r.store.GetServer(ctx, serverID); err != nil {
		return "", "", 0, err
	}
	if err := r.store.RevokeTokens(ctx, serverID, storage.TokenKindEnroll); err != nil {
		return "", "", 0, err
	}
	plain, err = auth.GenerateToken()
	if err != nil {
		return "", "", 0, err
	}
	t, err := r.store.CreateToken(ctx, serverID, storage.TokenKindEnroll,
		auth.HashToken(plain), EnrollTokenTTL, actor)
	if err != nil {
		return "", "", 0, err
	}
	return plain, t.ID, t.ExpiresAt, nil
}

// IssueAgentToken 签发长期 agent 令牌（用于 enroll 兑换或手动轮换）。
func (r *Registry) IssueAgentToken(ctx context.Context, serverID, actor string) (string, storage.AgentToken, error) {
	plain, err := auth.GenerateToken()
	if err != nil {
		return "", storage.AgentToken{}, err
	}
	t, err := r.store.CreateToken(ctx, serverID, storage.TokenKindAgent,
		auth.HashToken(plain), 0, actor)
	if err != nil {
		return "", storage.AgentToken{}, err
	}
	return plain, t, nil
}

// ConsumeEnrollToken 校验并消费一次性入驻令牌，返回绑定的 server_id。
func (r *Registry) ConsumeEnrollToken(ctx context.Context, plain, ip string) (storage.AgentToken, error) {
	hash := auth.HashToken(plain)
	t, err := r.store.FindTokenByHash(ctx, hash)
	if err != nil {
		return storage.AgentToken{}, err
	}
	if t.Kind != storage.TokenKindEnroll {
		return storage.AgentToken{}, errors.New("该令牌不是入驻令牌")
	}
	if t.RevokedAt != 0 {
		return storage.AgentToken{}, errors.New("入驻令牌已被吊销")
	}
	if t.ExpiresAt != 0 && time.Now().UnixMilli() > t.ExpiresAt {
		return storage.AgentToken{}, errors.New("入驻令牌已过期")
	}
	if t.UsedAt != 0 {
		return storage.AgentToken{}, errors.New("入驻令牌已被使用（一次性）")
	}
	if err := r.store.MarkTokenUsed(ctx, t.ID); err != nil {
		return storage.AgentToken{}, err
	}
	r.store.Audit(ctx, storage.AuditServerEnroll, "agent", t.ServerID, "enroll_token 已消费", ip)
	return t, nil
}

// AuthenticateAgentToken 校验长期 agent 令牌，返回所属服务器与令牌记录。
func (r *Registry) AuthenticateAgentToken(ctx context.Context, plain string) (storage.Server, storage.AgentToken, error) {
	if plain == "" {
		return storage.Server{}, storage.AgentToken{}, agentproto.ErrUnauthorized
	}
	t, err := r.store.FindTokenByHash(ctx, auth.HashToken(plain))
	if err != nil {
		return storage.Server{}, storage.AgentToken{}, err
	}
	if t.Kind != storage.TokenKindAgent {
		return storage.Server{}, storage.AgentToken{}, errors.New("该令牌不是 agent 令牌")
	}
	if t.Expired(time.Now()) {
		return storage.Server{}, storage.AgentToken{}, errors.New("agent 令牌已失效")
	}
	srv, err := r.store.GetServer(ctx, t.ServerID)
	if err != nil {
		return storage.Server{}, storage.AgentToken{}, err
	}
	return srv, t, nil
}

// ---------------------------------------------------------------------------
// 期望状态
// ---------------------------------------------------------------------------

// HelloAck 组装给 Agent 的握手应答。
func (r *Registry) HelloAck(srv storage.Server) agentproto.HelloAckPayload {
	return agentproto.HelloAckPayload{
		ServerID:             srv.ID,
		ServerName:           srv.Name,
		HeartbeatIntervalSec: int(r.heartbeat.Seconds()),
		MetricsIntervalSec:   int(r.metrics.Seconds()),
		MasterPublicKey:      agentproto.EncodePublicKey(r.publicKey),
		Features: map[string]bool{
			"metrics":       true,
			"xray_control":  true,
			"nginx_install": true,
			"acme_deploy":   true,
			"probe":         true,
		},
		ServerTime: time.Now().UnixMilli(),
	}
}
