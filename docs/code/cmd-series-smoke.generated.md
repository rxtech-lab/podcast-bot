---
slug: code/cmd/series-smoke
title: Package cmd/series-smoke
description: Auto-generated go doc reference for the cmd/series-smoke package.
---

# Package `cmd/series-smoke`

_Generated with `go doc -all ./cmd/series-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command series-smoke renders a quick visual preview of the TV-series channel
style without going through the orchestrator, AI image gen, TTS, or any other
production-content path. It loads a topic.md just to pick up the title / show
name / surface text, then walks a fixed set of "narration beats" through the
renderer using committed PNGs from --assets (defaults to assets/) as scene
backgrounds. Output is a single mp4 you can scrub through to eyeball the layout,
lower-third, camera moves, and scene crossfades.

Frames are piped raw RGBA into ffmpeg so we get a real mp4 instead of a stack of
PNGs. No network calls, no API keys, no cached AI artefacts — just the renderer
+ on-disk asset PNGs.

Flags:

    --topic   path to a series topic.md (default channels/series/01_pilot.md)
    --out     output dir (default out/series-smoke)
    --assets  dir of background PNGs to rotate through (default assets/,
              matches assets/image-*.png)
```
