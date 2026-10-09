// Package web embeds the built UI (web/dist) into the panel binary.
package web

import "embed"

// Dist holds the Vite build output. Run `make web` before building the panel;
// dist/.gitkeep keeps the embed valid before the first UI build.
//
//go:embed all:dist
var Dist embed.FS
