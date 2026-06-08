---
slug: code/cmd/movement-smoke
title: Package cmd/movement-smoke
description: Auto-generated go doc reference for the cmd/movement-smoke package.
---

# Package `cmd/movement-smoke`

_Generated with `go doc -all ./cmd/movement-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command movement-smoke exercises the puzzle scene-bg pipeline end-to-end:
camera moves (pan / zoom) and image-to-image transitions are driven against the
encoder so the output stream demonstrates each path in isolation.

Inputs: assets/image-0.png and assets/image-1.png (1920x1080 RGB). Outputs:
<out>/hls/stream.m3u8 (segmented) + <out>/preview.mp4 (single file, ready to
play in any video player).
```
