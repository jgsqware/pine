// Package web embeds the Pine single-page UI served by the API server.
package web

import "embed"

// FS contains the static UI assets.
//
//go:embed *
var FS embed.FS
