package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// 公告级别。
const (
	AnnounceInfo      = "info"
	AnnounceWarning   = "warning"
	AnnounceImportant = "important"
)

// Announcement 是一条面向用户的公告。
type Announcement struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Level     string `json:"level"`
	Enabled   bool   `json:"enabled"`
	Pinned    bool   `json:"pinned"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	ExpiresAt int64  `json:"expires_at"`
}

const announceCols = `id,title,content,level,enabled,pinned,created_at,updated_at,expires_at`

func scanAnnouncement(sc interface{ Scan(...any) error }) (Announcement, error) {
	var a Announcement
	var enabled, pinned int
	err := sc.Scan(&a.ID, &a.Title, &a.Content, &a.Level, &enabled, &pinned,
		&a.CreatedAt, &a.UpdatedAt, &a.ExpiresAt)
	a.Enabled = enabled != 0
	a.Pinned = pinned != 0
	return a, err
}

// CreateAnnouncement 新建公告。
func (s *Store) CreateAnnouncement(ctx context.Context, a Announcement) (Announcement, error) {
	if a.Level == "" {
		a.Level = AnnounceInfo
	}
	switch a.Level {
	case AnnounceInfo, AnnounceWarning, AnnounceImportant:
	default:
		return Announcement{}, errors.New("level 必须是 info / warning / important")
	}
	now := nowMS()
	a.ID = idgen.New("ann")
	a.CreatedAt, a.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO announcements(`+announceCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Title, a.Content, a.Level, b2i(a.Enabled), b2i(a.Pinned),
		a.CreatedAt, a.UpdatedAt, a.ExpiresAt)
	return a, err
}

// UpdateAnnouncement 更新公告。
func (s *Store) UpdateAnnouncement(ctx context.Context, a Announcement) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE announcements SET title=?, content=?, level=?, enabled=?, pinned=?,
             expires_at=?, updated_at=? WHERE id=?`,
		a.Title, a.Content, a.Level, b2i(a.Enabled), b2i(a.Pinned),
		a.ExpiresAt, nowMS(), a.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAnnouncement 删除公告。
func (s *Store) DeleteAnnouncement(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM announcements WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAnnouncement 取公告。
func (s *Store) GetAnnouncement(ctx context.Context, id string) (Announcement, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+announceCols+` FROM announcements WHERE id=?`, id)
	a, err := scanAnnouncement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Announcement{}, ErrNotFound
	}
	return a, err
}

// ListActiveAnnouncements 返回当前对用户可见的公告（已启用且未过期）。
func (s *Store) ListActiveAnnouncements(ctx context.Context, limit int) ([]Announcement, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+announceCols+` FROM announcements
          WHERE enabled=1 AND (expires_at=0 OR expires_at > ?)
          ORDER BY pinned DESC, created_at DESC LIMIT ?`, nowMS(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Announcement
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAllAnnouncements 列出全部公告（管理端）。
func (s *Store) ListAllAnnouncements(ctx context.Context, limit int) ([]Announcement, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+announceCols+` FROM announcements ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Announcement
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

var _ = time.Now
