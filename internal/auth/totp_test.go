package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestGenerateTOTPSecret(t *testing.T) {
	secret, url, err := GenerateTOTPSecret("alice")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if secret == "" {
		t.Fatal("secret 为空")
	}
	// 必须是合法 base32（客户端要用它算码）
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret)); err != nil {
		t.Fatalf("secret 不是合法 base32: %v", err)
	}
	if !strings.HasPrefix(url, "otpauth://totp/") {
		t.Fatalf("URL 格式错误: %s", url)
	}
	if !strings.Contains(url, "issuer=MMX") {
		t.Fatalf("URL 缺少 issuer: %s", url)
	}
	if !strings.Contains(url, "alice") {
		t.Fatalf("URL 缺少账号名: %s", url)
	}

	// 两次生成必须不同
	secret2, _, _ := GenerateTOTPSecret("alice")
	if secret == secret2 {
		t.Fatal("两次生成的密钥不应相同")
	}
}

func TestVerifyTOTPAcceptsCurrentCode(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("bob")
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCodeCustom(secret, time.Now().UTC(), totp.ValidateOpts{
		Period: TOTPPeriod, Skew: TOTPSkew, Digits: TOTPDigits,
	})
	if err != nil {
		t.Fatalf("生成验证码失败: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("验证码应为 6 位，得到 %q", code)
	}
	if !VerifyTOTP(secret, code) {
		t.Fatal("当前验证码应当通过")
	}
}

func TestVerifyTOTPRejectsBadInput(t *testing.T) {
	secret, _, _ := GenerateTOTPSecret("carol")
	cases := []struct {
		name   string
		secret string
		code   string
	}{
		{"错误验证码", secret, "000000"},
		{"空验证码", secret, ""},
		{"位数不足", secret, "12345"},
		{"位数过多", secret, "1234567"},
		{"非数字", secret, "abcdef"},
		{"空密钥", "", "123456"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if VerifyTOTP(c.secret, c.code) {
				t.Fatal("应当拒绝")
			}
		})
	}
}

func TestRecoveryCodesAreUniqueAndHashed(t *testing.T) {
	plain, hashes, err := GenerateRecoveryCodes(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 8 || len(hashes) != 8 {
		t.Fatalf("数量不符: %d/%d", len(plain), len(hashes))
	}
	seen := map[string]bool{}
	for i, p := range plain {
		if seen[p] {
			t.Fatal("恢复码重复")
		}
		seen[p] = true
		// 明文形如 XXXXX-XXXXX-XXXXX-XXXXX
		if len(p) != 23 || strings.Count(p, "-") != 3 {
			t.Fatalf("恢复码格式错误: %q", p)
		}
		// 落库的必须是 hash，不能是明文
		if hashes[i] == p {
			t.Fatal("落库的恢复码不能是明文")
		}
		if hashes[i] != HashToken(normalizeRecovery(p)) {
			t.Fatal("恢复码摘要算法不一致")
		}
	}
}

func TestMatchRecoveryCodeConsumes(t *testing.T) {
	plain, hashes, _ := GenerateRecoveryCodes(3)

	// 忽略大小写与连字符
	remaining, ok := MatchRecoveryCode(hashes, strings.ToLower(strings.ReplaceAll(plain[1], "-", "")))
	if !ok {
		t.Fatal("忽略格式差异后应当命中")
	}
	if len(remaining) != 2 {
		t.Fatalf("命中后应剩 2 个，得到 %d", len(remaining))
	}
	// 已用掉的那个不能再命中
	if _, ok := MatchRecoveryCode(remaining, plain[1]); ok {
		t.Fatal("已消费的恢复码不应再次命中")
	}
	// 其他码仍可用
	if _, ok := MatchRecoveryCode(remaining, plain[0]); !ok {
		t.Fatal("未使用的恢复码应当仍然可用")
	}
	// 不存在的码
	if _, ok := MatchRecoveryCode(hashes, "AAAAA-BBBBB-CCCCC-DDDDD"); ok {
		t.Fatal("不存在的恢复码不应命中")
	}
}

func TestGenerateAPIKeyFormat(t *testing.T) {
	k1, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := GenerateAPIKey()
	if k1 == k2 {
		t.Fatal("两次生成的接口令牌不应相同")
	}
	if !strings.HasPrefix(k1, "mmx_") {
		t.Fatalf("接口令牌应以 mmx_ 开头: %s", k1)
	}
	if HashToken(k1) == k1 {
		t.Fatal("HashToken 不应当返回明文")
	}
}

func TestConstantTimeEqualString(t *testing.T) {
	if !ConstantTimeEqualString("abc", "abc") {
		t.Fatal("相同字符串应判等")
	}
	if ConstantTimeEqualString("abc", "abd") || ConstantTimeEqualString("abc", "ab") {
		t.Fatal("不同字符串不应判等")
	}
}
