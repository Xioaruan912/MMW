package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore 在临时目录建一个真实 SQLite 库并跑完迁移。
// 用真库而不是 mock：迁移语句、ON CONFLICT、事务语义都只有真库才能验证。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return st
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, err := Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	st1, err := New(ctx, db)
	if err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 同一个库再迁移一次不应报错（迁移表会跳过已应用项）
	if _, err := New(ctx, db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	// 关键表必须存在
	for _, table := range []string{
		"users", "sessions", "servers", "agent_tokens", "server_metrics",
		"audit_logs", "inbounds", "inbound_clients", "nodes", "traffic_daily",
		"pending_tasks", "api_tokens", "user_traffic_daily",
	} {
		var n int
		if err := st1.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("表 %s 不可用: %v", table, err)
		}
	}
}

func TestAddServerTrafficAccumulates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	srv, err := st.CreateServer(ctx, "n1", "1.2.3.4", "", XrayModeExternal)
	if err != nil {
		t.Fatal(err)
	}

	// 同一天多次累加
	for i := 0; i < 3; i++ {
		if err := st.AddServerTraffic(ctx, srv.ID, 100, 200); err != nil {
			t.Fatalf("累加失败: %v", err)
		}
	}
	sum, err := st.ServerTrafficSummary(ctx, srv.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Up != 300 || sum.Down != 600 {
		t.Fatalf("累加结果错误: up=%d down=%d", sum.Up, sum.Down)
	}
	if sum.Total != 900 {
		t.Fatalf("总量错误: %d", sum.Total)
	}
	if len(sum.Series) != 1 {
		t.Fatalf("应只有 1 个数据点，得到 %d", len(sum.Series))
	}
	if sum.Series[0].Day != DayKey(time.Now()) {
		t.Fatalf("日期键错误: %s", sum.Series[0].Day)
	}
}

func TestAddServerTrafficZeroIsNoop(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	srv, _ := st.CreateServer(ctx, "n1", "", "", XrayModeExternal)

	if err := st.AddServerTraffic(ctx, srv.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	sum, _ := st.ServerTrafficSummary(ctx, srv.ID, 30)
	if len(sum.Series) != 0 {
		t.Fatalf("零增量不应写入记录，实得 %+v", sum.Series)
	}
}

func TestUserTrafficAlsoBumpsTotal(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, err := st.CreateUser(ctx, "alice", "hash", "Alice", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.AddUserTraffic(ctx, u.ID, 1024, 2048); err != nil {
		t.Fatalf("用户流量累加失败: %v", err)
	}
	if err := st.AddUserTraffic(ctx, u.ID, 1, 1); err != nil {
		t.Fatal(err)
	}

	// 日表
	sum, err := st.UserTrafficSummary(ctx, u.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Up != 1025 || sum.Down != 2049 {
		t.Fatalf("用户日表错误: %+v", sum)
	}

	// 总额（users.traffic_used）必须与日表同事务更新
	got, err := st.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(1025 + 2049)
	if got.TrafficUsed != want {
		t.Fatalf("用户总额错误: 期望 %d 实得 %d", want, got.TrafficUsed)
	}
}

func TestUserQuotaMath(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		limit, used int64
		expireAt    int64
		wantUnlim   bool
		wantExh     bool
		wantExpired bool
		wantRemain  int64
	}{
		{"不限量", 0, 5000, 0, true, false, false, -1},
		{"未用满", 10000, 3000, 0, false, false, false, 7000},
		{"刚好用满", 10000, 10000, 0, false, true, false, 0},
		{"超额", 10000, 12000, 0, false, true, false, 0},
		{"已过期", 10000, 100, time.Now().Add(-time.Hour).UnixMilli(), false, false, true, 9900},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 每个用例一个独立用户，避免互相影响
			u, err := st.CreateUser(ctx, "u"+string(rune('a'+i)), "hash", "", "", RoleUser)
			if err != nil {
				t.Fatal(err)
			}
			u.TrafficLimit = c.limit
			u.TrafficUsed = c.used
			u.ExpireAt = c.expireAt
			if err := st.UpdateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			q, err := st.GetUserQuota(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if q.Unlimited != c.wantUnlim || q.Exhausted != c.wantExh ||
				q.Expired != c.wantExpired || q.Remaining != c.wantRemain {
				t.Fatalf("配额判定错误: %+v（期望 unlimited=%v exhausted=%v expired=%v remain=%d）",
					q, c.wantUnlim, c.wantExh, c.wantExpired, c.wantRemain)
			}
		})
	}
}

func TestAllServersTrafficSummaryMergesDays(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	s1, _ := st.CreateServer(ctx, "a", "", "", XrayModeExternal)
	s2, _ := st.CreateServer(ctx, "b", "", "", XrayModeExternal)

	if err := st.AddServerTraffic(ctx, s1.ID, 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.AddServerTraffic(ctx, s2.ID, 50, 25); err != nil {
		t.Fatal(err)
	}

	sum, err := st.AllServersTrafficSummary(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Up != 150 || sum.Down != 125 {
		t.Fatalf("多服务器合并错误: %+v", sum)
	}
	if len(sum.Series) != 1 {
		t.Fatalf("同一天应合并为 1 个点，得到 %d", len(sum.Series))
	}
}
