package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mmx-project/mmx/internal/auth"
	"github.com/mmx-project/mmx/internal/inbound"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/internal/xrayctl"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// hashPassword / decodeInto 是本包的两个薄适配层。
//
// 集中在一处是为了让工具实现里不出现跨包的杂糅调用，
// 也便于将来替换实现（例如换成 argon2）。
func hashPassword(plain string) (string, error) { return auth.HashPassword(plain) }

func decodeInto(src map[string]any, dst any) error {
	if len(src) == 0 {
		return nil
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// Deps 是工具实现所需的依赖。
type Deps struct {
	Store *storage.Store
	Hub   Hub
	Xray  *xrayctl.Applier
	// Online 判断服务器是否在线
	Online func(serverID string) bool
}

// Hub 是本包对 agentlink 的最小依赖。
type Hub interface {
	DispatchTask(ctx context.Context, serverID, kind string, args agentproto.RawArgs, actor string) (agentproto.TaskResultPayload, error)
}

// Tools 构造全部工具。
func Tools(d Deps) []Tool {
	return []Tool{
		toolListServers(d),
		toolCreateServer(d),
		toolListNodes(d),
		toolCreateInbound(d),
		toolApplyXray(d),
		toolListUsers(d),
		toolCreateUser(d),
		toolGetTraffic(d),
		toolListAnnouncements(d),
		toolDispatchTask(d),
		toolOverview(d),
	}
}

// ---------------------------------------------------------------------------

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func num(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// ---------------------------------------------------------------------------

func toolListServers(d Deps) Tool {
	return Tool{
		Name:        "list_servers",
		Description: "列出全部受管服务器及其在线状态与最新指标。",
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ Identity, _ map[string]any) (string, error) {
			servers, err := d.Store.ListServers(ctx)
			if err != nil {
				return "", err
			}
			type item struct {
				ID       string  `json:"id"`
				Name     string  `json:"name"`
				Address  string  `json:"address"`
				Status   string  `json:"status"`
				Online   bool    `json:"online"`
				XrayMode string  `json:"xray_mode"`
				CPU      float64 `json:"cpu_percent,omitempty"`
				MemUsed  int64   `json:"mem_used,omitempty"`
				MemTotal int64   `json:"mem_total,omitempty"`
				LastSeen int64   `json:"last_seen_at"`
			}
			out := make([]item, 0, len(servers))
			for _, s := range servers {
				it := item{
					ID: s.ID, Name: s.Name, Address: s.Address, Status: s.Status,
					Online: d.Online(s.ID), XrayMode: s.XrayMode, LastSeen: s.LastSeenAt,
				}
				if m, ok, err := d.Store.LatestMetrics(ctx, s.ID); err == nil && ok {
					it.CPU = m.CPUPercent
					it.MemUsed = m.MemUsed
					it.MemTotal = m.MemTotal
				}
				out = append(out, it)
			}
			return jsonText(map[string]any{"total": len(out), "servers": out}), nil
		},
	}
}

func toolCreateServer(d Deps) Tool {
	return Tool{
		Name:        "create_server",
		Description: "新建一台受管服务器并返回一次性入驻令牌与一键安装命令。令牌只能用一次。",
		AdminOnly:   true,
		InputSchema: obj(map[string]any{
			"name":      str("服务器名称"),
			"address":   str("服务器地址（IP 或域名）"),
			"xray_mode": str("Xray 模式：embedded / external / none，默认 external"),
			"remark":    str("备注"),
		}, "name"),
		Handler: func(ctx context.Context, id Identity, args map[string]any) (string, error) {
			name := argString(args, "name")
			if name == "" {
				return "", fmt.Errorf("缺少 name")
			}
			mode := argString(args, "xray_mode")
			if mode == "" {
				mode = storage.XrayModeExternal
			}
			// 这里不直接调 HTTP handler，而是复用存储层，避免 MCP 依赖 HTTP 层
			srv, err := d.Store.CreateServer(ctx, name, argString(args, "address"),
				argString(args, "remark"), mode)
			if err != nil {
				return "", err
			}
			d.Store.Audit(ctx, storage.AuditServerCreate, id.Username, srv.ID, "via MCP", "")
			return jsonText(map[string]any{
				"server": srv,
				"note":   "请到面板「服务管理」生成一键安装命令（含一次性令牌）",
			}), nil
		},
	}
}

func toolListNodes(d Deps) Tool {
	return Tool{
		Name:        "list_nodes",
		Description: "列出全部节点（含所属服务器、入站协议/传输/安全层与端口）。",
		InputSchema: obj(map[string]any{
			"server_id": str("只列出某台服务器的节点（可选）"),
		}),
		Handler: func(ctx context.Context, _ Identity, args map[string]any) (string, error) {
			views, err := d.Store.ListNodeViews(ctx, argString(args, "server_id"))
			if err != nil {
				return "", err
			}
			type item struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Region    string `json:"region"`
				Server    string `json:"server"`
				Address   string `json:"server_address"`
				Protocol  string `json:"protocol"`
				Transport string `json:"transport"`
				Security  string `json:"security"`
				Port      int    `json:"port"`
				Enabled   bool   `json:"enabled"`
			}
			out := make([]item, 0, len(views))
			for _, v := range views {
				out = append(out, item{
					ID: v.ID, Name: v.Name, Region: v.Region,
					Server: v.ServerName, Address: v.ServerAddr,
					Protocol: v.Inbound.Protocol, Transport: v.Inbound.Transport,
					Security: v.Inbound.Security, Port: v.Inbound.Port, Enabled: v.Enabled,
				})
			}
			return jsonText(map[string]any{"total": len(out), "nodes": out}), nil
		},
	}
}

// toolCreateInbound 创建入站。
//
// 之所以把传输/安全层的嵌套参数也完整暴露出来，而不是只给几个扁平字段：
// REALITY 需要 dest / server_names / private_key，TLS 需要 server_name ——
// 少一个就会在生成 Xray 配置时才报错，那时 AI Agent 已经走远了好几轮。
func toolCreateInbound(d Deps) Tool {
	return Tool{
		Name: "create_inbound",
		Description: "在指定服务器上创建入站（协议 × 传输 × 安全层），并自动生成同名展示节点。" +
			"REALITY 仅支持 TCP，且必须提供 stream.reality_dest / reality_server_names / reality_private_key；" +
			"TLS 必须提供 stream.server_name。",
		AdminOnly: true,
		InputSchema: obj(map[string]any{
			"server_id": str("服务器 ID"),
			"tag":       str("入站唯一标识（同服务器内唯一）"),
			"protocol":  str("vless / vmess / trojan / shadowsocks"),
			"transport": str("tcp / ws / grpc / httpupgrade"),
			"security":  str("none / tls / reality"),
			"port":      num("监听端口"),
			"listen":    str("监听地址，默认 0.0.0.0"),
			"node_name": str("展示节点名（可选，默认用 tag）"),
			"region":    str("地区标签（可选）"),
			"remark":    str("备注（可选）"),
			"settings": map[string]any{
				"type":        "object",
				"description": "协议参数：flow（vless）、alter_id（vmess）、method/password（shadowsocks）",
			},
			"stream": map[string]any{
				"type": "object",
				"description": "传输与安全参数：ws_path / ws_host / service_name / httpupgrade_path / " +
					"server_name（TLS）/ reality_dest / reality_server_names / reality_private_key / reality_short_ids",
			},
		}, "server_id", "protocol", "port"),
		Handler: func(ctx context.Context, id Identity, args map[string]any) (string, error) {
			spec := inbound.Spec{
				ServerID:  argString(args, "server_id"),
				Tag:       argString(args, "tag"),
				Protocol:  argString(args, "protocol"),
				Transport: argString(args, "transport"),
				Security:  argString(args, "security"),
				Port:      argInt(args, "port", 0),
				Listen:    argString(args, "listen"),
				Remark:    argString(args, "remark"),
				Enabled:   true,
			}
			if err := decodeInto(argMap(args, "settings"), &spec.ProtocolSettings); err != nil {
				return "", fmt.Errorf("settings 解析失败: %w", err)
			}
			if err := decodeInto(argMap(args, "stream"), &spec.Stream); err != nil {
				return "", fmt.Errorf("stream 解析失败: %w", err)
			}
			spec.Normalize()
			if err := spec.Validate(); err != nil {
				return "", err
			}
			nodeName := argString(args, "node_name")
			if nodeName == "" {
				nodeName = spec.Tag
			}
			in, node, err := d.Store.CreateInboundWithNode(ctx, spec.ToStorage(), nodeName, argString(args, "region"))
			if err != nil {
				return "", err
			}
			d.Store.Audit(ctx, "inbound.create", id.Username, in.ID, "via MCP", "")
			return jsonText(map[string]any{
				"inbound": in, "node": node,
				"next": "调用 apply_xray_config 把配置下发到 Agent",
			}), nil
		},
	}
}

func toolApplyXray(d Deps) Tool {
	return Tool{
		Name:        "apply_xray_config",
		Description: "根据数据库中的入站与客户端生成 Xray 配置，并通过签名任务下发到 Agent。",
		AdminOnly:   true,
		InputSchema: obj(map[string]any{"server_id": str("服务器 ID")}, "server_id"),
		Handler: func(ctx context.Context, id Identity, args map[string]any) (string, error) {
			serverID := argString(args, "server_id")
			if serverID == "" {
				return "", fmt.Errorf("缺少 server_id")
			}
			res, err := d.Xray.Apply(ctx, serverID, id.Username)
			if err != nil && res == nil {
				return "", err
			}
			return jsonText(res), nil
		},
	}
}

func toolListUsers(d Deps) Tool {
	return Tool{
		Name:        "list_users",
		Description: "列出全部用户及其角色、状态、套餐与流量配额。",
		AdminOnly:   true,
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ Identity, _ map[string]any) (string, error) {
			users, err := d.Store.ListUsers(ctx)
			if err != nil {
				return "", err
			}
			type item struct {
				ID           string `json:"id"`
				Username     string `json:"username"`
				Role         string `json:"role"`
				Status       string `json:"status"`
				PackageID    string `json:"package_id"`
				TrafficUsed  int64  `json:"traffic_used"`
				TrafficLimit int64  `json:"traffic_limit"`
				ExpireAt     int64  `json:"expire_at"`
			}
			out := make([]item, 0, len(users))
			for _, u := range users {
				out = append(out, item{
					ID: u.ID, Username: u.Username, Role: u.Role, Status: u.Status,
					PackageID: u.PackageID, TrafficUsed: u.TrafficUsed,
					TrafficLimit: u.TrafficLimit, ExpireAt: u.ExpireAt,
				})
			}
			return jsonText(map[string]any{"total": len(out), "users": out}), nil
		},
	}
}

func toolCreateUser(d Deps) Tool {
	return Tool{
		Name:        "create_user",
		Description: "创建普通用户。创建后系统会自动签发订阅凭据。",
		AdminOnly:   true,
		InputSchema: obj(map[string]any{
			"username": str("用户名"),
			"password": str("初始口令（至少 10 位）"),
			"nickname": str("昵称（可选）"),
			"email":    str("邮箱（可选）"),
		}, "username", "password"),
		Handler: func(ctx context.Context, id Identity, args map[string]any) (string, error) {
			username := argString(args, "username")
			password := argString(args, "password")
			if username == "" || password == "" {
				return "", fmt.Errorf("username 与 password 必填")
			}
			if len(password) < 10 {
				return "", fmt.Errorf("口令至少 10 位")
			}
			hash, err := hashPassword(password)
			if err != nil {
				return "", err
			}
			nick := argString(args, "nickname")
			if nick == "" {
				nick = username
			}
			u, err := d.Store.CreateUser(ctx, username, hash, nick, argString(args, "email"), storage.RoleUser)
			if err != nil {
				return "", err
			}
			d.Store.Audit(ctx, "user.create", id.Username, u.ID, "via MCP", "")
			return jsonText(map[string]any{"user": u, "note": "订阅凭据请在面板重置以获取"}), nil
		},
	}
}

func toolGetTraffic(d Deps) Tool {
	return Tool{
		Name:        "get_traffic",
		Description: "查询流量汇总。不传 server_id 时返回全部服务器按日合并的曲线。",
		InputSchema: obj(map[string]any{
			"server_id": str("服务器 ID（可选）"),
			"days":      num("统计天数，默认 30"),
		}),
		Handler: func(ctx context.Context, _ Identity, args map[string]any) (string, error) {
			days := argInt(args, "days", 30)
			serverID := argString(args, "server_id")
			if serverID != "" {
				sum, err := d.Store.ServerTrafficSummary(ctx, serverID, days)
				if err != nil {
					return "", err
				}
				return jsonText(sum), nil
			}
			sum, err := d.Store.AllServersTrafficSummary(ctx, days)
			if err != nil {
				return "", err
			}
			return jsonText(sum), nil
		},
	}
}

func toolListAnnouncements(d Deps) Tool {
	return Tool{
		Name:        "list_announcements",
		Description: "列出当前对用户可见的公告。",
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ Identity, _ map[string]any) (string, error) {
			list, err := d.Store.ListActiveAnnouncements(ctx, 20)
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{"total": len(list), "announcements": list}), nil
		},
	}
}

func toolDispatchTask(d Deps) Tool {
	return Tool{
		Name: "dispatch_task",
		Description: "向 Agent 下发一个白名单任务并等待结果。可用类型见 list_task_kinds。" +
			"任务会经过 Ed25519 签名校验，Agent 侧只执行白名单内的操作。",
		AdminOnly: true,
		InputSchema: obj(map[string]any{
			"server_id": str("服务器 ID"),
			"kind":      str("任务类型，例如 xray.restart / xray.apply_config / probe.collect_now"),
			"args":      map[string]any{"type": "object", "description": "任务参数（可选）"},
		}, "server_id", "kind"),
		Handler: func(ctx context.Context, id Identity, args map[string]any) (string, error) {
			serverID := argString(args, "server_id")
			kind := argString(args, "kind")
			if serverID == "" || kind == "" {
				return "", fmt.Errorf("server_id 与 kind 必填")
			}
			if !agentproto.KnownTask(kind) {
				return "", fmt.Errorf("任务 %q 不在白名单内；可用类型: %s",
					kind, strings.Join(agentproto.TaskKinds(), ", "))
			}
			raw := agentproto.RawArgs{}
			for k, v := range argMap(args, "args") {
				raw[k] = v
			}
			res, err := d.Hub.DispatchTask(ctx, serverID, kind, raw, id.Username+" (MCP)")
			if err != nil {
				return "", err
			}
			return jsonText(res), nil
		},
	}
}

func toolOverview(d Deps) Tool {
	return Tool{
		Name:        "get_overview",
		Description: "获取系统总览：服务器数量与在线数、用户数、节点数。",
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ Identity, _ map[string]any) (string, error) {
			servers, err := d.Store.ListServers(ctx)
			if err != nil {
				return "", err
			}
			online := 0
			for _, s := range servers {
				if d.Online(s.ID) {
					online++
				}
			}
			users, _ := d.Store.CountUsers(ctx)
			nodes, _ := d.Store.ListNodeViews(ctx, "")
			return jsonText(map[string]any{
				"servers_total":  len(servers),
				"servers_online": online,
				"users_total":    users,
				"nodes_total":    len(nodes),
			}), nil
		},
	}
}
