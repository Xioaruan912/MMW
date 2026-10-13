// Package storage 是数据访问层。主控的所有持久化状态都经过这里。
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，无 cgo
)

// DB 包装 *sql.DB，附带驱动信息与迁移能力。
type DB struct {
	*sql.DB
	Driver string
}

// Open 打开数据库连接。driver 支持 sqlite / postgres。
func Open(driver, dsn string) (*DB, error) {
	switch driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
		dsn = sqliteDSN(dsn)
	case "postgres":
		// 预留：接入 jackc/pgx 的 stdlib 驱动
		return nil, errors.New("postgres 驱动尚未接入（路线图 P7）")
	default:
		return nil, fmt.Errorf("不支持的驱动 %q", driver)
	}

	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // SQLite 单写者；Postgres 接入后需调整
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	return &DB{DB: sqlDB, Driver: driver}, nil
}

// sqliteDSN 给 DSN 补上必要的 pragma（WAL、外键、忙等待）。
func sqliteDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(10000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
	}
	return "file:" + path + "?" + strings.Join(pragmas, "&")
}

// ---------------------------------------------------------------------------
// 迁移
// ---------------------------------------------------------------------------

type migration struct {
	Version int
	Name    string
	SQL     string
}

// migrations 是**只追加**的列表：已经发布的条目永远不要修改，只能新增。
var migrations = []migration{
	{
		Version: 1,
		Name:    "core_agents_and_users",
		SQL: `
-- ---------- 用户与鉴权 ----------
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    nickname      TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL DEFAULT 'user',      -- admin | user
    status        TEXT NOT NULL DEFAULT 'active',    -- active | disabled
    totp_secret   TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_exp  ON sessions(expires_at);

-- ---------- 受管服务器 ----------
CREATE TABLE IF NOT EXISTS servers (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    address           TEXT NOT NULL DEFAULT '',
    remark            TEXT NOT NULL DEFAULT '',
    xray_mode         TEXT NOT NULL DEFAULT 'external',  -- embedded | external | none
    status            TEXT NOT NULL DEFAULT 'offline',   -- online | offline | degraded
    agent_version     TEXT NOT NULL DEFAULT '',
    protocol_version  INTEGER NOT NULL DEFAULT 0,
    last_seen_at      INTEGER NOT NULL DEFAULT 0,
    last_ip           TEXT NOT NULL DEFAULT '',
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_servers_status ON servers(status);

-- ---------- Agent 令牌（三级模型的 ①② 级） ----------
CREATE TABLE IF NOT EXISTS agent_tokens (
    id          TEXT PRIMARY KEY,
    server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,                -- enroll | agent
    token_hash  TEXT NOT NULL UNIQUE,         -- sha256(明文)
    expires_at  INTEGER NOT NULL DEFAULT 0,   -- 0 = 永不过期
    used_at     INTEGER NOT NULL DEFAULT 0,
    revoked_at  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    created_by  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_server ON agent_tokens(server_id);

-- ---------- 运行指标 ----------
CREATE TABLE IF NOT EXISTS server_metrics (
    server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    ts          INTEGER NOT NULL,
    cpu_percent REAL NOT NULL DEFAULT 0,
    mem_used    INTEGER NOT NULL DEFAULT 0,
    mem_total   INTEGER NOT NULL DEFAULT 0,
    disk_used   INTEGER NOT NULL DEFAULT 0,
    disk_total  INTEGER NOT NULL DEFAULT 0,
    load1       REAL NOT NULL DEFAULT 0,
    load5       REAL NOT NULL DEFAULT 0,
    load15      REAL NOT NULL DEFAULT 0,
    net_up      INTEGER NOT NULL DEFAULT 0,
    net_down    INTEGER NOT NULL DEFAULT 0,
    tcp_conns   INTEGER NOT NULL DEFAULT 0,
    udp_conns   INTEGER NOT NULL DEFAULT 0,
    processes   INTEGER NOT NULL DEFAULT 0,
    uptime_sec  INTEGER NOT NULL DEFAULT 0,
    xray_running INTEGER NOT NULL DEFAULT 0,
    traffic_up  INTEGER NOT NULL DEFAULT 0,
    traffic_down INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, ts)
);
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON server_metrics(ts);

-- ---------- 审计 ----------
CREATE TABLE IF NOT EXISTS audit_logs (
    id         TEXT PRIMARY KEY,
    ts         INTEGER NOT NULL,
    actor      TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);
`,
	},
}

// Migrate 应用所有未执行的迁移。
func (db *DB) Migrate(ctx context.Context) error {
	if db.Driver != "sqlite" {
		return errors.New("当前只有 sqlite 迁移实现")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        name    TEXT NOT NULL,
        applied_at INTEGER NOT NULL
    )`); err != nil {
		return fmt.Errorf("创建迁移表失败: %w", err)
	}

	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("读取当前迁移版本失败: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("迁移 v%d (%s) 失败: %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)`,
			m.Version, m.Name, time.Now().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
