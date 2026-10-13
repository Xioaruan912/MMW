// Package version 保存构建期注入的版本信息。
package version

import "fmt"

// 这些变量通过 -ldflags "-X ..." 在构建时注入，见 Makefile。
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// String 返回一行可读的版本描述。
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuildTime)
}
