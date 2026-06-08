---
slug: code/cmd/series-recap-smoke
title: Package cmd/series-recap-smoke
description: Auto-generated go doc reference for the cmd/series-recap-smoke package.
---

# Package `cmd/series-recap-smoke`

_Generated with `go doc -all ./cmd/series-recap-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command series-recap-smoke runs two consecutive series episodes against the same
persistent archive root and validates the cross-episode plumbing:

 1. Episode 1 (default: channels/series/01_pilot.md) runs
    end-to-end and archives its scene plan + script + audio under
    `<root>/tv-series/<show>/s1/e1/`.
 2. Episode 2 (default: channels/series/02_followup.md) runs in the same
    process. Its preparation step calls the compression LLM (when creds are
    available) for a "previously on …" recap and lifts canonical image-reuse
    keys out of the prior plan.
 3. The smoke asserts: * episode 1 produced scene-plan.json (and, with creds,
    the audio + subtitle artefacts). * episode 2 saw a non-empty recap
    (when creds were available). * episode 2's host stream emitted a
    `<season-1-episode-N-image-M/>` marker (only verified when episode 2
    actually produced TTS — the smoke peeks the per-turn script files).

Without API creds the smoke degrades to fixture-cached fallbacks; the assertions
about real LLM-driven recaps + image-reuse markers become soft warnings instead
of failures.

Flags:

    --ep1     path to episode-1 topic (default channels/series/01_pilot.md)
    --ep2     path to episode-2 topic (default channels/series/02_followup.md)
    --out     output root (default: tempdir; --keep retains it on exit)
    --keep    don't delete the temp dir on success
    --mp4     stitch each episode's HLS + audio into out-eN.mp4 via ffmpeg
```
