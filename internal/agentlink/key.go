package agentlink

import (
	"crypto/ed25519"

	"github.com/mmx-project/mmx/pkg/agentproto"
)

// LoadOrCreateMasterKeyIfNeeded 加载（或首次生成）主控签名密钥。
func LoadOrCreateMasterKeyIfNeeded(path string) (ed25519.PublicKey, ed25519.PrivateKey, bool, error) {
	return agentproto.LoadOrCreateMasterKey(path)
}

// EncodePublicKeyShort 返回便于日志展示的短公钥。
func EncodePublicKeyShort(pub ed25519.PublicKey) string {
	s := agentproto.EncodePublicKey(pub)
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}
