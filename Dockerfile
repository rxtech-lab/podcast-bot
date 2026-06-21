# syntax=docker/dockerfile:1

############################
# Stage 1 — build the SPA  #
############################
# Vite writes its output to ../internal/server/web-dist (see frontend/vite.config.ts),
# so the whole repo layout must be present for the relative outDir to resolve.
FROM oven/bun:1 AS frontend
WORKDIR /src
COPY frontend/package.json frontend/bun.lock* frontend/
RUN cd frontend && bun install --frozen-lockfile || (cd frontend && bun install)
COPY . .
RUN cd frontend && bun run build
# -> /src/internal/server/web-dist

############################
# Stage 2 — build the Go binary
############################
# CGO is required: the SQLite driver (mattn/go-sqlite3) is a CGO package.
FROM golang:1.25-bookworm AS backend
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pull in the freshly built, embedded SPA from stage 1.
COPY --from=frontend /src/internal/server/web-dist ./internal/server/web-dist
ENV CGO_ENABLED=1
RUN go build -o /out/debate-bot ./cmd/debate-bot

############################
# Stage 3 — runtime image  #
############################
# ffmpeg + ffplay are required on PATH (audio.VerifyTools); the Debian ffmpeg
# package provides both.
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=backend /out/debate-bot /usr/local/bin/debate-bot

EXPOSE 3000
ENV OUT_DIR=/app/out

# Default: stream mode. Override the args to switch to video mode, e.g.
#   docker run ... debate-bot server --mode video --addr :3000
ENTRYPOINT ["debate-bot"]
CMD ["server", "--channel", "./channels/channels.json", "--content", "./topics/*.md", "--addr", ":3000"]
