// Package web 嵌入前端静态资源，实现单二进制分发。
package web

import "embed"

// Assets 包含 static/ 前端全部资源（含 js/mySocket.js 通信层）。
//
//go:embed all:static
var Assets embed.FS
