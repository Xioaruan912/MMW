package storage

import (
	"context"
	"time"
)

// NodeLatency 是一次节点探测结果。
type NodeLatency struct {
	NodeID   string `json:"node_id"`
	ServerID string `json:"server_id"`
	TS       int64  `json:"ts"`
	// Method 是探测方式：tcp（TCP 握手）| speedtest（完整协议测速）
	Method    string `json:"method"`
	Alive     bool   `json:"alive"`
	LatencyMS int    `json:"latency_ms"`
	// 以下仅 speedtest 有值
	DownloadBPS int64  `json:"download_bps,omitempty"`
	UploadBPS   int64  `json:"upload_bps,omitempty"`
	Error       string `json:"error,omitempty"`
}

// AddNodeLatency 写入一次探测结果。
func (s *Store) AddNodeLatency(ctx context.Context, r NodeLatency) error {
	if r.TS == 0 {
		r.TS = nowMS()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO node_latency(
            node_id,ts,server_id,method,alive,latency_ms,download_bps,upload_bps,error)
         VALUES(?,?,?,?,?,?,?,?,?)`,
		r.NodeID, r.TS, r.ServerID, r.Method, b2i(r.Alive), r.LatencyMS,
		r.DownloadBPS, r.UploadBPS, r.Error)
	return err
}

// LatestNodeLatency 返回每个节点的最新一条探测结果。
func (s *Store) LatestNodeLatency(ctx context.Context, serverID string) (map[string]NodeLatency, error) {
	q := `SELECT node_id,ts,server_id,method,alive,latency_ms,download_bps,upload_bps,error
            FROM node_latency`
	args := []any{}
	if serverID != "" {
		q += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]NodeLatency{}
	for rows.Next() {
		var r NodeLatency
		var alive int
		if err := rows.Scan(&r.NodeID, &r.TS, &r.ServerID, &r.Method, &alive,
			&r.LatencyMS, &r.DownloadBPS, &r.UploadBPS, &r.Error); err != nil {
			return nil, err
		}
		r.Alive = alive != 0
		// 表以 (node_id, ts) 为主键，同一节点可能有多条历史；保留最新的
		if prev, ok := out[r.NodeID]; !ok || r.TS > prev.TS {
			out[r.NodeID] = r
		}
	}
	return out, rows.Err()
}

// NodeLatencySeries 返回某节点的探测历史。
func (s *Store) NodeLatencySeries(ctx context.Context, nodeID string, limit int) ([]NodeLatency, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id,ts,server_id,method,alive,latency_ms,download_bps,upload_bps,error
           FROM node_latency WHERE node_id=? ORDER BY ts DESC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeLatency
	for rows.Next() {
		var r NodeLatency
		var alive int
		if err := rows.Scan(&r.NodeID, &r.TS, &r.ServerID, &r.Method, &alive,
			&r.LatencyMS, &r.DownloadBPS, &r.UploadBPS, &r.Error); err != nil {
			return nil, err
		}
		r.Alive = alive != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneNodeLatency 清理旧探测记录。
func (s *Store) PruneNodeLatency(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM node_latency WHERE ts < ?`,
		time.Now().Add(-olderThan).UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
