package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyPackageSetsQuotaAndExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, err := st.CreateUser(ctx, "alice", "hash", "", "", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.CreatePackage(ctx, Package{
		Name: "月度 100G", TrafficLimit: 100 << 30, DurationDays: 30, PriceCents: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.ApplyPackage(ctx, u.ID, pkg.ID, ApplyPackageOptions{ResetTraffic: true})
	if err != nil {
		t.Fatalf("绑定套餐失败: %v", err)
	}
	if got.PackageID != pkg.ID {
		t.Fatalf("package_id 未设置: %q", got.PackageID)
	}
	if got.TrafficLimit != 100<<30 {
		t.Fatalf("流量上限错误: %d", got.TrafficLimit)
	}
	if got.TrafficUsed != 0 {
		t.Fatalf("ResetTraffic 时应清零已用流量: %d", got.TrafficUsed)
	}
	wantExpire := time.Now().AddDate(0, 0, 30).UnixMilli()
	if diff := got.ExpireAt - wantExpire; diff > 60_000 || diff < -60_000 {
		t.Fatalf("到期时间偏差过大: %d vs %d", got.ExpireAt, wantExpire)
	}
}

func TestApplyPackageExtendsExistingExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, _ := st.CreateUser(ctx, "bob", "hash", "", "", RoleUser)
	pkg, _ := st.CreatePackage(ctx, Package{Name: "月卡", DurationDays: 30})

	// 先设一个未来到期时间
	future := time.Now().AddDate(0, 0, 10)
	u.ExpireAt = future.UnixMilli()
	if err := st.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	// 默认续期：应从原到期时间顺延
	got, err := st.ApplyPackage(ctx, u.ID, pkg.ID, ApplyPackageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := future.AddDate(0, 0, 30).UnixMilli()
	if diff := got.ExpireAt - want; diff > 60_000 || diff < -60_000 {
		t.Fatalf("续期应从原到期时间顺延，实得 %d 期望约 %d", got.ExpireAt, want)
	}

	// ExtendFromNow：从当前时间起算
	got2, err := st.ApplyPackage(ctx, u.ID, pkg.ID, ApplyPackageOptions{ExtendFromNow: true})
	if err != nil {
		t.Fatal(err)
	}
	want2 := time.Now().AddDate(0, 0, 30).UnixMilli()
	if diff := got2.ExpireAt - want2; diff > 60_000 || diff < -60_000 {
		t.Fatalf("ExtendFromNow 应从当前时间起算，实得 %d 期望约 %d", got2.ExpireAt, want2)
	}
}

func TestApplyPackageZeroDurationMeansNoExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "carol", "hash", "", "", RoleUser)
	pkg, _ := st.CreatePackage(ctx, Package{Name: "永久", TrafficLimit: 10 << 30, DurationDays: 0})

	got, err := st.ApplyPackage(ctx, u.ID, pkg.ID, ApplyPackageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpireAt != 0 {
		t.Fatalf("时长为 0 应表示不过期，实得 %d", got.ExpireAt)
	}
}

func TestCreatePackageRejectsNegative(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreatePackage(ctx, Package{Name: "bad", TrafficLimit: -1}); err == nil {
		t.Fatal("负流量必须被拒")
	}
	if _, err := st.CreatePackage(ctx, Package{Name: "bad2", DurationDays: -5}); err == nil {
		t.Fatal("负时长必须被拒")
	}
}

func TestRedeemCodeSingleUse(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u1, _ := st.CreateUser(ctx, "u1", "hash", "", "", RoleUser)
	u2, _ := st.CreateUser(ctx, "u2", "hash", "", "", RoleUser)

	code, err := st.CreateRedeemCode(ctx, RedeemCode{TrafficBytes: 5 << 30, DurationDays: 7, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if code.Code == "" {
		t.Fatal("应自动生成兑换码")
	}
	// 兑换码应带连字符便于抄写
	if len(code.Code) != 19 {
		t.Fatalf("兑换码格式异常（期望 4-4-4-4）: %q", code.Code)
	}

	if _, _, err := st.Redeem(ctx, code.Code, u1.ID); err != nil {
		t.Fatalf("首次兑换失败: %v", err)
	}
	// 第二次必须失败（MaxUses=1）
	if _, _, err := st.Redeem(ctx, code.Code, u2.ID); !errors.Is(err, ErrRedeemInvalid) {
		t.Fatalf("超出使用次数应返回 ErrRedeemInvalid，实得 %v", err)
	}

	// 首次兑换的权益应已生效
	got, _ := st.GetUser(ctx, u1.ID)
	if got.TrafficLimit != 5<<30 {
		t.Fatalf("流量未派发: %d", got.TrafficLimit)
	}
	if got.ExpireAt == 0 {
		t.Fatal("到期时间未派发")
	}
}

func TestRedeemCodeExpired(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "u", "hash", "", "", RoleUser)

	code, _ := st.CreateRedeemCode(ctx, RedeemCode{
		TrafficBytes: 1 << 30,
		ExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(),
	})
	if _, _, err := st.Redeem(ctx, code.Code, u.ID); !errors.Is(err, ErrRedeemInvalid) {
		t.Fatalf("过期兑换码应被拒，实得 %v", err)
	}
}

func TestRedeemUnknownCode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "u", "hash", "", "", RoleUser)
	if _, _, err := st.Redeem(ctx, "AAAA-BBBB-CCCC-DDDD", u.ID); !errors.Is(err, ErrRedeemInvalid) {
		t.Fatalf("不存在的兑换码应返回 ErrRedeemInvalid，实得 %v", err)
	}
}

func TestRedeemMultiUse(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	code, _ := st.CreateRedeemCode(ctx, RedeemCode{TrafficBytes: 1 << 30, MaxUses: 3})

	for i := 0; i < 3; i++ {
		u, _ := st.CreateUser(ctx, "u"+string(rune('a'+i)), "hash", "", "", RoleUser)
		if _, _, err := st.Redeem(ctx, code.Code, u.ID); err != nil {
			t.Fatalf("第 %d 次兑换失败: %v", i+1, err)
		}
	}
	u4, _ := st.CreateUser(ctx, "u4", "hash", "", "", RoleUser)
	if _, _, err := st.Redeem(ctx, code.Code, u4.ID); !errors.Is(err, ErrRedeemInvalid) {
		t.Fatal("第 4 次应超出 MaxUses 被拒")
	}

	// 使用次数应被正确记录
	list, _ := st.ListRedeemCodes(ctx, 10)
	if len(list) != 1 || list[0].UsedCount != 3 {
		t.Fatalf("使用次数记录错误: %+v", list)
	}
}

func TestRedeemWithPackageOverridesQuota(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "u", "hash", "", "", RoleUser)
	pkg, _ := st.CreatePackage(ctx, Package{Name: "季卡", TrafficLimit: 300 << 30, DurationDays: 90})

	code, _ := st.CreateRedeemCode(ctx, RedeemCode{PackageID: pkg.ID, MaxUses: 1})
	got, _, err := st.Redeem(ctx, code.Code, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PackageID != pkg.ID {
		t.Fatalf("套餐未绑定: %q", got.PackageID)
	}
	if got.TrafficLimit != 300<<30 {
		t.Fatalf("套餐流量未生效: %d", got.TrafficLimit)
	}
	want := time.Now().AddDate(0, 0, 90).UnixMilli()
	if diff := got.ExpireAt - want; diff > 60_000 || diff < -60_000 {
		t.Fatalf("套餐时长未生效: %d", got.ExpireAt)
	}
}

func TestRandomCodeAvoidsAmbiguousChars(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c, err := randomCode(16)
		if err != nil {
			t.Fatal(err)
		}
		if seen[c] {
			t.Fatal("兑换码出现重复")
		}
		seen[c] = true
		for _, ch := range c {
			if ch == '-' {
				continue
			}
			if ch == '0' || ch == 'O' || ch == '1' || ch == 'I' || ch == 'L' {
				t.Fatalf("兑换码不应包含易混淆字符 %q: %s", ch, c)
			}
		}
	}
}
