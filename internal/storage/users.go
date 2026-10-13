package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// 角色与状态。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
	StatusOn  = "active"
	StatusOff = "disabled"
)

const userCols = `id,username,password_hash,nickname,email,role,status,totp_secret,created_at,updated_at`

func scanUser(sc interface {
	Scan(...any) error
}) (User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Nickname, &u.Email,
		&u.Role, &u.Status, &u.TOTPSecret, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

// CountUsers 返回用户总数（用于首启判断是否需要初始化管理员）。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser 新建用户。passwordHash 必须由调用方先算好。
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, nickname, email, role string) (User, error) {
	if role == "" {
		role = RoleUser
	}
	now := nowMS()
	u := User{
		ID: idgen.New(idgen.PrefixUser), Username: username, PasswordHash: passwordHash,
		Nickname: nickname, Email: email, Role: role, Status: StatusOn,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(`+userCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, u.Nickname, u.Email, u.Role, u.Status,
		u.TOTPSecret, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUserByUsername 按用户名查用户。
func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username=?`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// GetUser 按 ID 查用户。
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// ListUsers 列出用户。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 会话
// ---------------------------------------------------------------------------

// CreateSession 落库一条会话（只存 token 的 hash）。
func (s *Store) CreateSession(ctx context.Context, tokenHash, userID, userAgent, ip string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(token_hash,user_id,created_at,expires_at,user_agent,ip) VALUES(?,?,?,?,?,?)`,
		tokenHash, userID, now.UnixMilli(), now.Add(ttl).UnixMilli(), userAgent, ip)
	return err
}

// SessionUser 用会话 token 的 hash 换回用户；过期或不存在返回 ErrNotFound。
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT u.`+replaceAll(userCols, ",", ",u.")+`
           FROM sessions s JOIN users u ON u.id = s.user_id
          WHERE s.token_hash=? AND s.expires_at > ?`,
		tokenHash, time.Now().UnixMilli())
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// DeleteSession 登出。
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// PruneSessions 清理过期会话。
func (s *Store) PruneSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func replaceAll(s, old, new string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			out = append(out, new...)
			i += len(old) - 1
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
