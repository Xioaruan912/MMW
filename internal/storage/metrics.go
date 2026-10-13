package storage

import (
	"context"
	"time"
)

// InsertMetrics 落库一次指标上报。
func (s *Store) InsertMetrics(ctx context.Context, m ServerMetrics) error {
	if m.TS == 0 {
		m.TS = nowMS()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO server_metrics(
            server_id,ts,cpu_percent,mem_used,mem_total,disk_used,disk_total,
            load1,load5,load15,net_up,net_down,tcp_conns,udp_conns,processes,
            uptime_sec,xray_running,traffic_up,traffic_down)
         VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ServerID, m.TS, m.CPUPercent, m.MemUsed, m.MemTotal, m.DiskUsed, m.DiskTotal,
		m.Load1, m.Load5, m.Load15, m.NetUp, m.NetDown, m.TCPConns, m.UDPConns, m.Processes,
		m.UptimeSec, boolToInt(m.XrayRunning), m.TrafficUp, m.TrafficDown)
	return err
}

// LatestMetrics 取某台服务器的最新一条指标；无数据返回 (零值,false,nil)。
func (s *Store) LatestMetrics(ctx context.Context, serverID string) (ServerMetrics, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT server_id,ts,cpu_percent,mem_used,mem_total,disk_used,disk_total,
                load1,load5,load15,net_up,net_down,tcp_conns,udp_conns,processes,
                uptime_sec,xray_running,traffic_up,traffic_down
           FROM server_metrics WHERE server_id=? ORDER BY ts DESC LIMIT 1`, serverID)
	var m ServerMetrics
	var xray int
	err := row.Scan(&m.ServerID, &m.TS, &m.CPUPercent, &m.MemUsed, &m.MemTotal,
		&m.DiskUsed, &m.DiskTotal, &m.Load1, &m.Load5, &m.Load15, &m.NetUp, &m.NetDown,
		&m.TCPConns, &m.UDPConns, &m.Processes, &m.UptimeSec, &xray, &m.TrafficUp, &m.TrafficDown)
	if err != nil {
		if isNoRows(err) {
			return ServerMetrics{}, false, nil
		}
		return ServerMetrics{}, false, err
	}
	m.XrayRunning = xray != 0
	return m, true, nil
}

// SeriesMetrics 取时间区间内的指标序列（升序），用于探针历史曲线。
func (s *Store) SeriesMetrics(ctx context.Context, serverID string, from, to time.Time, limit int) ([]ServerMetrics, error) {
	if limit <= 0 || limit > 5000 {
		limit = 720
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id,ts,cpu_percent,mem_used,mem_total,disk_used,disk_total,
                load1,load5,load15,net_up,net_down,tcp_conns,udp_conns,processes,
                uptime_sec,xray_running,traffic_up,traffic_down
           FROM server_metrics
          WHERE server_id=? AND ts BETWEEN ? AND ?
          ORDER BY ts ASC LIMIT ?`,
		serverID, from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServerMetrics
	for rows.Next() {
		var m ServerMetrics
		var xray int
		if err := rows.Scan(&m.ServerID, &m.TS, &m.CPUPercent, &m.MemUsed, &m.MemTotal,
			&m.DiskUsed, &m.DiskTotal, &m.Load1, &m.Load5, &m.Load15, &m.NetUp, &m.NetDown,
			&m.TCPConns, &m.UDPConns, &m.Processes, &m.UptimeSec, &xray, &m.TrafficUp, &m.TrafficDown); err != nil {
			return nil, err
		}
		m.XrayRunning = xray != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneMetrics 删除早于 cutoff 的指标，返回删除行数。
func (s *Store) PruneMetrics(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM server_metrics WHERE ts < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
