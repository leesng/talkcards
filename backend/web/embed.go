// Package web embeds the frontend static assets for single-binary distribution.
package web

import "embed"

// Assets holds everything under static/ (incl. the js/mySocket.js client).
//
//go:embed all:static
var Assets embed.FS
