package xrayctl

import (
	"context"
	"errors"
	"fmt"

	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// Hub 是本包对 agentlink 的最小依赖（接口化便于测试）。
type Hub interface {
	IsOnline(serverID string) bool
	PushConfig(serverID string, cfg agentproto.ConfigPayload) error
	DispatchTask(ctx context.Context, serverID, kind string, args agentproto.RawArgs, actor string) (agentproto.TaskResultPayload, error)
}

// Applier 把期望状态构建并下发到指定服务器。
type Applier struct {
	store *storage.Store
	hub   Hub
	opts  Options
}

// NewApplier 构造 Applier。
func NewApplier(store *storage.Store, hub Hub, opts Options) *Applier {
	return &Applier{store: store, hub: hub, opts: opts}
}

// BuildForServer 读取服务器上的全部启用入站与客户端，产出配置与 rev。
func (a *Applier) BuildForServer(ctx context.Context, serverID string) (*BuildResult, error) {
	inbounds, err := a.store.ListInbounds(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("读取入站失败: %w", err)
	}
	clients := make(map[string][]storage.InboundClient, len(inbounds))
	for _, in := range inbounds {
		cs, err := a.store.ListClients(ctx, in.ID)
		if err != nil {
			return nil, fmt.Errorf("读取入站 %s 的客户端失败: %w", in.ID, err)
		}
		clients[in.ID] = cs
	}
	outbounds, err := a.store.ListOutbounds(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("读取自定义出站失败: %w", err)
	}
	rules, err := a.store.ListRoutingRules(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("读取路由规则失败: %w", err)
	}
	return BuildInput(Input{
		Inbounds: inbounds, Clients: clients, Outbounds: outbounds, Rules: rules,
	}, a.opts)
}

// ApplyResult 是一次下发的产物。
type ApplyResult struct {
	ServerID string `json:"server_id"`
	Rev      string `json:"rev"`
	Inbounds int    `json:"inbounds"`
	Status   string `json:"status"`
	Applied  bool   `json:"applied"`
	AgentRev string `json:"agent_rev,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ErrServerOffline 表示服务器当前没有活跃连接。
var ErrServerOffline = errors.New("服务器离线，配置已生成但未下发")

// Apply 构建配置并通过签名任务下发给 Agent（同步等待结果）。
//
// 用任务而不是裸推配置，是为了拿到**可审计、可回执**的结果：
// 任务有 Ed25519 签名、有幂等 ID、有执行输出；配置推送是单向无回执的。
func (a *Applier) Apply(ctx context.Context, serverID, actor string) (*ApplyResult, error) {
	res, err := a.BuildForServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	out := &ApplyResult{
		ServerID: serverID,
		Rev:      res.Rev,
		Inbounds: len(res.Tags),
	}

	if !a.hub.IsOnline(serverID) {
		out.Status = "skipped"
		out.Error = ErrServerOffline.Error()
		return out, ErrServerOffline
	}

	// config 作为任务参数下发；Agent 侧会写入配置文件并热重载。
	cfgMap, err := toMap(res.Config)
	if err != nil {
		return nil, err
	}
	taskRes, err := a.hub.DispatchTask(ctx, serverID, agentproto.TaskXrayApplyConfig,
		agentproto.RawArgs{"config": cfgMap, "rev": res.Rev}, actor)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		return out, err
	}
	out.Status = taskRes.Status
	out.Output = taskRes.Output
	out.Error = taskRes.Error
	out.Applied = taskRes.Status == agentproto.TaskStatusOK
	if taskRes.Result != nil {
		out.AgentRev = taskRes.Result.GetString("rev")
	}
	return out, nil
}

// toMap 把配置转成 map，便于塞进任务参数。
func toMap(cfg *agentproto.XrayConfig) (map[string]any, error) {
	raw, err := Canonical(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := jsonUnmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
