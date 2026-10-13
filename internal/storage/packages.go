package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// randRead 是 crypto/rand.Read 的薄封装，便于集中替换实现。
func randRead(b []byte) (int, error) { return rand.Read(b) }

// Package 是一个套餐（流量 + 时长 + 价格）。
type Package struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// TrafficLimit 是套餐包含的流量（字节，0 = 不限）
	TrafficLimit int64 `json:"traffic_limit"`
	// DurationDays 是有效期天数（0 = 不过期）
	DurationDays int `json:"duration_days"`
	// PriceCents 是价格（以分为单位，避免浮点误差）
	PriceCents int64  `json:"price_cents"`
	Currency   string `json:"currency"`
	// MaxNodes / MaxServers 是套餐允许的节点/服务器数（0 = 不限）——用于拼车与子账号场景
	MaxNodes   int   `json:"max_nodes"`
	MaxServers int   `json:"max_servers"`
	Sort       int   `json:"sort"`
	Enabled    bool  `json:"enabled"`
	CreatedAt  int64 `json:"created_at"`
	UpdatedAt  int64 `json:"updated_at"`
}

// RedeemCode 是一张兑换码。
type RedeemCode struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	// PackageID 为空表示只加时长/流量，不绑定套餐
	PackageID    string `json:"package_id"`
	TrafficBytes int64  `json:"traffic_bytes"`
	DurationDays int    `json:"duration_days"`
	MaxUses      int    `json:"max_uses"`
	UsedCount    int    `json:"used_count"`
	ExpiresAt    int64  `json:"expires_at"`
	CreatedAt    int64  `json:"created_at"`
}

const pkgCols = `id,name,description,traffic_limit,duration_days,price_cents,currency,
                 max_nodes,max_servers,sort,enabled,created_at,updated_at`
const redeemCols = `id,code,package_id,traffic_bytes,duration_days,max_uses,used_count,expires_at,created_at`

// ---------------------------------------------------------------------------
// 套餐
// ---------------------------------------------------------------------------

// CreatePackage 新建套餐。
func (s *Store) CreatePackage(ctx context.Context, p Package) (Package, error) {
	if p.ID == "" {
		p.ID = idgen.New("pkg")
	}
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	if p.DurationDays < 0 || p.TrafficLimit < 0 || p.PriceCents < 0 {
		return Package{}, errors.New("流量/时长/价格不能为负")
	}
	now := nowMS()
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO packages(`+pkgCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Description, p.TrafficLimit, p.DurationDays, p.PriceCents,
		p.Currency, p.MaxNodes, p.MaxServers, p.Sort, b2i(p.Enabled), p.CreatedAt, p.UpdatedAt)
	return p, err
}

// GetPackage 取套餐。
func (s *Store) GetPackage(ctx context.Context, id string) (Package, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+pkgCols+` FROM packages WHERE id=?`, id)
	var p Package
	var enabled int
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.TrafficLimit, &p.DurationDays,
		&p.PriceCents, &p.Currency, &p.MaxNodes, &p.MaxServers, &p.Sort, &enabled,
		&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Package{}, ErrNotFound
	}
	p.Enabled = enabled != 0
	return p, err
}

// ListPackages 列出套餐。
func (s *Store) ListPackages(ctx context.Context) ([]Package, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pkgCols+` FROM packages ORDER BY sort ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		var p Package
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.TrafficLimit, &p.DurationDays,
			&p.PriceCents, &p.Currency, &p.MaxNodes, &p.MaxServers, &p.Sort, &enabled,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePackage 更新套餐。
func (s *Store) UpdatePackage(ctx context.Context, p Package) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE packages SET name=?, description=?, traffic_limit=?, duration_days=?,
             price_cents=?, currency=?, max_nodes=?, max_servers=?, sort=?, enabled=?,
             updated_at=? WHERE id=?`,
		p.Name, p.Description, p.TrafficLimit, p.DurationDays, p.PriceCents, p.Currency,
		p.MaxNodes, p.MaxServers, p.Sort, b2i(p.Enabled), nowMS(), p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePackage 删除套餐。
func (s *Store) DeletePackage(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM packages WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyPackageOptions 控制套餐绑定的行为。
type ApplyPackageOptions struct {
	// ResetTraffic 为 true 时把已用流量清零（续费场景通常要重置）
	ResetTraffic bool
	// ExtendFromNow 为 true 时到期时间从当前时间起算（而不是从原到期时间续期）
	ExtendFromNow bool
}

// ApplyPackage 把套餐绑定到用户，并派生出配额与到期时间。
//
// 语义说明（最容易出错的地方）：
//   - 流量上限 = 套餐流量；ResetTraffic 时同时清零已用
//   - 到期时间：已有未过期的到期时间且 ExtendFromNow=false 时**顺延**；
//     否则从当前时间起算
//   - 套餐时长为 0 表示不过期（expire_at=0）
func (s *Store) ApplyPackage(ctx context.Context, userID, packageID string, opts ApplyPackageOptions) (User, error) {
	pkg, err := s.GetPackage(ctx, packageID)
	if err != nil {
		return User{}, err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return User{}, err
	}

	now := time.Now()
	var base time.Time
	if !opts.ExtendFromNow && u.ExpireAt > now.UnixMilli() {
		base = time.UnixMilli(u.ExpireAt) // 未过期则顺延
	} else {
		base = now
	}

	u.PackageID = pkg.ID
	u.TrafficLimit = pkg.TrafficLimit
	if opts.ResetTraffic {
		u.TrafficUsed = 0
	}
	if pkg.DurationDays > 0 {
		u.ExpireAt = base.AddDate(0, 0, pkg.DurationDays).UnixMilli()
	} else {
		u.ExpireAt = 0
	}
	if err := s.UpdateUser(ctx, u); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, userID)
}

// ---------------------------------------------------------------------------
// 兑换码
// ---------------------------------------------------------------------------

// CreateRedeemCode 生成一张兑换码。
func (s *Store) CreateRedeemCode(ctx context.Context, c RedeemCode) (RedeemCode, error) {
	if c.ID == "" {
		c.ID = idgen.New("rdm")
	}
	if c.Code == "" {
		code, err := randomCode(16)
		if err != nil {
			return RedeemCode{}, err
		}
		c.Code = code
	}
	if c.MaxUses <= 0 {
		c.MaxUses = 1
	}
	c.CreatedAt = nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO redeem_codes(`+redeemCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Code, c.PackageID, c.TrafficBytes, c.DurationDays, c.MaxUses,
		c.UsedCount, c.ExpiresAt, c.CreatedAt)
	return c, err
}

// ListRedeemCodes 列出兑换码。
func (s *Store) ListRedeemCodes(ctx context.Context, limit int) ([]RedeemCode, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+redeemCols+` FROM redeem_codes ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RedeemCode
	for rows.Next() {
		var c RedeemCode
		if err := rows.Scan(&c.ID, &c.Code, &c.PackageID, &c.TrafficBytes, &c.DurationDays,
			&c.MaxUses, &c.UsedCount, &c.ExpiresAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteRedeemCode 删除兑换码。
func (s *Store) DeleteRedeemCode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM redeem_codes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrRedeemInvalid 表示兑换码不可用。
var ErrRedeemInvalid = errors.New("兑换码无效或已用尽")

// Redeem 兑换：原子地占用一次使用次数并派发权益。
//
// 必须在同一事务里「检查 + 自增」，否则并发兑换会把同一张码用超。
func (s *Store) Redeem(ctx context.Context, code, userID string) (User, RedeemCode, error) {
	var rec RedeemCode
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			`SELECT `+redeemCols+` FROM redeem_codes WHERE code=?`, code)
		if err := row.Scan(&rec.ID, &rec.Code, &rec.PackageID, &rec.TrafficBytes,
			&rec.DurationDays, &rec.MaxUses, &rec.UsedCount, &rec.ExpiresAt, &rec.CreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRedeemInvalid
			}
			return err
		}
		now := time.Now()
		if rec.ExpiresAt != 0 && now.UnixMilli() > rec.ExpiresAt {
			return ErrRedeemInvalid
		}
		if rec.UsedCount >= rec.MaxUses {
			return ErrRedeemInvalid
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE redeem_codes SET used_count = used_count + 1 WHERE id=?`, rec.ID); err != nil {
			return err
		}
		rec.UsedCount++

		// 读用户当前状态
		urow := tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, userID)
		u, err := scanUser(urow)
		if err != nil {
			return err
		}

		// 派发权益
		var base time.Time
		if u.ExpireAt > now.UnixMilli() {
			base = time.UnixMilli(u.ExpireAt)
		} else {
			base = now
		}
		if rec.TrafficBytes > 0 {
			u.TrafficLimit += rec.TrafficBytes
		}
		if rec.DurationDays > 0 {
			u.ExpireAt = base.AddDate(0, 0, rec.DurationDays).UnixMilli()
		}
		if rec.PackageID != "" {
			var pkgLimit int64
			var pkgDays int
			if err := tx.QueryRowContext(ctx,
				`SELECT traffic_limit, duration_days FROM packages WHERE id=?`, rec.PackageID).
				Scan(&pkgLimit, &pkgDays); err == nil {
				u.PackageID = rec.PackageID
				if pkgLimit > 0 {
					u.TrafficLimit = pkgLimit
				}
				if pkgDays > 0 {
					u.ExpireAt = base.AddDate(0, 0, pkgDays).UnixMilli()
				}
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET traffic_limit=?, expire_at=?, package_id=?, updated_at=? WHERE id=?`,
			u.TrafficLimit, u.ExpireAt, u.PackageID, nowMS(), userID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return User{}, RedeemCode{}, err
	}
	u, err := s.GetUser(ctx, userID)
	return u, rec, err
}

// randomCode 生成可读的兑换码（去掉易混淆字符 0/O/1/I/L）。
func randomCode(n int) (string, error) {
	const alphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	b := make([]byte, n)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	// 每 4 位插一个连字符，便于口述与抄写
	var sb []byte
	for i, c := range out {
		if i > 0 && i%4 == 0 {
			sb = append(sb, '-')
		}
		sb = append(sb, c)
	}
	return string(sb), nil
}
