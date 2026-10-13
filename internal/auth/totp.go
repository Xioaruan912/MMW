package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP 参数。
const (
	TOTPIssuer      = "MMX"
	TOTPPeriod      = 30
	TOTPSkew        = 1 // 容忍前后各一个时间窗，抵消客户端时钟漂移
	TOTPDigits      = otp.DigitsSix
	RecoveryCodeNum = 8
	RecoveryCodeLen = 10 // 以字节计的随机熵，转成 16 进制后 20 字符
)

// GenerateTOTPSecret 生成新的 TOTP 密钥与其 otpauth:// URL（供客户端扫码）。
func GenerateTOTPSecret(account string) (secret string, otpauthURL string, err error) {
	if account == "" {
		return "", "", errors.New("account 不能为空")
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: account,
		Period:      TOTPPeriod,
		SecretSize:  20, // 160 bit，RFC 4226 推荐
		Digits:      TOTPDigits,
	})
	if err != nil {
		return "", "", fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// VerifyTOTP 校验 6 位动态码。
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if secret == "" || len(code) != 6 {
		return false
	}
	ok, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    TOTPPeriod,
		Skew:      TOTPSkew,
		Digits:    TOTPDigits,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}

// GenerateRecoveryCodes 生成一次性恢复码。
//
// 返回明文（只展示一次）与对应的 sha256 摘要（落库）。
// 恢复码比动态码长，用 16 进制分组表示，便于用户抄写。
func GenerateRecoveryCodes(n int) (plain []string, hashes []string, err error) {
	if n <= 0 {
		n = RecoveryCodeNum
	}
	plain = make([]string, 0, n)
	hashes = make([]string, 0, n)
	for i := 0; i < n; i++ {
		b := make([]byte, RecoveryCodeLen)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		raw := strings.ToUpper(hex.EncodeToString(b)) // 20 字符
		formatted := raw[0:5] + "-" + raw[5:10] + "-" + raw[10:15] + "-" + raw[15:20]
		plain = append(plain, formatted)
		hashes = append(hashes, HashToken(normalizeRecovery(formatted)))
	}
	return plain, hashes, nil
}

// MatchRecoveryCode 在已存的摘要列表中查找匹配项。
// 命中时返回剩余摘要列表（该恢复码随即作废）。
func MatchRecoveryCode(hashes []string, input string) (remaining []string, ok bool) {
	want := HashToken(normalizeRecovery(input))
	for i, h := range hashes {
		if subtleEqual(h, want) {
			out := make([]string, 0, len(hashes)-1)
			out = append(out, hashes[:i]...)
			out = append(out, hashes[i+1:]...)
			return out, true
		}
	}
	return hashes, false
}

func normalizeRecovery(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.ReplaceAll(s, "-", "")
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// GenerateAPIKey 生成接口令牌明文（格式 mmx_<base64url>，便于识别与撤销）。
func GenerateAPIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "mmx_" + base64.RawURLEncoding.EncodeToString(b), nil
}
