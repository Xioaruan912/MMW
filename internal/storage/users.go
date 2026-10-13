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

const userCols = `id,username,password_hash,nickname,email,role,status,totp_secret,
                   sub_token,traffic_limit,traffic_used,expire_at,template_id,package_id,created_at,updated_at`

func scanUser(sc interface {
	Scan(...any) error
}) (User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Nickname, &u.Email,
		&u.Role, &u.Status, &u.TOTPSecret,
		&u.SubToken, &u.TrafficLimit, &u.TrafficUsed, &u.ExpireAt, &u.TemplateID, &u.PackageID,
		&u.CreatedAt, &u.UpdatedAt)
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
		`INSERT INTO users(`+userCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, u.Nickname, u.Email, u.Role, u.Status,
		u.TOTPSecret, u.SubToken, u.TrafficLimit, u.TrafficUsed, u.ExpireAt, u.TemplateID,
		u.PackageID, u.CreatedAt, u.UpdatedAt)
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

// ---------------------------------------------------------------------------
// 订阅凭据与配额
// ---------------------------------------------------------------------------

// SetUserSubToken 设置（或轮换）用户的订阅凭据。
func (s *Store) SetUserSubToken(ctx context.Context, userID, token string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET sub_token=?, updated_at=? WHERE id=?`,
		token, nowMS(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetUserBySubToken 用订阅凭据查用户。
func (s *Store) GetUserBySubToken(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE sub_token=?`, token)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// UpdateUser 更新用户的可变字段（不含口令）。
func (s *Store) UpdateUser(ctx context.Context, u User) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET nickname=?, email=?, role=?, status=?,
             traffic_limit=?, traffic_used=?, expire_at=?, template_id=?, package_id=?,
             updated_at=? WHERE id=?`,
		u.Nickname, u.Email, u.Role, u.Status,
		u.TrafficLimit, u.TrafficUsed, u.ExpireAt, u.TemplateID, u.PackageID, nowMS(), u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserPassword 设置口令散列。
func (s *Store) SetUserPassword(ctx context.Context, userID, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=?, updated_at=? WHERE id=?`,
		hash, nowMS(), userID)
	return err
}

// DeleteUser 删除用户（其客户端凭据级联删除）。
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
