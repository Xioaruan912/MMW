package probe

import (
	"testing"
	"time"
)

func TestDefaultSettingsIsSafeByDefault(t *testing.T) {
	st := DefaultSettings()
	if st.Enabled {
		t.Fatal("探针对外暴露，必须默认关闭")
	}
	if st.ShowName {
		t.Fatal("默认不应暴露服务器名称（最小信息披露）")
	}
	if st.RequireToken {
		t.Fatal("默认不要求令牌（同源访问即可）")
	}
	if st.RefreshSeconds <= 0 {
		t.Fatal("刷新间隔必须有默认值")
	}
}

func TestPeriodRangeMidMonthStart(t *testing.T) {
	// 起始日 = 5 号，now = 15 号 → 周期为 本月5号 ~ 下月5号
	now := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	start, end := PeriodRange(now, 5)
	if start.Format("2006-01-02") != "2026-03-05" {
		t.Fatalf("起点错误: %s", start)
	}
	if end.Format("2006-01-02") != "2026-04-05" {
		t.Fatalf("终点错误: %s", end)
	}
	if !(now.After(start) && now.Before(end)) {
		t.Fatal("now 应落在周期内")
	}
}

func TestPeriodRangeBeforeStartDay(t *testing.T) {
	// now = 3 号，起始日 = 5 号 → 周期为 上月5号 ~ 本月5号
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	start, end := PeriodRange(now, 5)
	if start.Format("2006-01-02") != "2026-02-05" {
		t.Fatalf("起点错误: %s", start)
	}
	if end.Format("2006-01-02") != "2026-03-05" {
		t.Fatalf("终点错误: %s", end)
	}
}

func TestPeriodRangeYearBoundary(t *testing.T) {
	// 1 月 2 日、起始日 5 号 → 上一年 12 月 5 日
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	start, end := PeriodRange(now, 5)
	if start.Format("2006-01-02") != "2025-12-05" {
		t.Fatalf("跨年起点错误: %s", start)
	}
	if end.Format("2006-01-02") != "2026-01-05" {
		t.Fatalf("跨年终点错误: %s", end)
	}
}

func TestPeriodRangeClampsInvalidStartDay(t *testing.T) {
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	// 0 -> 1，31 -> 28（避免 2 月没有 31 号导致周期错乱）
	if s, _ := PeriodRange(now, 0); s.Day() != 1 {
		t.Fatalf("startDay=0 应被夹到 1，实得 %d", s.Day())
	}
	if s, _ := PeriodRange(now, 31); s.Day() != 28 {
		t.Fatalf("startDay=31 应被夹到 28，实得 %d", s.Day())
	}
}

func TestPeriodRangeExactlyOnStartDay(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	start, end := PeriodRange(now, 5)
	if start.Format("2006-01-02") != "2026-03-05" {
		t.Fatalf("起始当日应属于新周期: %s", start)
	}
	if end.Format("2006-01-02") != "2026-04-05" {
		t.Fatalf("终点错误: %s", end)
	}
}
