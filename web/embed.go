// Package web embeds the built dashboard bundle (M4 Task 4).
//
// From a clean checkout dist/ holds only .gitkeep: build artifacts are
// not committed; `make build` runs the Vite build first (node + npm,
// versions pinned in .nvmrc / package-lock.json) so the binary always
// embeds a fresh bundle. Without the bundle the hub serves an explainer
// instead of the dashboard. Everything the dashboard needs ships inside
// the bundle — the Jost webfont is self-hosted (woff2 fingerprinted into
// the bundle, M7), echarts is vendored by the bundler — so serving it
// makes no external requests (verified by TestDistNoExternalOrigins after
// every web build).
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
