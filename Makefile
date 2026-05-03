BINARY    := debate-bot
GO_PKG    := ./cmd/debate-bot
FRONTEND  := frontend
EMBED_DIR := internal/server/web-dist
BIN_DIR   := bin

.PHONY: all build frontend backend run dev clean tidy gen-assets

all: build

# Full production build: bundle the React SPA into the embed directory,
# then build the Go binary that embeds it.
build: frontend backend

frontend:
	cd $(FRONTEND) && bun install && bun run build

backend:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(GO_PKG)

run: build
	./$(BIN_DIR)/$(BINARY)

# Dev: run Vite (with /api proxy) and the Go server in parallel.
# Frontend hot-reloads on http://localhost:5173; Go serves the API on :8080.
# Override the proxy target with VITE_API_PROXY_TARGET if your Go server is
# bound elsewhere.
dev:
	@echo "starting Go server on :8080 and Vite on :5173"
	@( cd $(FRONTEND) && bun run dev ) & \
	  go run $(GO_PKG) server --addr :8080 ; \
	  kill %1 2>/dev/null || true

tidy:
	go mod tidy
	cd $(FRONTEND) && bun install

# Regenerate the embedded TV-studio plates via the Vercel AI Gateway image
# endpoint. Reads OPENAI_API_KEY (vck_…) from .env. Run when you want a fresh
# look — the resulting PNGs are committed under internal/video/assets/.
gen-assets:
	go run ./cmd/gen-assets

clean:
	rm -rf $(BIN_DIR) $(EMBED_DIR) $(FRONTEND)/dist $(FRONTEND)/node_modules
