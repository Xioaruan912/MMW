package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mmx-project/mmx/internal/idgen"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 是数据访问入口。所有方法都要求显式传入 context。
type Store struct {
	db *DB
}

// New 构造 Store 并执行迁移。
func New(ctx context.Context, db *DB) (*Store, error) {
	if err := db.Migrate(ctx); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// DB 暴露底层连接（供需要事务的模块使用）。
func (s *Store) DB() *DB { return s.db }

func nowMS() int64 { return time.Now().UnixMilli() }

// isNoRows 判断是否为 sql.ErrNoRows（避免上层重复导入 database/sql）。
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

// Audit 写入一条审计记录。
func (s *Store) Audit(ctx context.Context, action, actor, target, detail, ip string) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_logs(id,ts,actor,action,target,detail,ip) VALUES(?,?,?,?,?,?,?)`,
		idgen.New(idgen.PrefixAudit), nowMS(), actor, action, target, detail, ip)
	_ = err // 审计失败不应打断主流程
}

// ListAudit 按时间倒序取审计记录。
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,ts,actor,action,target,detail,ip FROM audit_logs ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var a AuditLog
		if err := rows.Scan(&a.ID, &a.TS, &a.Actor, &a.Action, &a.Target, &a.Detail, &a.IP); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 事务辅助
// ---------------------------------------------------------------------------

// Tx 在事务中执行 fn；出错自动回滚。
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
