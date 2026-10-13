package agentproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NonceBytes 是任务 nonce 的长度。
const NonceBytes = 16

// NewNonce 生成一个随机 nonce（base64 无填充）。
func NewNonce() (string, error) {
	b := make([]byte, NonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TaskSigningInput 是任务签名的规范输入，双端必须完全一致。
//
// 格式：v1|task_id|kind|args_digest|nonce
func TaskSigningInput(taskID, kind, argsDigest, nonce string) []byte {
	return []byte(strings.Join([]string{"v1", taskID, kind, argsDigest, nonce}, "|"))
}

// SignTask 用主控私钥给任务签名（仅在 master 侧调用）。
func SignTask(priv ed25519.PrivateKey, t *TaskPayload) error {
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("非法的主控私钥")
	}
	input := TaskSigningInput(t.TaskID, t.Kind, t.Args.Digest(), t.Nonce)
	sig := ed25519.Sign(priv, input)
	t.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return nil
}

// VerifyTask 用主控公钥校验任务签名（仅在 agent 侧调用）。
func VerifyTask(pub ed25519.PublicKey, t *TaskPayload) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("非法的主控公钥")
	}
	if t.Signature == "" {
		return errors.New("任务缺少签名")
	}
	sig, err := base64.RawURLEncoding.DecodeString(t.Signature)
	if err != nil {
		return fmt.Errorf("签名不是合法 base64: %w", err)
	}
	input := TaskSigningInput(t.TaskID, t.Kind, t.Args.Digest(), t.Nonce)
	if !ed25519.Verify(pub, input, sig) {
		return errors.New("任务签名校验失败")
	}
	return nil
}

// GenerateMasterKey 生成主控 Ed25519 密钥对。
func GenerateMasterKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// LoadOrCreateMasterKey 从 path 读取主密钥，不存在则生成并落盘（0600）。
// 返回 (公钥, 私钥, 是否新建)。
func LoadOrCreateMasterKey(path string) (ed25519.PublicKey, ed25519.PrivateKey, bool, error) {
	if data, err := os.ReadFile(path); err == nil {
		raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, nil, false, fmt.Errorf("主密钥文件格式错误（应为 hex）: %w", err)
		}
		switch len(raw) {
		case ed25519.PrivateKeySize:
			priv := ed25519.PrivateKey(raw)
			return priv.Public().(ed25519.PublicKey), priv, false, nil
		case ed25519.SeedSize:
			priv := ed25519.NewKeyFromSeed(raw)
			return priv.Public().(ed25519.PublicKey), priv, false, nil
		default:
			return nil, nil, false, fmt.Errorf("主密钥长度非法: %d 字节", len(raw))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, err
	}

	pub, priv, err := GenerateMasterKey()
	if err != nil {
		return nil, nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, false, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		return nil, nil, false, err
	}
	return pub, priv, true, nil
}

// EncodePublicKey / DecodePublicKey 用于把主控公钥放进 hello_ack。
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}

// DecodePublicKey 解析 base64 公钥。
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("公钥长度非法: %d", len(b))
	}
	return ed25519.PublicKey(b), nil
}

// ConstantTimeEqual 做常量时间比较（用于 token 校验）。
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
