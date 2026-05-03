// Package assets bundles the broadcast-style PNG plates that internal/video
// composites into each frame. Files are produced by ./cmd/gen-assets via the
// Vercel AI Gateway image endpoint and committed alongside this file so the
// binary is self-contained — no network access at runtime.
//
// If you ever need to ship without real assets (e.g. CI), the placeholder 1×1
// PNGs in this directory satisfy the embed pattern and the renderer detects
// them as "no asset" and falls back to its procedural background.
package assets

import "embed"

//go:embed *.png
var FS embed.FS
