package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web-dist
var webFS embed.FS

// staticHandler serves the embedded SPA built by the Vite frontend
// (see frontend/vite.config.ts → build.outDir = ../internal/server/web-dist).
// Run `make build` (or `cd frontend && bun run build`) to regenerate web-dist
// before `go build`.
func staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web-dist")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
