// Package agentproto 定义主控（master）与远程 Agent 之间的线协议。
//
// 这是双端**共用**的包：master 与 agent 都依赖它，因此协议不可能漂移。
// 规格说明见 docs/AGENT-PROTOCOL.md —— 两者必须同步修改。
package agentproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// Version 是当前协议版本。破坏性变更时必须 +1。
const Version = 1

// 消息类型。
const (
	TypeHello        = "hello"
	TypeHelloAck     = "hello_ack"
	TypeHeartbeat    = "heartbeat"
	TypeHeartbeatAck = "heartbeat_ack"
	TypeMetrics      = "metrics"
	TypeTask         = "task"
	TypeTaskResult   = "task_result"
	TypeConfig       = "config"
	TypeAck          = "ack"
	TypeError        = "error"
)

// Envelope 是所有消息的统一信封。
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	V       int             `json:"v"`
	SentAt  int64           `json:"sent_at"`
	Nonce   string          `json:"nonce,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// NewEnvelope 构造一个信封并序列化 payload。
func NewEnvelope(typ string, payload any) (Envelope, error) {
	e := Envelope{
		ID:     NewID(),
		Type:   typ,
		V:      Version,
		SentAt: time.Now().UnixMilli(),
	}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, fmt.Errorf("序列化 %s 载荷失败: %w", typ, err)
		}
		e.Payload = b
	}
	return e, nil
}

// MustEnvelope 与 NewEnvelope 相同，但序列化失败会 panic。
// 仅用于编译期已知可序列化的静态载荷。
func MustEnvelope(typ string, payload any) Envelope {
	e, err := NewEnvelope(typ, payload)
	if err != nil {
		panic(err)
	}
	return e
}

// Decode 把载荷解码到 dst。
func (e Envelope) Decode(dst any) error {
	if len(e.Payload) == 0 {
		return errors.New("信封没有载荷")
	}
	return json.Unmarshal(e.Payload, dst)
}

// Validate 做信封级校验（不含业务语义）。
func (e Envelope) Validate() error {
	if e.ID == "" {
		return errors.New("信封缺少 id")
	}
	if e.Type == "" {
		return errors.New("信封缺少 type")
	}
	if e.V != Version {
		return fmt.Errorf("不支持的协议版本 %d（本端为 %d）", e.V, Version)
	}
	return nil
}

// NewID 生成一个 ULID 字符串，用作幂等键。
func NewID() string {
	id, err := ulid.New(ulid.Timestamp(time.Now()), ulid.DefaultEntropy())
	if err != nil {
		// 熵源异常时退化为纳秒时间戳，保证不 panic
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id.String()
}

// ErrorPayload 是 type=error 的载荷。
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// 协议级错误码（用于 ErrorPayload.Code 与 HTTP 响应体）。
const (
	CodeUnsupportedProtocol = "unsupported_protocol"
	CodeUnauthorized        = "unauthorized"
	CodeTokenExpired        = "token_expired"
	CodeTokenUsed           = "token_used"
	CodeTokenReuse          = "agent_token_reuse"
	CodeServerNotFound      = "server_not_found"
	CodeBadPayload          = "bad_payload"
	CodeTaskRejected        = "task_rejected"
	CodeSignatureInvalid    = "signature_invalid"
)

// 协议级错误哨兵（用于 errors.Is 判断）。
var (
	ErrUnauthorized   = errors.New("未授权")
	ErrTokenReuse     = errors.New("agent_token 被不同 IP 复用")
	ErrServerNotFound = errors.New("服务器不存在")
)
