package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mmx-project/mmx/internal/idgen"
)

// Outbound 是服务器级的额外出站定义。
//
// 用途：
//   - 链式代理 / 中继：本机入站的流量先经过另一台服务器再出网
//   - 分流出口：不同域名走不同出口（例如某类站点走家宽出口）
//   - 自定义协议出口：socks / http / shadowsocks / vless 等
type Outbound struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
	// Settings 是协议参数（JSON）
	Settings string `json:"settings"`
	// Stream 是传输与安全参数（JSON），仅出站支持 TLS 时有意义
	Stream string `json:"stream"`
	// ProxyTag 非空时表示这是一个"链式出站"：先走 ProxyTag 指定的出站，
	// 再连到本出站的目标地址。对应 Xray 的 proxySettings.tag。
	ProxyTag string `json:"proxy_tag"`
	// SendThrough 是绑定源地址（多网卡/多 IP 出口时用）
	SendThrough string `json:"send_through"`
	Remark      string `json:"remark"`
	Enabled     bool   `json:"enabled"`
	Sort        int    `json:"sort"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// RoutingRule 是一条路由规则。
type RoutingRule struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	Sort     int    `json:"sort"`
	// Kind 是规则类型：domain | ip | port | inbound | protocol
	Kind string `json:"kind"`
	// Values 是匹配值列表（JSON 数组），例如 ["geosite:cn","domain:example.com"]
	Values string `json:"values"`
	// OutboundTag 是命中后使用的出站 tag（direct / block 或自定义出站）
	OutboundTag string `json:"outbound_tag"`
	Remark      string `json:"remark"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

const outboundCols = `id,server_id,tag,protocol,settings,stream,proxy_tag,send_through,
                      remark,enabled,sort,created_at,updated_at`
const routeRuleCols = `id,server_id,sort,kind,values_json,outbound_tag,remark,enabled,created_at,updated_at`

// ---------------------------------------------------------------------------
// 出站
// ---------------------------------------------------------------------------

// CreateOutbound 新建出站。
func (s *Store) CreateOutbound(ctx context.Context, o Outbound) (Outbound, error) {
	if o.ID == "" {
		o.ID = idgen.New("out")
	}
	if o.Settings == "" {
		o.Settings = "{}"
	}
	if o.Stream == "" {
		o.Stream = "{}"
	}
	now := nowMS()
	o.CreatedAt, o.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO outbounds(`+outboundCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.ServerID, o.Tag, o.Protocol, o.Settings, o.Stream, o.ProxyTag,
		o.SendThrough, o.Remark, b2i(o.Enabled), o.Sort, o.CreatedAt, o.UpdatedAt)
	return o, err
}

// GetOutbound 取出站。
func (s *Store) GetOutbound(ctx context.Context, id string) (Outbound, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+outboundCols+` FROM outbounds WHERE id=?`, id)
	var o Outbound
	var enabled int
	err := row.Scan(&o.ID, &o.ServerID, &o.Tag, &o.Protocol, &o.Settings, &o.Stream,
		&o.ProxyTag, &o.SendThrough, &o.Remark, &enabled, &o.Sort, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, ErrNotFound
	}
	o.Enabled = enabled != 0
	return o, err
}

// ListOutbounds 列出某台服务器的出站。
func (s *Store) ListOutbounds(ctx context.Context, serverID string) ([]Outbound, error) {
	q := `SELECT ` + outboundCols + ` FROM outbounds`
	args := []any{}
	if serverID != "" {
		q += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	q += ` ORDER BY sort ASC, created_at ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outbound
	for rows.Next() {
		var o Outbound
		var enabled int
		if err := rows.Scan(&o.ID, &o.ServerID, &o.Tag, &o.Protocol, &o.Settings, &o.Stream,
			&o.ProxyTag, &o.SendThrough, &o.Remark, &enabled, &o.Sort,
			&o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.Enabled = enabled != 0
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateOutbound 更新出站。
func (s *Store) UpdateOutbound(ctx context.Context, o Outbound) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE outbounds SET tag=?, protocol=?, settings=?, stream=?, proxy_tag=?,
             send_through=?, remark=?, enabled=?, sort=?, updated_at=? WHERE id=?`,
		o.Tag, o.Protocol, o.Settings, o.Stream, o.ProxyTag, o.SendThrough, o.Remark,
		b2i(o.Enabled), o.Sort, nowMS(), o.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOutbound 删除出站。被路由规则引用时应先解除引用。
func (s *Store) DeleteOutbound(ctx context.Context, id string) error {
	o, err := s.GetOutbound(ctx, id)
	if err != nil {
		return err
	}
	// 拒绝删除仍被规则引用的出站：否则配置生成时会出现"指向不存在的出站"，
	// Xray 起不来，而错误发生在 Agent 侧，排障链路很长。
	var refs int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM routing_rules WHERE server_id=? AND outbound_tag=? AND enabled=1`,
		o.ServerID, o.Tag).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return errors.New("该出站仍被路由规则引用，请先修改规则")
	}
	var proxyRefs int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbounds WHERE proxy_tag=? AND enabled=1`, o.Tag).Scan(&proxyRefs); err != nil {
		return err
	}
	if proxyRefs > 0 {
		return errors.New("该出站仍被其它出站作为链式前置引用")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM outbounds WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 路由规则
// ---------------------------------------------------------------------------

// CreateRoutingRule 新建路由规则。
func (s *Store) CreateRoutingRule(ctx context.Context, r RoutingRule) (RoutingRule, error) {
	if r.ID == "" {
		r.ID = idgen.New("rtr")
	}
	if r.Values == "" {
		r.Values = "[]"
	}
	now := nowMS()
	r.CreatedAt, r.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO routing_rules(`+routeRuleCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ServerID, r.Sort, r.Kind, r.Values, r.OutboundTag, r.Remark,
		b2i(r.Enabled), r.CreatedAt, r.UpdatedAt)
	return r, err
}

// ListRoutingRules 列出路由规则。
func (s *Store) ListRoutingRules(ctx context.Context, serverID string) ([]RoutingRule, error) {
	q := `SELECT ` + routeRuleCols + ` FROM routing_rules`
	args := []any{}
	if serverID != "" {
		q += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	q += ` ORDER BY sort ASC, created_at ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoutingRule
	for rows.Next() {
		var r RoutingRule
		var enabled int
		if err := rows.Scan(&r.ID, &r.ServerID, &r.Sort, &r.Kind, &r.Values, &r.OutboundTag,
			&r.Remark, &enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRoutingRule 更新路由规则。
func (s *Store) UpdateRoutingRule(ctx context.Context, r RoutingRule) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE routing_rules SET sort=?, kind=?, values_json=?, outbound_tag=?, remark=?,
             enabled=?, updated_at=? WHERE id=?`,
		r.Sort, r.Kind, r.Values, r.OutboundTag, r.Remark, b2i(r.Enabled), nowMS(), r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRoutingRule 删除路由规则。
func (s *Store) DeleteRoutingRule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM routing_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
