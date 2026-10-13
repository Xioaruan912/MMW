// Package web 只做一件事：把构建好的前端产物嵌进主控二进制。
//
// 为什么把 Go 文件放在前端目录里：Go 的 //go:embed 只能引用**本包目录及其子目录**，
// 无法向上引用 ../../web/dist。把 embed 声明放在这里是最省事且不破坏结构的方式。
package web

import "embed"

// Dist 是前端的构建产物。
//
// 仓库里保留了 dist/index.html 作为占位（未构建时的提示页），
// 因此全新 clone 后 `go build ./...` 不会因为缺少文件而失败。
// 执行 `npm run build` 后会被真实产物覆盖。
//
//go:embed all:dist
var Dist embed.FS
