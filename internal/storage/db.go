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

// migrationV10SQL 是出站与路由规则。
const migrationV10SQL = `
CREATE TABLE IF NOT EXISTS outbounds (
    id           TEXT PRIMARY KEY,
    server_id    TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tag          TEXT NOT NULL,
    protocol     TEXT NOT NULL,
    settings     TEXT NOT NULL DEFAULT '{}',
    stream       TEXT NOT NULL DEFAULT '{}',
    proxy_tag    TEXT NOT NULL DEFAULT '',
    send_through TEXT NOT NULL DEFAULT '',
    remark       TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    sort         INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE(server_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_outbounds_server ON outbounds(server_id);

CREATE TABLE IF NOT EXISTS routing_rules (
    id           TEXT PRIMARY KEY,
    server_id    TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    sort         INTEGER NOT NULL DEFAULT 0,
    kind         TEXT NOT NULL,
    values_json  TEXT NOT NULL DEFAULT '[]',
    outbound_tag TEXT NOT NULL,
    remark       TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_routing_server ON routing_rules(server_id);
`

// migrationV9SQL 是套餐与兑换码。
const migrationV9SQL = `
CREATE TABLE IF NOT EXISTS packages (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    description   TEXT NOT NULL DEFAULT '',
    traffic_limit INTEGER NOT NULL DEFAULT 0,   -- 字节，0 = 不限
    duration_days INTEGER NOT NULL DEFAULT 0,   -- 0 = 不过期
    price_cents   INTEGER NOT NULL DEFAULT 0,
    currency      TEXT NOT NULL DEFAULT 'CNY',
    max_nodes     INTEGER NOT NULL DEFAULT 0,
    max_servers   INTEGER NOT NULL DEFAULT 0,
    sort          INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS redeem_codes (
    id            TEXT PRIMARY KEY,
    code          TEXT NOT NULL UNIQUE,
    package_id    TEXT NOT NULL DEFAULT '',
    traffic_bytes INTEGER NOT NULL DEFAULT 0,
    duration_days INTEGER NOT NULL DEFAULT 0,
    max_uses      INTEGER NOT NULL DEFAULT 1,
    used_count    INTEGER NOT NULL DEFAULT 0,
    expires_at    INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_redeem_code ON redeem_codes(code);

ALTER TABLE users ADD COLUMN package_id TEXT NOT NULL DEFAULT '';
`

// migrationV8SQL 是订阅模板 / 规则集 / 订阅文件。
const migrationV8SQL = `
CREATE TABLE IF NOT EXISTS rule_templates (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL,
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rule_providers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL DEFAULT 'http',  -- http | file | inline
    behavior   TEXT NOT NULL DEFAULT 'classical',
    format     TEXT NOT NULL DEFAULT 'yaml',
    url        TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL DEFAULT '',
    interval   INTEGER NOT NULL DEFAULT 86400,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS subscribe_files (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    content    TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- 订阅时选用的模板（空表示用内置常用模板）
ALTER TABLE users ADD COLUMN template_id TEXT NOT NULL DEFAULT '';
`

// migrationV7SQL 是公告。
const migrationV7SQL = `
CREATE TABLE IF NOT EXISTS announcements (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL DEFAULT '',
    level      TEXT NOT NULL DEFAULT 'info',   -- info | warning | important
    enabled    INTEGER NOT NULL DEFAULT 1,
    pinned     INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_announcements_enabled ON announcements(enabled, created_at);
`

// migrationV6SQL 是通用设置表与服务器展示字段（探针/品牌等都要用）。
const migrationV6SQL = `
-- 通用键值设置。品牌、探针、通知等配置都落这里，避免每加一个功能就加一张表。
CREATE TABLE IF NOT EXISTS system_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

-- 服务器展示信息（探针页与列表页用）
ALTER TABLE servers ADD COLUMN region             TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN region_country     TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN region_city        TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN provider_name      TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN provider_url       TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN telecom_paid_peer  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN traffic_limit      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN period_start_day   INTEGER NOT NULL DEFAULT 1;
ALTER TABLE servers ADD COLUMN probe_visible      INTEGER NOT NULL DEFAULT 1;
ALTER TABLE servers ADD COLUMN weight             INTEGER NOT NULL DEFAULT 0;
`

// migrationV5SQL 是每用户日流量。
const migrationV5SQL = `
CREATE TABLE IF NOT EXISTS user_traffic_daily (
    day     TEXT NOT NULL,
    user_id TEXT NOT NULL,
    up      INTEGER NOT NULL DEFAULT 0,
    down    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, user_id)
);
CREATE INDEX IF NOT EXISTS idx_user_traffic_user ON user_traffic_daily(user_id);
`

// migrationV4SQL 是接口令牌与双因子。
const migrationV4SQL = `
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL UNIQUE,
    scopes       TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    revoked_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);

ALTER TABLE users ADD COLUMN totp_enabled        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_pending_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN recovery_codes      TEXT NOT NULL DEFAULT '';
`

// migrationV3SQL 是任务持久化。
const migrationV3SQL = `
CREATE TABLE IF NOT EXISTS pending_tasks (
    id            TEXT PRIMARY KEY,
    server_id     TEXT NOT NULL,
    kind          TEXT NOT NULL,
    args          TEXT NOT NULL DEFAULT '{}',
    nonce         TEXT NOT NULL DEFAULT '',
    signature     TEXT NOT NULL DEFAULT '',
    deadline_ms   INTEGER NOT NULL DEFAULT 60000,
    actor         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'pending',
    attempts      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    dispatched_at INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0,
    output        TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_tasks_server_status ON pending_tasks(server_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_created ON pending_tasks(created_at);
`

// migrationV2 返回入站 / 节点 / 流量与服务端可选字段的迁移。
// 单独拆出来是为了新版 gofmt 下长 SQL 字面量更易读。
func init() {
	migrations = append(migrations, migration{
		Version: 2,
		Name:    "inbounds_nodes_traffic",
		SQL:     migrationV2SQL,
	})
	migrations = append(migrations, migration{
		Version: 3,
		Name:    "pending_tasks",
		SQL:     migrationV3SQL,
	})
	migrations = append(migrations, migration{
		Version: 4,
		Name:    "api_tokens_and_totp",
		SQL:     migrationV4SQL,
	})
	migrations = append(migrations, migration{
		Version: 5,
		Name:    "user_traffic_daily",
		SQL:     migrationV5SQL,
	})
	migrations = append(migrations, migration{
		Version: 6,
		Name:    "system_settings_and_server_display",
		SQL:     migrationV6SQL,
	})
	migrations = append(migrations, migration{
		Version: 7,
		Name:    "announcements",
		SQL:     migrationV7SQL,
	})
	migrations = append(migrations, migration{
		Version: 8,
		Name:    "rule_templates_providers_subscribe_files",
		SQL:     migrationV8SQL,
	})
	migrations = append(migrations, migration{
		Version: 9,
		Name:    "packages_and_redeem_codes",
		SQL:     migrationV9SQL,
	})
	migrations = append(migrations, migration{
		Version: 10,
		Name:    "outbounds_and_routing_rules",
		SQL:     migrationV10SQL,
	})
}

const migrationV2SQL = `
-- ---------- 入站（一台服务器上的一个监听端口） ----------
CREATE TABLE IF NOT EXISTS inbounds (
    id          TEXT PRIMARY KEY,
    server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    protocol    TEXT NOT NULL,                      -- vless | vmess | trojan | shadowsocks
    transport   TEXT NOT NULL DEFAULT 'tcp',        -- tcp | ws | grpc | httpupgrade
    security    TEXT NOT NULL DEFAULT 'none',       -- none | tls | reality
    port        INTEGER NOT NULL,
    listen      TEXT NOT NULL DEFAULT '0.0.0.0',
    settings    TEXT NOT NULL DEFAULT '{}',         -- 协议参数（clients 由 inbound_clients 派生）
    stream      TEXT NOT NULL DEFAULT '{}',         -- 传输 / 安全层参数
    sniffing    TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    remark      TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(server_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_inbounds_server ON inbounds(server_id);

-- ---------- 入站下的客户端凭据（一个用户一条） ----------
CREATE TABLE IF NOT EXISTS inbound_clients (
    id          TEXT PRIMARY KEY,
    inbound_id  TEXT NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    uuid        TEXT NOT NULL,
    email       TEXT NOT NULL DEFAULT '',
    flow        TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    UNIQUE(inbound_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_clients_user ON inbound_clients(user_id);
CREATE INDEX IF NOT EXISTS idx_clients_inbound ON inbound_clients(inbound_id);

-- ---------- 节点（入站的展示层：名字 / 地区 / 排序） ----------
CREATE TABLE IF NOT EXISTS nodes (
    id          TEXT PRIMARY KEY,
    inbound_id  TEXT NOT NULL UNIQUE REFERENCES inbounds(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    region      TEXT NOT NULL DEFAULT '',
    tags        TEXT NOT NULL DEFAULT '',
    sort        INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- ---------- 日流量聚合 ----------
CREATE TABLE IF NOT EXISTS traffic_daily (
    day         TEXT NOT NULL,                      -- YYYY-MM-DD（主控本地时区）
    server_id   TEXT NOT NULL,
    up          INTEGER NOT NULL DEFAULT 0,
    down        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, server_id)
);

-- ---------- 用户订阅凭据与配额 ----------
ALTER TABLE users ADD COLUMN sub_token    TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN traffic_limit INTEGER NOT NULL DEFAULT 0;   -- 字节，0 = 不限
ALTER TABLE users ADD COLUMN traffic_used  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN expire_at     INTEGER NOT NULL DEFAULT 0;   -- unix ms，0 = 不过期
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_sub_token ON users(sub_token) WHERE sub_token != '';
`
