package agentlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// 连接参数。
const (
	writeWait      = 10 * time.Second
	pongWait       = 90 * time.Second
	pingPeriod     = 30 * time.Second
	maxMessageSize = 1 << 20 // 1 MiB
	sendBufferSize = 32
)

// TokenFromRequest 依次从 Authorization 头、X-MMX-Token 头、token 查询参数取令牌。
func TokenFromRequest(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if after, ok := strings.CutPrefix(v, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-MMX-Token"); v != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// ClientIP 提取客户端 IP（只信任直连地址，反代场景由上层决定是否覆盖）。
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // Agent 不是浏览器，无同源概念
}

// HandleWS 处理 `/api/agent/ws`。
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	plain := TokenFromRequest(r)
	if plain == "" {
		writeWSError(w, http.StatusUnauthorized, agentproto.CodeUnauthorized, "缺少 agent token")
		return
	}

	srv, tok, err := h.reg.AuthenticateAgentToken(r.Context(), plain)
	if err != nil {
		writeWSError(w, http.StatusUnauthorized, agentproto.CodeUnauthorized, err.Error())
		return
	}

	ip := ClientIP(r)
	if !h.registerPrecheck(tok.ID, ip, srv.ID) {
		writeWSError(w, http.StatusConflict, agentproto.CodeTokenReuse,
			"agent_token 被不同 IP 复用，请为每台服务器使用独立令牌")
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Debug("websocket 升级失败", "server", srv.ID, "err", err)
		return
	}

	c := &conn{
		hub:     h,
		server:  srv,
		tokenID: tok.ID,
		ip:      ip,
		send:    make(chan agentproto.Envelope, sendBufferSize),
		done:    make(chan struct{}),
	}
	if err := h.register(c); err != nil {
		_ = ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, err.Error()),
			time.Now().Add(writeWait))
		_ = ws.Close()
		return
	}
	defer h.unregister(c)

	h.log.Info("agent 已连接", "server", srv.ID, "name", srv.Name, "ip", ip)
	if h.OnConnect != nil {
		h.OnConnect(srv.ID)
	}

	go c.writeLoop(ws)
	c.readLoop(ws)

	h.log.Info("agent 已断开", "server", srv.ID, "ip", ip)
}

// registerPrecheck 只做 IP 独占的预检（在 Upgrade 之前，便于返回 HTTP 409）。
func (h *Hub) registerPrecheck(tokenID, ip, serverID string) bool {
	h.mu.RLock()
	lk, ok := h.ipLocks[tokenID]
	h.mu.RUnlock()
	if !ok {
		return true
	}
	if time.Now().Before(lk.until) && lk.ip != ip {
		h.store.Audit(context.Background(), storage.AuditServerTokenReuse, "agent", serverID,
			fmt.Sprintf("token=%s 首个 IP=%s 当前 IP=%s", tokenID, lk.ip, ip), ip)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 读循环
// ---------------------------------------------------------------------------

func (c *conn) readLoop(ws *websocket.Conn) {
	defer ws.Close()
	ws.SetReadLimit(maxMessageSize)
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		var env agentproto.Envelope
		if err := ws.ReadJSON(&env); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.hub.log.Debug("读取失败", "server", c.server.ID, "err", err)
			}
			return
		}
		if err := env.Validate(); err != nil {
			c.sendError(agentproto.CodeUnsupportedProtocol, err.Error())
			return
		}
		c.handle(env)
	}
}

func (c *conn) handle(env agentproto.Envelope) {
	h := c.hub
	ctx := context.Background()

	switch env.Type {
	case agentproto.TypeHello:
		var p agentproto.HelloPayload
		if err := env.Decode(&p); err != nil {
			c.sendError(agentproto.CodeBadPayload, "hello 载荷解析失败")
			return
		}
		if p.ProtocolVersion != agentproto.Version {
			c.sendError(agentproto.CodeUnsupportedProtocol,
				fmt.Sprintf("agent 协议版本 %d，主控为 %d", p.ProtocolVersion, agentproto.Version))
			return
		}
		ack := h.reg.HelloAck(c.server)
		reply, err := agentproto.NewEnvelope(agentproto.TypeHelloAck, ack)
		if err == nil {
			c.trySend(reply)
		}
		_ = h.store.MarkServerSeen(ctx, c.server.ID, c.ip, p.AgentVersion,
			p.ProtocolVersion, "")
		h.log.Info("agent 握手完成",
			"server", c.server.ID, "host", p.Hostname, "os", p.OS,
			"agent", p.AgentVersion, "xray_mode", p.XrayMode)

	case agentproto.TypeHeartbeat:
		var p agentproto.HeartbeatPayload
		if err := env.Decode(&p); err != nil {
			c.sendError(agentproto.CodeBadPayload, "heartbeat 载荷解析失败")
			return
		}
		_ = h.store.MarkServerSeen(ctx, c.server.ID, c.ip, "", 0, "")
		if reply, err := agentproto.NewEnvelope(agentproto.TypeHeartbeatAck, struct{}{}); err == nil {
			c.trySend(reply)
		}

	case agentproto.TypeMetrics:
		var p agentproto.MetricsPayload
		if err := env.Decode(&p); err != nil {
			c.sendError(agentproto.CodeBadPayload, "metrics 载荷解析失败")
			return
		}
		if p.TS == 0 {
			p.TS = time.Now().UnixMilli()
		}
		m := storage.ServerMetrics{
			ServerID: c.server.ID, TS: p.TS,
			CPUPercent: p.CPUPercent, MemUsed: p.MemUsed, MemTotal: p.MemTotal,
			DiskUsed: p.DiskUsed, DiskTotal: p.DiskTotal,
			Load1: p.Load1, Load5: p.Load5, Load15: p.Load15,
			NetUp: p.NetUp, NetDown: p.NetDown,
			TCPConns: p.TCPConns, UDPConns: p.UDPConns,
			Processes: p.Processes, UptimeSec: p.UptimeSec, XrayRunning: p.XrayRunning,
		}
		if p.Traffic != nil {
			m.TrafficUp, m.TrafficDown = p.Traffic.Up, p.Traffic.Down
		}
		if err := h.store.InsertMetrics(ctx, m); err != nil {
			h.log.Warn("指标落库失败", "server", c.server.ID, "err", err)
		}
		_ = h.store.MarkServerSeen(ctx, c.server.ID, c.ip, "", 0, "")
		if h.OnMetrics != nil {
			h.OnMetrics(c.server.ID, p)
		}

	case agentproto.TypeTaskResult:
		var p agentproto.TaskResultPayload
		if err := env.Decode(&p); err != nil {
			c.sendError(agentproto.CodeBadPayload, "task_result 载荷解析失败")
			return
		}
		h.deliverTaskResult(p)
		h.log.Info("任务完成", "server", c.server.ID, "task", p.TaskID,
			"status", p.Status, "err", p.Error)

	case agentproto.TypeAck:
		// 无需处理

	case agentproto.TypeError:
		var p agentproto.ErrorPayload
		_ = env.Decode(&p)
		h.log.Warn("agent 报错", "server", c.server.ID, "code", p.Code, "msg", p.Message)

	default:
		h.log.Debug("忽略未知消息类型", "type", env.Type)
	}
}

func (c *conn) sendError(code, msg string) {
	env, err := agentproto.NewEnvelope(agentproto.TypeError, agentproto.ErrorPayload{Code: code, Message: msg})
	if err != nil {
		return
	}
	c.trySend(env)
}

// ---------------------------------------------------------------------------
// 写循环
// ---------------------------------------------------------------------------

func (c *conn) writeLoop(ws *websocket.Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = ws.Close()
	}()
	for {
		select {
		case env, ok := <-c.send:
			if !ok {
				return
			}
			_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := ws.WriteJSON(env); err != nil {
				return
			}
		case <-ticker.C:
			_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP 辅助
// ---------------------------------------------------------------------------

func writeWSError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"code":%q,"message":%q}`, code, msg)))
}

// ErrTokenReuseCheck 供 handler 层复用的错误判断。
func ErrTokenReuseCheck(err error) bool { return errors.Is(err, ErrTokenReuse) }
