---
slug: code/cmd/encoder-smoke
title: Package cmd/encoder-smoke
description: Auto-generated go doc reference for the cmd/encoder-smoke package.
---

# Package `cmd/encoder-smoke`

_Generated with `go doc -all ./cmd/encoder-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command encoder-smoke spins up the video.Encoder against a temp output dir,
lets the audio pump emit silence for a few seconds (no LiveStream attached),
then exits. Used to manually verify that the HLS muxer produces a manifest with
both video and audio tracks before wiring the encoder into a full run.
```
