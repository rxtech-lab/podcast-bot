---
slug: code/cmd/render-smoke
title: Package cmd/render-smoke
description: Auto-generated go doc reference for the cmd/render-smoke package.
---

# Package `cmd/render-smoke`

_Generated with `go doc -all ./cmd/render-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command render-smoke produces sample frames from the video renderer and writes
them as PNGs so we can eyeball that the layout (topic title, phase, subtitle
box) and CJK glyphs render correctly.

Three modes:

    --mode debate (default): the original CNN-style debate cases, output to
      out/render-smoke/.
    --mode puzzle: cinematic situation-puzzle layout. If OPENAI_API_KEY /
      AI_GATEWAY_API_KEY is set, real Gemini-generated scene backgrounds are
      fetched (and disk-cached). Otherwise the smoke test falls back to a
      procedural noise bg so the layout is still reviewable. Output goes to
      out/puzzle-render-smoke/.
    --mode puzzle-fade: emits an mp4 that demonstrates the cinematic name-
      plate fade-in / hold / fade-out so we can eyeball the smoothstep
      curve without having to wait the full 22 s hold in real time. Output
      goes to out/puzzle-fade-smoke/fade.mp4.
```
