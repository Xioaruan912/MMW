// Package idgen 生成带前缀的、按时间有序的标识符。
//
// 形如 srv_01J8ZC5N9K3QX7Y2V0M4B6T1PD —— 前缀便于人眼识别，ULID 保证时间有序且可排序。
package idgen

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// New 生成 <prefix>_<ulid> 形式的 ID。
func New(prefix string) string {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	if prefix == "" {
		return id.String()
	}
	return prefix + "_" + id.String()
}

// Server / Token / Task 等用到的固定前缀。
const (
	PrefixServer  = "srv"
	PrefixToken   = "tok"
	PrefixTask    = "tsk"
	PrefixUser    = "usr"
	PrefixSession = "ses"
	PrefixAudit   = "log"
	PrefixMetric  = "mtr"
	PrefixInbound = "inb"
	PrefixNode    = "nod"
	PrefixPackage = "pkg"
	PrefixCert    = "crt"
)

// NewUUID 生成 RFC 4122 v4 UUID（用于 Xray 客户端标识）。
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	const hexd = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[j] = '-'
			j++
		}
		out[j] = hexd[b[i]>>4]
		out[j+1] = hexd[b[i]&0x0f]
		j += 2
	}
	return string(out)
}
