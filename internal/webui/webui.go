// Package webui 暴露内嵌的前端文件系统。
package webui

import (
	"io/fs"

	web "github.com/mmx-project/mmx/web"
)

// FS 返回前端资源（已剥离 dist/ 前缀）；出错时返回 nil，调用方应回落到仅 API 模式。
func FS() fs.FS {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
