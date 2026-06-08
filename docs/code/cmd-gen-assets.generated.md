---
slug: code/cmd/gen-assets
title: Package cmd/gen-assets
description: Auto-generated go doc reference for the cmd/gen-assets package.
---

# Package `cmd/gen-assets`

_Generated with `go doc -all ./cmd/gen-assets`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command gen-assets calls the Vercel AI Gateway image-generation endpoint
(OpenAI-compatible shape at /v1/images/generations) to produce the static PNG
assets that internal/video embeds and composites into each frame.

Run once when you want to refresh the look:

    export AI_GATEWAY_API_KEY=...
    go run ./cmd/gen-assets            # writes internal/video/assets/*.png
    go run ./cmd/gen-assets --only bg  # regenerate just the background

Each asset is generated at the API's native size (1024 / 1536), then resampled
with Catmull-Rom to its on-frame target so the embedded PNG is already the right
shape — the renderer only does compositing, not scaling.
```
