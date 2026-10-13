package storage

import (
	"context"
	"database/sql"
	"time"
)

// TrafficPoint 是一个「日 × 维度」的流量点。
type TrafficPoint struct {
	Day  string `json:"day"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

// TrafficSummary 是一段时间的流量汇总。
type TrafficSummary struct {
	Days   int            `json:"days"`
	Up     int64          `json:"up"`
	Down   int64          `json:"down"`
	Total  int64          `json:"total"`
	Series []TrafficPoint `json:"series"`
}

// dayKey 返回主控本地时区下的日期键（YYYY-MM-DD）。
//
// 流量按**本地日**切分，与运维直觉一致；跨时区部署时统一以主控时区为准。
func dayKey(t time.Time) string {
	return t.Format("2006-01-02")
}

// DayKey 对外暴露日期键计算（供聚合器使用）。
func DayKey(t time.Time) string { return dayKey(t) }

// AddServerTraffic 累加某台服务器当天的流量。
func (s *Store) AddServerTraffic(ctx context.Context, serverID string, up, down int64) error {
	if up == 0 && down == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO traffic_daily(day,server_id,up,down) VALUES(?,?,?,?)
         ON CONFLICT(day,server_id) DO UPDATE SET up = up + excluded.up, down = down + excluded.down`,
		dayKey(time.Now()), serverID, up, down)
	return err
}

// AddUserTraffic 累加某个用户当天的流量，并同步累计到 users.traffic_used。
//
// 两个写入必须同事务：否则一旦中途失败，日表与总额会对不上，
// 而用户看到的是总额（配额判断依据），对账时很难定位。
func (s *Store) AddUserTraffic(ctx context.Context, userID string, up, down int64) error {
	if up == 0 && down == 0 {
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_traffic_daily(day,user_id,up,down) VALUES(?,?,?,?)
             ON CONFLICT(day,user_id) DO UPDATE SET up = up + excluded.up, down = down + excluded.down`,
			dayKey(time.Now()), userID, up, down); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE users SET traffic_used = traffic_used + ?, updated_at=? WHERE id=?`,
			up+down, nowMS(), userID)
		return err
	})
}

// ServerTrafficSummary 汇总服务器近 N 天流量。
func (s *Store) ServerTrafficSummary(ctx context.Context, serverID string, days int) (TrafficSummary, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	from := dayKey(time.Now().AddDate(0, 0, -(days - 1)))

	rows, err := s.db.QueryContext(ctx,
		`SELECT day, up, down FROM traffic_daily WHERE server_id=? AND day >= ? ORDER BY day ASC`,
		serverID, from)
	if err != nil {
		return TrafficSummary{}, err
	}
	defer rows.Close()

	out := TrafficSummary{Days: days, Series: []TrafficPoint{}}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.Day, &p.Up, &p.Down); err != nil {
			return TrafficSummary{}, err
		}
		out.Up += p.Up
		out.Down += p.Down
		out.Series = append(out.Series, p)
	}
	out.Total = out.Up + out.Down
	return out, rows.Err()
}

// UserTrafficSummary 汇总某个用户近 N 天流量。
func (s *Store) UserTrafficSummary(ctx context.Context, userID string, days int) (TrafficSummary, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	from := dayKey(time.Now().AddDate(0, 0, -(days - 1)))

	rows, err := s.db.QueryContext(ctx,
		`SELECT day, up, down FROM user_traffic_daily WHERE user_id=? AND day >= ? ORDER BY day ASC`,
		userID, from)
	if err != nil {
		return TrafficSummary{}, err
	}
	defer rows.Close()

	out := TrafficSummary{Days: days, Series: []TrafficPoint{}}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.Day, &p.Up, &p.Down); err != nil {
			return TrafficSummary{}, err
		}
		out.Up += p.Up
		out.Down += p.Down
		out.Series = append(out.Series, p)
	}
	out.Total = out.Up + out.Down
	return out, rows.Err()
}

// AllServersTrafficSummary 汇总全部服务器近 N 天流量（按日合并）。
func (s *Store) AllServersTrafficSummary(ctx context.Context, days int) (TrafficSummary, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	from := dayKey(time.Now().AddDate(0, 0, -(days - 1)))

	rows, err := s.db.QueryContext(ctx,
		`SELECT day, SUM(up), SUM(down) FROM traffic_daily WHERE day >= ?
          GROUP BY day ORDER BY day ASC`, from)
	if err != nil {
		return TrafficSummary{}, err
	}
	defer rows.Close()

	out := TrafficSummary{Days: days, Series: []TrafficPoint{}}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.Day, &p.Up, &p.Down); err != nil {
			return TrafficSummary{}, err
		}
		out.Up += p.Up
		out.Down += p.Down
		out.Series = append(out.Series, p)
	}
	out.Total = out.Up + out.Down
	return out, rows.Err()
}

// UserQuotaState 描述用户的配额使用情况。
type UserQuotaState struct {
	UserID       string `json:"user_id"`
	TrafficUsed  int64  `json:"traffic_used"`
	TrafficLimit int64  `json:"traffic_limit"`
	Unlimited    bool   `json:"unlimited"`
	Exhausted    bool   `json:"exhausted"`
	Expired      bool   `json:"expired"`
	ExpireAt     int64  `json:"expire_at"`
	Remaining    int64  `json:"remaining"`
}

// GetUserQuota 计算用户的剩余额度与是否超限。
func (s *Store) GetUserQuota(ctx context.Context, userID string) (UserQuotaState, error) {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return UserQuotaState{}, err
	}
	st := UserQuotaState{
		UserID:       u.ID,
		TrafficUsed:  u.TrafficUsed,
		TrafficLimit: u.TrafficLimit,
		ExpireAt:     u.ExpireAt,
		Unlimited:    u.TrafficLimit == 0,
	}
	if st.Unlimited {
		st.Remaining = -1
	} else {
		st.Remaining = u.TrafficLimit - u.TrafficUsed
		if st.Remaining < 0 {
			st.Remaining = 0
		}
		st.Exhausted = u.TrafficUsed >= u.TrafficLimit
	}
	if u.ExpireAt > 0 && time.Now().UnixMilli() > u.ExpireAt {
		st.Expired = true
	}
	return st, nil
}

// ServerTrafficRange 汇总某台服务器在 [from, to) 区间的流量。
//
// 用于计费周期口径（探针页与账单），与「自然月」不同 —— 周期起点是用户配置的每月起始日。
func (s *Store) ServerTrafficRange(ctx context.Context, serverID string, from, to time.Time) (TrafficSummary, error) {
	out := TrafficSummary{Series: []TrafficPoint{}}
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, up, down FROM traffic_daily
          WHERE server_id=? AND day >= ? AND day < ? ORDER BY day ASC`,
		serverID, dayKey(from), dayKey(to))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.Day, &p.Up, &p.Down); err != nil {
			return out, err
		}
		out.Up += p.Up
		out.Down += p.Down
		out.Series = append(out.Series, p)
	}
	out.Total = out.Up + out.Down
	out.Days = len(out.Series)
	return out, rows.Err()
}

// UserTrafficRange 汇总某个用户在 [from, to) 区间的流量。
func (s *Store) UserTrafficRange(ctx context.Context, userID string, from, to time.Time) (TrafficSummary, error) {
	out := TrafficSummary{Series: []TrafficPoint{}}
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, up, down FROM user_traffic_daily
          WHERE user_id=? AND day >= ? AND day < ? ORDER BY day ASC`,
		userID, dayKey(from), dayKey(to))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.Day, &p.Up, &p.Down); err != nil {
			return out, err
		}
		out.Up += p.Up
		out.Down += p.Down
		out.Series = append(out.Series, p)
	}
	out.Total = out.Up + out.Down
	out.Days = len(out.Series)
	return out, rows.Err()
}
