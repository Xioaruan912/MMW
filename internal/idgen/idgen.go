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
