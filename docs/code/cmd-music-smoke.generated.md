---
slug: code/cmd/music-smoke
title: Package cmd/music-smoke
description: Auto-generated go doc reference for the cmd/music-smoke package.
---

# Package `cmd/music-smoke`

_Generated with `go doc -all ./cmd/music-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command music-smoke verifies the music subsystem end-to-end before plumbing it
into a full puzzle run. Two modes:

  - default: hits Lyria 3 Pro and writes the resulting mp3 to disk. Confirms the
    API request shape works.

  - --session: exercises musicmixer.NewSession by piping a short pre-generated
    TTS-style mp3 through it on top of the supplied music bed, then writes the
    mixed output for offline listening. Confirms the silence-filler keeps amix
    flowing between TTS bursts and that volume balance is sane. Pass --music to
    point at an existing bed (e.g. a previously cached Lyria clip).

Reads GEMINI_API_KEY from the process env (or .env via godotenv) for the default
mode; --session mode needs no API key.
```
