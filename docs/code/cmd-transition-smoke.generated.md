---
slug: code/cmd/transition-smoke
title: Package cmd/transition-smoke
description: Auto-generated go doc reference for the cmd/transition-smoke package.
---

# Package `cmd/transition-smoke`

_Generated with `go doc -all ./cmd/transition-smoke`. Regenerate with `scripts/gen_go_docs.sh`._

```text
Command transition-smoke reproduces the sequential-mode topic transition flow
against a real Encoder + bus + DebateStage + PuzzleStage so we can eyeball
whether two back-to-back topics hand off cleanly. No LLM / TTS / scene-gen
network calls — speakers, transcripts, scenes, and timing are scripted
procedurally so the smoke runs offline.

Output: out/transition-smoke/<mode>/preview.mp4 (also leaves the HLS segments
behind in the same folder).

Modes:

    puzzle-puzzle   two 海龜湯 puzzles back-to-back
    puzzle-debate   puzzle followed by a debate
    debate-puzzle   debate followed by a puzzle
    debate-debate   two debates back-to-back
    series-series   two narrated TV-series episodes back-to-back (mirrors
                    runChannel's Preactivate → TopicMsg → PostEpisodeIdle →
                    inter-episode gap → next-episode handoff)
    series-debate   series followed by a debate
    debate-series   debate followed by a series
    series-puzzle   series followed by a puzzle
    puzzle-series   puzzle followed by a series
```
