package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// APIToken 是一个可轮换、可吊销、带作用域的接口令牌。
//
// 与会话 cookie 的区别：
//   - 面向程序调用（脚本 / CI / AI Agent），不依赖浏览器
//   - 不参与 CSRF（没有 cookie 就没有跨站自动携带问题）
//   - 权限等同所属用户，但可单独吊销
type APIToken struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	Name       string `json:"name"`
	TokenHash  string `json:"-"`
	Scopes     string `json:"scopes"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	LastUsedAt int64  `json:"last_used_at"`
	RevokedAt  int64  `json:"revoked_at"`
}

// Valid 判断令牌在给定时刻是否可用。
func (t APIToken) Valid(now time.Time) bool {
	if t.RevokedAt != 0 {
		return false
	}
	if t.ExpiresAt != 0 && now.UnixMilli() > t.ExpiresAt {
		return false
	}
	return true
}

const apiTokenCols = `id,user_id,name,token_hash,scopes,created_at,expires_at,last_used_at,revoked_at`

func scanAPIToken(sc interface{ Scan(...any) error }) (APIToken, error) {
	var t APIToken
	err := sc.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Scopes,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	return t, err
}

// CreateAPIToken 落库一条令牌（只存 hash）。
func (s *Store) CreateAPIToken(ctx context.Context, userID, name, hash, scopes string, ttl time.Duration) (APIToken, error) {
	var exp int64
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixMilli()
	}
	t := APIToken{
		ID: idgen.New("atk"), UserID: userID, Name: name, TokenHash: hash,
		Scopes: scopes, CreatedAt: nowMS(), ExpiresAt: exp,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens(`+apiTokenCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.ID, t.UserID, t.Name, t.TokenHash, t.Scopes, t.CreatedAt, t.ExpiresAt, 0, 0)
	if err != nil {
		return APIToken{}, err
	}
	return t, nil
}

// FindAPITokenByHash 按 hash 查令牌。
func (s *Store) FindAPITokenByHash(ctx context.Context, hash string) (APIToken, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE token_hash=?`, hash)
	t, err := scanAPIToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	return t, err
}

// TouchAPIToken 记录最近使用时间（失败不影响主流程）。
func (s *Store) TouchAPIToken(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, nowMS(), id)
}

// ListAPITokens 列出某用户的令牌（不含明文与 hash）。
func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken 吊销令牌；同时校验归属，避免越权吊销他人令牌。
func (s *Store) RevokeAPIToken(ctx context.Context, userID, tokenID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at=0`,
		nowMS(), tokenID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 双因子（TOTP）
// ---------------------------------------------------------------------------

// TOTPState 是用户的双因子状态。
type TOTPState struct {
	Secret        string `json:"-"` // 不入 JSON，避免误泄漏
	Enabled       bool   `json:"enabled"`
	RecoveryCodes string `json:"-"` // JSON 数组（存的是 hash）
	PendingSecret string `json:"-"` // 已生成但尚未验证的密钥
}

// GetTOTPState 读取双因子状态。
func (s *Store) GetTOTPState(ctx context.Context, userID string) (TOTPState, error) {
	var st TOTPState
	var enabled int
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret, totp_enabled, recovery_codes, totp_pending_secret
           FROM users WHERE id=?`, userID).
		Scan(&st.Secret, &enabled, &st.RecoveryCodes, &st.PendingSecret)
	if errors.Is(err, sql.ErrNoRows) {
		return TOTPState{}, ErrNotFound
	}
	st.Enabled = enabled != 0
	return st, err
}

// SetTOTPPendingSecret 保存待验证密钥（用户扫码后还没输入验证码）。
func (s *Store) SetTOTPPendingSecret(ctx context.Context, userID, secret string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_pending_secret=?, updated_at=? WHERE id=?`, secret, nowMS(), userID)
	return err
}

// EnableTOTP 启用双因子并写入恢复码哈希。
func (s *Store) EnableTOTP(ctx context.Context, userID, secret, recoveryCodes string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret=?, totp_enabled=1, recovery_codes=?,
             totp_pending_secret='', updated_at=? WHERE id=?`,
		secret, recoveryCodes, nowMS(), userID)
	return err
}

// DisableTOTP 关闭双因子并清空相关字段。
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret='', totp_enabled=0, recovery_codes='',
             totp_pending_secret='', updated_at=? WHERE id=?`, nowMS(), userID)
	return err
}

// ConsumeRecoveryCode 用掉一个恢复码（命中则重写剩余列表）。
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID string, remaining string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET recovery_codes=?, updated_at=? WHERE id=?`, remaining, nowMS(), userID)
	return err
}
