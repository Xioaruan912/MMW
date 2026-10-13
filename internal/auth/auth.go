// Package auth 提供口令散列、令牌生成与会话校验。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength 是管理员/用户口令的最小长度。
const MinPasswordLength = 10

// HashPassword 用 bcrypt 生成口令散列。
func HashPassword(plain string) (string, error) {
	if len(plain) < MinPasswordLength {
		return "", fmt.Errorf("口令至少 %d 位", MinPasswordLength)
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 校验口令。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// TokenBytes 是各类令牌的随机熵长度。
const TokenBytes = 32

// GenerateToken 生成 32 字节随机令牌（base64url 无填充）。
// 返回值是**明文**，只在生成的这一刻存在；落库的永远是 HashToken 的结果。
func GenerateToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken 返回令牌的 sha256 十六进制摘要，用于落库与查找。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ErrUnauthorized 表示未通过鉴权。
var ErrUnauthorized = errors.New("未授权")

// RoleAllowed 判断角色是否在允许集合内。
func RoleAllowed(role string, allowed ...string) bool {
	for _, a := range allowed {
		if role == a {
			return true
		}
	}
	return false
}
