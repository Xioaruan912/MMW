package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mmx-project/mmx/internal/idgen"
)

// Inbound 是一台服务器上的一个监听端口（协议 × 传输 × 安全层）。
type Inbound struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	Tag       string `json:"tag"`
	Protocol  string `json:"protocol"`
	Transport string `json:"transport"`
	Security  string `json:"security"`
	Port      int    `json:"port"`
	Listen    string `json:"listen"`
	Settings  string `json:"settings"`
	Stream    string `json:"stream"`
	Sniffing  string `json:"sniffing"`
	Enabled   bool   `json:"enabled"`
	Remark    string `json:"remark"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// InboundClient 是入站下的一个客户端凭据；一个用户在一个入站上最多一条。
type InboundClient struct {
	ID        string `json:"id"`
	InboundID string `json:"inbound_id"`
	UserID    string `json:"user_id"`
	UUID      string `json:"uuid"`
	Email     string `json:"email"`
	Flow      string `json:"flow"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// Node 是入站的展示层（名字 / 地区 / 标签 / 排序）。
type Node struct {
	ID        string `json:"id"`
	InboundID string `json:"inbound_id"`
	Name      string `json:"name"`
	Region    string `json:"region"`
	Tags      string `json:"tags"`
	Sort      int    `json:"sort"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// NodeView 是订阅生成与前端展示用的合并视图。
type NodeView struct {
	Node
	Inbound    Inbound `json:"inbound"`
	ServerName string  `json:"server_name"`
	ServerAddr string  `json:"server_address"`
}

const inboundCols = `id,server_id,tag,protocol,transport,security,port,listen,
                     settings,stream,sniffing,enabled,remark,created_at,updated_at`
const nodeCols = `id,inbound_id,name,region,tags,sort,enabled,created_at,updated_at`
const clientCols = `id,inbound_id,user_id,uuid,email,flow,enabled,created_at`

func scanInbound(sc interface{ Scan(...any) error }) (Inbound, error) {
	var in Inbound
	var enabled int
	err := sc.Scan(&in.ID, &in.ServerID, &in.Tag, &in.Protocol, &in.Transport, &in.Security,
		&in.Port, &in.Listen, &in.Settings, &in.Stream, &in.Sniffing, &enabled, &in.Remark,
		&in.CreatedAt, &in.UpdatedAt)
	in.Enabled = enabled != 0
	return in, err
}

func scanNode(sc interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var enabled int
	err := sc.Scan(&n.ID, &n.InboundID, &n.Name, &n.Region, &n.Tags, &n.Sort, &enabled,
		&n.CreatedAt, &n.UpdatedAt)
	n.Enabled = enabled != 0
	return n, err
}

func scanClient(sc interface{ Scan(...any) error }) (InboundClient, error) {
	var c InboundClient
	var enabled int
	err := sc.Scan(&c.ID, &c.InboundID, &c.UserID, &c.UUID, &c.Email, &c.Flow, &enabled, &c.CreatedAt)
	c.Enabled = enabled != 0
	return c, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Inbound CRUD
// ---------------------------------------------------------------------------

// CreateInboundWithNode 创建入站，并自动生成同名的展示节点。
func (s *Store) CreateInboundWithNode(ctx context.Context, in Inbound, nodeName, region string) (Inbound, Node, error) {
	if err := s.CheckServerExists(ctx, in.ServerID); err != nil {
		return Inbound{}, Node{}, err
	}
	if in.Listen == "" {
		in.Listen = "0.0.0.0"
	}
	if in.Settings == "" {
		in.Settings = "{}"
	}
	if in.Stream == "" {
		in.Stream = "{}"
	}
	if in.Sniffing == "" {
		in.Sniffing = "{}"
	}
	now := nowMS()
	in.ID = idgen.New(idgen.PrefixInbound)
	in.CreatedAt, in.UpdatedAt = now, now

	node := Node{
		ID: idgen.New(idgen.PrefixNode), InboundID: in.ID, Name: nodeName,
		Region: region, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if node.Name == "" {
		node.Name = in.Tag
	}

	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO inbounds(`+inboundCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.ID, in.ServerID, in.Tag, in.Protocol, in.Transport, in.Security, in.Port,
			in.Listen, in.Settings, in.Stream, in.Sniffing, b2i(in.Enabled), in.Remark,
			in.CreatedAt, in.UpdatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes(`+nodeCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
			node.ID, node.InboundID, node.Name, node.Region, node.Tags, node.Sort,
			b2i(node.Enabled), node.CreatedAt, node.UpdatedAt)
		return err
	})
	if err != nil {
		return Inbound{}, Node{}, err
	}
	return in, node, nil
}

// CheckServerExists 校验服务器存在。
func (s *Store) CheckServerExists(ctx context.Context, serverID string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE id=?`, serverID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// GetInbound 按 ID 取入站。
func (s *Store) GetInbound(ctx context.Context, id string) (Inbound, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+inboundCols+` FROM inbounds WHERE id=?`, id)
	in, err := scanInbound(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Inbound{}, ErrNotFound
	}
	return in, err
}

// ListInbounds 列出某台服务器的入站；serverID 为空则列出全部。
func (s *Store) ListInbounds(ctx context.Context, serverID string) ([]Inbound, error) {
	q := `SELECT ` + inboundCols + ` FROM inbounds`
	args := []any{}
	if serverID != "" {
		q += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	q += ` ORDER BY port ASC, created_at ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Inbound
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// UpdateInbound 更新入站的可变字段。
func (s *Store) UpdateInbound(ctx context.Context, in Inbound) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE inbounds SET tag=?, protocol=?, transport=?, security=?, port=?, listen=?,
             settings=?, stream=?, sniffing=?, enabled=?, remark=?, updated_at=? WHERE id=?`,
		in.Tag, in.Protocol, in.Transport, in.Security, in.Port, in.Listen,
		in.Settings, in.Stream, in.Sniffing, b2i(in.Enabled), in.Remark, nowMS(), in.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInbound 删除入站（节点与客户端级联删除）。
func (s *Store) DeleteInbound(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM inbounds WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Node
// ---------------------------------------------------------------------------

// GetNodeByInbound 取入站对应的展示节点。
func (s *Store) GetNodeByInbound(ctx context.Context, inboundID string) (Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE inbound_id=?`, inboundID)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}

// UpdateNode 更新节点展示信息。
func (s *Store) UpdateNode(ctx context.Context, n Node) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE nodes SET name=?, region=?, tags=?, sort=?, enabled=?, updated_at=? WHERE id=?`,
		n.Name, n.Region, n.Tags, n.Sort, b2i(n.Enabled), nowMS(), n.ID)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

// ListNodeViews 返回节点 + 入站 + 服务器信息的合并视图。
// serverID 为空则返回全部。
func (s *Store) ListNodeViews(ctx context.Context, serverID string) ([]NodeView, error) {
	q := `SELECT n.id,n.inbound_id,n.name,n.region,n.tags,n.sort,n.enabled,n.created_at,n.updated_at,
                 i.id,i.server_id,i.tag,i.protocol,i.transport,i.security,i.port,i.listen,
                 i.settings,i.stream,i.sniffing,i.enabled,i.remark,i.created_at,i.updated_at,
                 s.name,s.address
            FROM nodes n
            JOIN inbounds i ON i.id = n.inbound_id
            JOIN servers  s ON s.id = i.server_id`
	args := []any{}
	if serverID != "" {
		q += ` WHERE i.server_id=?`
		args = append(args, serverID)
	}
	q += ` ORDER BY n.sort ASC, i.port ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NodeView
	for rows.Next() {
		var v NodeView
		var nEnabled, iEnabled int
		if err := rows.Scan(
			&v.ID, &v.InboundID, &v.Name, &v.Region, &v.Tags, &v.Sort, &nEnabled, &v.CreatedAt, &v.UpdatedAt,
			&v.Inbound.ID, &v.Inbound.ServerID, &v.Inbound.Tag, &v.Inbound.Protocol, &v.Inbound.Transport,
			&v.Inbound.Security, &v.Inbound.Port, &v.Inbound.Listen, &v.Inbound.Settings, &v.Inbound.Stream,
			&v.Inbound.Sniffing, &iEnabled, &v.Inbound.Remark, &v.Inbound.CreatedAt, &v.Inbound.UpdatedAt,
			&v.ServerName, &v.ServerAddr,
		); err != nil {
			return nil, err
		}
		v.Enabled = nEnabled != 0
		v.Inbound.Enabled = iEnabled != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// InboundClient
// ---------------------------------------------------------------------------

// UpsertClient 为某用户在指定入站上创建或复用一条客户端凭据。
func (s *Store) UpsertClient(ctx context.Context, inboundID, userID, email, flow string) (InboundClient, error) {
	if existing, err := s.GetClient(ctx, inboundID, userID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return InboundClient{}, err
	}
	c := InboundClient{
		ID: idgen.New("cli"), InboundID: inboundID, UserID: userID,
		UUID: idgen.NewUUID(), Email: email, Flow: flow, Enabled: true, CreatedAt: nowMS(),
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO inbound_clients(`+clientCols+`) VALUES(?,?,?,?,?,?,?,?)`,
		c.ID, c.InboundID, c.UserID, c.UUID, c.Email, c.Flow, b2i(c.Enabled), c.CreatedAt)
	if err != nil {
		return InboundClient{}, err
	}
	return c, nil
}

// GetClient 取指定用户在某入站上的凭据。
func (s *Store) GetClient(ctx context.Context, inboundID, userID string) (InboundClient, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+clientCols+` FROM inbound_clients WHERE inbound_id=? AND user_id=?`, inboundID, userID)
	c, err := scanClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return InboundClient{}, ErrNotFound
	}
	return c, err
}

// ListClients 列出某入站的全部凭据。
func (s *Store) ListClients(ctx context.Context, inboundID string) ([]InboundClient, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+clientCols+` FROM inbound_clients WHERE inbound_id=? ORDER BY created_at ASC`, inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboundClient
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListUserClients 列出某用户的全部凭据（订阅生成用）。
func (s *Store) ListUserClients(ctx context.Context, userID string) ([]InboundClient, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+clientCols+` FROM inbound_clients WHERE user_id=? AND enabled=1 ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboundClient
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeClient 删除凭据。
func (s *Store) RevokeClient(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM inbound_clients WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
