package xray

import (
	"bytes"
	"encoding/json"

	"github.com/mmx-project/mmx/pkg/agentproto"
)

// marshalConfig 把强类型配置序列化成 Xray 可读的 JSON。
//
// 不用 json.MarshalIndent 的默认转义，避免把 URL 里的 & 变成 \u0026 —— Xray 本身能读，
// 但会让人工排查时难以比对。
func marshalConfig(cfg *agentproto.XrayConfig) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
