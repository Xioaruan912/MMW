package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

const serverCols = `id,name,address,remark,xray_mode,status,agent_version,protocol_version,
                    last_seen_at,last_ip,last_error,created_at,updated_at,
                    region,region_country,region_city,provider_name,provider_url,
                    telecom_paid_peer,traffic_limit,period_start_day,probe_visible,weight`

func scanServer(sc interface {
	Scan(...any) error
}) (Server, error) {
	var s Server
	var paidPeer, probeVisible int
	err := sc.Scan(&s.ID, &s.Name, &s.Address, &s.Remark, &s.XrayMode, &s.Status,
		&s.AgentVersion, &s.ProtocolVersion, &s.LastSeenAt, &s.LastIP, &s.LastError,
		&s.CreatedAt, &s.UpdatedAt,
		&s.Region, &s.RegionCountry, &s.RegionCity, &s.ProviderName, &s.ProviderURL,
		&paidPeer, &s.TrafficLimit, &s.PeriodStartDay, &probeVisible, &s.Weight)
	s.TelecomPaidPeer = paidPeer != 0
	s.ProbeVisible = probeVisible != 0
	return s, err
}

// CreateServer 新建一台服务器记录。
func (s *Store) CreateServer(ctx context.Context, name, address, remark, xrayMode string) (Server, error) {
	if xrayMode == "" {
		xrayMode = XrayModeExternal
	}
	now := nowMS()
	srv := Server{
		ID: idgen.New(idgen.PrefixServer), Name: name, Address: address, Remark: remark,
		XrayMode: xrayMode, Status: ServerOffline, CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO servers(`+serverCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		srv.ID, srv.Name, srv.Address, srv.Remark, srv.XrayMode, srv.Status,
		srv.AgentVersion, srv.ProtocolVersion, srv.LastSeenAt, srv.LastIP, srv.LastError,
		srv.CreatedAt, srv.UpdatedAt,
		srv.Region, srv.RegionCountry, srv.RegionCity, srv.ProviderName, srv.ProviderURL,
		0, srv.TrafficLimit, srv.PeriodStartDay, 1, srv.Weight)
	if err != nil {
		return Server{}, err
	}
	return srv, nil
}

// GetServer 按 ID 取服务器。
func (s *Store) GetServer(ctx context.Context, id string) (Server, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, id)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	return srv, err
}

// ListServers 列出全部服务器（按创建时间）。
func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverCols+` FROM servers ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// UpdateServerDisplay 更新服务器的展示信息（探针/列表页用）。
func (s *Store) UpdateServerDisplay(ctx context.Context, srv Server) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET region=?, region_country=?, region_city=?, provider_name=?,
             provider_url=?, telecom_paid_peer=?, traffic_limit=?, period_start_day=?,
             probe_visible=?, weight=?, updated_at=? WHERE id=?`,
		srv.Region, srv.RegionCountry, srv.RegionCity, srv.ProviderName, srv.ProviderURL,
		b2i(srv.TelecomPaidPeer), srv.TrafficLimit, srv.PeriodStartDay,
		b2i(srv.ProbeVisible), srv.Weight, nowMS(), srv.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateServer 更新可变字段。
func (s *Store) UpdateServer(ctx context.Context, id, name, address, remark, xrayMode string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET name=?,address=?,remark=?,xray_mode=?,updated_at=? WHERE id=?`,
		name, address, remark, xrayMode, nowMS(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteServer 删除服务器及其级联数据。
func (s *Store) DeleteServer(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkServerSeen 记录一次成功心跳，并刷新在线状态与 Agent 元信息。
func (s *Store) MarkServerSeen(ctx context.Context, id, ip, agentVersion string, protoVersion int, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE servers SET status=?, last_seen_at=?, last_ip=?, agent_version=?,
            protocol_version=?, last_error=?, updated_at=? WHERE id=?`,
		ServerOnline, nowMS(), ip, agentVersion, protoVersion, lastErr, nowMS(), id)
	return err
}

// SetServerStatus 单独改状态（用于离线判定）。
func (s *Store) SetServerStatus(ctx context.Context, id, status, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE servers SET status=?, last_error=?, updated_at=? WHERE id=?`,
		status, lastErr, nowMS(), id)
	return err
}

// SweepOffline 把超过 deadline 未上报的服务器标记为 offline，返回受影响数量。
func (s *Store) SweepOffline(ctx context.Context, heartbeat time.Duration, factor int) (int, error) {
	deadline := time.Now().Add(-time.Duration(factor) * heartbeat).UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET status=?, updated_at=?
           WHERE status IN (?,?) AND last_seen_at != 0 AND last_seen_at < ?`,
		ServerOffline, nowMS(), ServerOnline, ServerDegraded, deadline)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---------------------------------------------------------------------------
// AgentToken
// ---------------------------------------------------------------------------

const tokenCols = `id,server_id,kind,token_hash,expires_at,used_at,revoked_at,created_at,created_by`

func scanToken(sc interface {
	Scan(...any) error
}) (AgentToken, error) {
	var t AgentToken
	err := sc.Scan(&t.ID, &t.ServerID, &t.Kind, &t.TokenHash, &t.ExpiresAt,
		&t.UsedAt, &t.RevokedAt, &t.CreatedAt, &t.CreatedBy)
	return t, err
}

// CreateToken 落库一条令牌记录（只存 hash）。
func (s *Store) CreateToken(ctx context.Context, serverID, kind, hash string, ttl time.Duration, createdBy string) (AgentToken, error) {
	var exp int64
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixMilli()
	}
	t := AgentToken{
		ID: idgen.New(idgen.PrefixToken), ServerID: serverID, Kind: kind,
		TokenHash: hash, ExpiresAt: exp, CreatedAt: nowMS(), CreatedBy: createdBy,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_tokens(`+tokenCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ServerID, t.Kind, t.TokenHash, t.ExpiresAt, t.UsedAt, t.RevokedAt,
		t.CreatedAt, t.CreatedBy)
	if err != nil {
		return AgentToken{}, err
	}
	return t, nil
}

// FindTokenByHash 按 hash 查令牌，不区分类型。
func (s *Store) FindTokenByHash(ctx context.Context, hash string) (AgentToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM agent_tokens WHERE token_hash=?`, hash)
	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentToken{}, ErrNotFound
	}
	return t, err
}

// MarkTokenUsed 记录一次性令牌已消费。
func (s *Store) MarkTokenUsed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_tokens SET used_at=? WHERE id=?`, nowMS(), id)
	return err
}

// RevokeTokens 吊销某台服务器某类型的全部令牌（重新生成时用）。
func (s *Store) RevokeTokens(ctx context.Context, serverID, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_tokens SET revoked_at=? WHERE server_id=? AND kind=? AND revoked_at=0`,
		nowMS(), serverID, kind)
	return err
}

// ListTokens 列出某台服务器的令牌（不含明文）。
func (s *Store) ListTokens(ctx context.Context, serverID string) ([]AgentToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tokenCols+` FROM agent_tokens WHERE server_id=? ORDER BY created_at DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
