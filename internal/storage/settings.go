package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

// GetSetting 读取一个设置项；不存在返回 ("", false, nil)。
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting 写入一个设置项。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO system_settings(key,value,updated_at) VALUES(?,?,?)
         ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, nowMS())
	return err
}

// DeleteSetting 删除设置项（恢复默认）。
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM system_settings WHERE key=?`, key)
	return err
}

// GetAllSettings 返回全部设置（用于导出/备份与前端一次性拉取）。
func (s *Store) GetAllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM system_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// GetJSONSetting 把设置项按 JSON 解到 dst；不存在或解析失败时保留 dst 原值。
func (s *Store) GetJSONSetting(ctx context.Context, key string, dst any) (bool, error) {
	raw, ok, err := s.GetSetting(ctx, key)
	if err != nil || !ok || raw == "" {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, err
	}
	return true, nil
}

// SetJSONSetting 把 dst 序列化后写入设置项。
func (s *Store) SetJSONSetting(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, key, string(raw))
}

// GetBoolSetting 读取布尔设置（缺失时返回 def）。
func (s *Store) GetBoolSetting(ctx context.Context, key string, def bool) bool {
	raw, ok, err := s.GetSetting(ctx, key)
	if err != nil || !ok {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return b
}
