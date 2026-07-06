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
# Stage 3 — PPTX renderer  #
############################
FROM node:22-bookworm-slim AS ppt-renderer
WORKDIR /src/tools/ppt-renderer
COPY tools/ppt-renderer/package.json tools/ppt-renderer/package-lock.json ./
RUN npm ci --omit=dev
COPY tools/ppt-renderer/render.mjs ./

############################
# Stage 4 — CJK font       #
############################
# The video renderer picks its font via loadFontSources (internal/video/
# render.go): $DEBATE_BOT_FONT → known system CJK fonts → embedded Latin-only
# Go fonts. The slim runtime image ships no CJK font, so without this stage
# Chinese text silently degrades to missing glyphs in generated video.
# Reuse the checksum-pinned NotoSansSC the style golden tests render with, so
# production output matches the goldens.
FROM debian:bookworm-slim AS cjk-font
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY scripts/fetch-style-font.sh scripts/fetch-style-font.sh
RUN bash scripts/fetch-style-font.sh
# -> /src/internal/video/testdata/fonts/NotoSansSC-Regular.otf

############################
# Stage 5 — runtime image  #
############################
# ffmpeg + ffplay are required on PATH (audio.VerifyTools); the Debian ffmpeg
# package provides both. fonts-noto-cjk covers Chinese/Japanese/Korean text in
# LibreOffice slide rendering (fonts-dejavu alone has no CJK glyphs).
FROM node:22-bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg ca-certificates libreoffice-impress fonts-dejavu fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=backend /out/debate-bot /usr/local/bin/debate-bot
COPY --from=ppt-renderer /src/tools/ppt-renderer ./tools/ppt-renderer
# --chmod: the fetch script leaves the font 0600 (mktemp default); world-
# readable so the renderer still finds it when the container runs as non-root.
COPY --from=cjk-font --chmod=644 /src/internal/video/testdata/fonts/NotoSansSC-Regular.otf \
    /usr/share/fonts/opentype/noto-sans-sc/NotoSansSC-Regular.otf
RUN fc-cache -f

EXPOSE 3000
ENV OUT_DIR=/app/out
ENV PPTX_RENDERER_SCRIPT=/app/tools/ppt-renderer/render.mjs
# Pin the Go video renderer to the exact font the style goldens were rendered
# with, so production frames match the committed goldens pixel-for-pixel. The
# fonts-noto-cjk NotoSansCJK-Regular.ttc above doubles as the fallback (it's
# the first Linux candidate in loadFontSources) if this pin ever breaks.
ENV DEBATE_BOT_FONT=/usr/share/fonts/opentype/noto-sans-sc/NotoSansSC-Regular.otf

# Default: stream mode. Override the args to switch to video mode, e.g.
#   docker run ... debate-bot server --mode video --addr :3000
ENTRYPOINT ["debate-bot"]
CMD ["server", "--channel", "./channels/channels.json", "--content", "./topics/*.md", "--addr", ":3000"]
