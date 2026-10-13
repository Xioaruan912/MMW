package agentlink

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// 连接与派发相关的错误。
var (
	ErrServerOffline = errors.New("服务器不在线")
	ErrTokenReuse    = errors.New("agent_token 被不同 IP 复用")
	ErrTaskTimeout   = errors.New("任务等待超时")
)

// Hub 维护所有活跃的 Agent 连接，并负责任务派发。
//
// 并发模型：
//   - 每个连接一个 readLoop + 一个 writeLoop
//   - conns / ipLocks / pending 三张表由 mu 保护
//   - 向连接写数据一律走带缓冲的 send channel，绝不直接写 websocket.Conn
type Hub struct {
	log        *slog.Logger
	store      *storage.Store
	reg        *Registry
	priv       ed25519.PrivateKey
	lock       time.Duration // token 换 IP 的锁定窗口
	trustProxy bool          // 是否采信 X-Forwarded-For / X-Real-IP

	mu      sync.RWMutex
	conns   map[string]*conn                             // serverID -> 连接
	ipLocks map[string]ipLock                            // tokenID -> 首个 IP 及其锁定期
	pending map[string]chan agentproto.TaskResultPayload // taskID -> 结果通道

	// 钩子（可选）：连接建立/断开时回调，供指标缓存或通知模块使用
	OnConnect    func(serverID string)
	OnDisconnect func(serverID string)
	OnMetrics    func(serverID string, m agentproto.MetricsPayload)
}

type ipLock struct {
	ip    string
	until time.Time
}

// conn 是一条活跃的 Agent 连接。
type conn struct {
	hub     *Hub
	server  storage.Server
	tokenID string
	ip      string
	send    chan agentproto.Envelope
	done    chan struct{}
	once    sync.Once
}

func (c *conn) close() {
	c.once.Do(func() { close(c.done) })
}

// trySend 非阻塞投递；连接已关闭或缓冲满时返回 false。
func (c *conn) trySend(env agentproto.Envelope) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- env:
		return true
	case <-c.done:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
}

// NewHub 构造 Hub。priv 是主控 Ed25519 私钥，用于给任务签名。
func NewHub(log *slog.Logger, store *storage.Store, reg *Registry, priv ed25519.PrivateKey, tokenReuseLock time.Duration, trustProxy bool) *Hub {
	return &Hub{
		log:        log,
		store:      store,
		reg:        reg,
		priv:       priv,
		lock:       tokenReuseLock,
		trustProxy: trustProxy,
		conns:      make(map[string]*conn),
		ipLocks:    make(map[string]ipLock),
		pending:    make(map[string]chan agentproto.TaskResultPayload),
	}
}

// ---------------------------------------------------------------------------
// 连接注册
// ---------------------------------------------------------------------------

// register 登记一条新连接。若同 token 已被其它 IP 占用且在锁定期内，返回 ErrTokenReuse。
func (h *Hub) register(c *conn) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	if lk, ok := h.ipLocks[c.tokenID]; ok {
		if now.Before(lk.until) && lk.ip != c.ip {
			h.store.Audit(context.Background(), storage.AuditServerTokenReuse, "agent", c.server.ID,
				fmt.Sprintf("token=%s 首个 IP=%s 当前 IP=%s 锁定至 %s", c.tokenID, lk.ip, c.ip,
					lk.until.Format(time.RFC3339)), c.ip)
			return fmt.Errorf("%w（首个 IP %s 锁定至 %s）", ErrTokenReuse, lk.ip, lk.until.Format(time.RFC3339))
		}
	}
	h.ipLocks[c.tokenID] = ipLock{ip: c.ip, until: now.Add(h.lock)}

	// 顶掉旧的同服务器连接（正常重连场景）
	if old, ok := h.conns[c.server.ID]; ok && old != c {
		h.log.Info("同一服务器建立新连接，关闭旧连接", "server", c.server.ID)
		old.close()
	}
	h.conns[c.server.ID] = c
	return nil
}

func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	if cur, ok := h.conns[c.server.ID]; ok && cur == c {
		delete(h.conns, c.server.ID)
	}
	h.mu.Unlock()
	c.close()
	if h.OnDisconnect != nil {
		h.OnDisconnect(c.server.ID)
	}
}

// IsOnline 报告某台服务器当前是否有活跃 WebSocket 连接。
func (h *Hub) IsOnline(serverID string) bool {
	h.mu.RLock()
	_, ok := h.conns[serverID]
	h.mu.RUnlock()
	return ok
}

// OnlineServers 返回当前在线的服务器 ID 列表。
func (h *Hub) OnlineServers() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	return out
}

// Count 返回活跃连接数。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// ---------------------------------------------------------------------------
// 发送
// ---------------------------------------------------------------------------

func (h *Hub) lookup(serverID string) (*conn, bool) {
	h.mu.RLock()
	c, ok := h.conns[serverID]
	h.mu.RUnlock()
	return c, ok
}

// Send 向指定服务器投递一个信封。
func (h *Hub) Send(serverID string, env agentproto.Envelope) error {
	c, ok := h.lookup(serverID)
	if !ok {
		return ErrServerOffline
	}
	if !c.trySend(env) {
		return ErrServerOffline
	}
	return nil
}

// PushConfig 下发期望配置。
func (h *Hub) PushConfig(serverID string, cfg agentproto.ConfigPayload) error {
	env, err := agentproto.NewEnvelope(agentproto.TypeConfig, cfg)
	if err != nil {
		return err
	}
	return h.Send(serverID, env)
}

// ---------------------------------------------------------------------------
// 任务派发
// ---------------------------------------------------------------------------

// DispatchTask 签名并下发一个任务，阻塞等待结果（或超时）。
//
// 任务在派发前先落库（pending_tasks），因此：
//   - 主控重启后可以恢复/超时结算，而不是静默丢失
//   - Pull（HTTP 降级）通道可以从同一个队列取任务，与 WS 模式能力对齐
//   - 有完整的执行历史可查
func (h *Hub) DispatchTask(ctx context.Context, serverID, kind string, args agentproto.RawArgs, actor string) (agentproto.TaskResultPayload, error) {
	if !agentproto.KnownTask(kind) {
		return agentproto.TaskResultPayload{}, fmt.Errorf("任务 %q 不在白名单内", kind)
	}
	if err := agentproto.ValidateTask(kind, args); err != nil {
		return agentproto.TaskResultPayload{}, err
	}
	if args == nil {
		args = agentproto.RawArgs{}
	}
	nonce, err := agentproto.NewNonce()
	if err != nil {
		return agentproto.TaskResultPayload{}, err
	}
	timeout := agentproto.TaskTimeout(kind)
	task := &agentproto.TaskPayload{
		TaskID:     idgen.New(idgen.PrefixTask),
		Kind:       kind,
		Nonce:      nonce,
		DeadlineMS: timeout.Milliseconds(),
		Args:       args,
	}
	if err := agentproto.SignTask(h.priv, task); err != nil {
		return agentproto.TaskResultPayload{}, err
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return agentproto.TaskResultPayload{}, err
	}
	rec := storage.PendingTask{
		ID: task.TaskID, ServerID: serverID, Kind: kind, Args: string(argsJSON),
		Nonce: nonce, Signature: task.Signature, DeadlineMS: task.DeadlineMS, Actor: actor,
	}
	if err := h.store.CreateTask(ctx, rec); err != nil {
		return agentproto.TaskResultPayload{}, fmt.Errorf("任务落库失败: %w", err)
	}

	// 无论成功失败都要结算任务记录，避免留下永久的 pending
	finish := func(status, output, errMsg string) {
		_ = h.store.FinishTask(context.Background(), task.TaskID, status, output, errMsg)
	}

	c, online := h.lookup(serverID)
	if !online {
		// 服务器离线：任务保留在队列里，等 Agent 用 Pull 通道取走或重连后由 sweeper 超时结算
		h.store.Audit(ctx, storage.AuditTaskDispatch, actor, serverID,
			fmt.Sprintf("kind=%s task=%s (服务器离线，已入队)", kind, task.TaskID), "")
		return agentproto.TaskResultPayload{}, ErrServerOffline
	}

	ch := make(chan agentproto.TaskResultPayload, 1)
	h.mu.Lock()
	h.pending[task.TaskID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, task.TaskID)
		h.mu.Unlock()
	}()

	env, err := agentproto.NewEnvelope(agentproto.TypeTask, task)
	if err != nil {
		finish(storage.TaskFailed, "", err.Error())
		return agentproto.TaskResultPayload{}, err
	}
	if !c.trySend(env) {
		finish(storage.TaskFailed, "", ErrServerOffline.Error())
		return agentproto.TaskResultPayload{}, ErrServerOffline
	}
	_ = h.store.MarkTaskDispatched(ctx, task.TaskID)
	h.store.Audit(ctx, storage.AuditTaskDispatch, actor, serverID,
		fmt.Sprintf("kind=%s task=%s", kind, task.TaskID), "")

	wait := timeout + 20*time.Second
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(wait):
		finish(storage.TaskTimeout, "", "主控侧等待回执超时")
		return agentproto.TaskResultPayload{}, fmt.Errorf("%w（task=%s kind=%s）", ErrTaskTimeout, task.TaskID, kind)
	case <-ctx.Done():
		finish(storage.TaskCancelled, "", "调用方取消")
		return agentproto.TaskResultPayload{}, ctx.Err()
	}
}

// DeliverResult 供外部（Pull 通道）把任务结果交给可能仍在等待的同步调用方。
func (h *Hub) DeliverResult(res agentproto.TaskResultPayload) { h.deliverTaskResult(res) }

// deliverTaskResult 由读循环调用，把结果交给等待者并结算任务记录。
func (h *Hub) deliverTaskResult(res agentproto.TaskResultPayload) {
	status := storage.TaskFailed
	switch res.Status {
	case agentproto.TaskStatusOK:
		status = storage.TaskOK
	case agentproto.TaskStatusRejected:
		status = storage.TaskCancelled
	}
	_ = h.store.FinishTask(context.Background(), res.TaskID, status, res.Output, res.Error)

	h.mu.RLock()
	ch, ok := h.pending[res.TaskID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

// ---------------------------------------------------------------------------
// 广播
// ---------------------------------------------------------------------------

// Broadcast 给全部在线服务器投递同一信封，返回成功数。
func (h *Hub) Broadcast(env agentproto.Envelope) int {
	h.mu.RLock()
	targets := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	n := 0
	for _, c := range targets {
		if c.trySend(env) {
			n++
		}
	}
	return n
}
