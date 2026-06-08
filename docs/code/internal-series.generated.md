---
slug: code/internal/series
title: Package internal/series
description: Auto-generated go doc reference for the internal/series package.
---

# Package `internal/series`

_Generated with `go doc -all ./internal/series`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package series // import "github.com/sirily11/debate-bot/internal/series"

Package series wires the content_creator orchestrator + the SeriesStage for a
TV-series episode: walks prior-episode archives, builds the "previously on …"
recap via the compression LLM, plans + generates per-beat narration imagery,
kicks off music + sound clip generation, and post-archives the run output.

Lives in its own package (rather than under content_creator or cmd/debate-bot)
so the cmd/series-smoke binary can reuse the same code path the production
server boots through. Imports both video + audio + content_creator packages,
so it can't be a sub-package of any of them without creating an import cycle.

FUNCTIONS

func BuildTopicMsg(topic *config.DebateTopic, id, title string, index, total int) contentcreator.TopicMsg
    BuildTopicMsg shapes the per-content-type TopicMsg for series. Mirrors
    cmd/debate-bot's buildSeriesTopicMsg so the smoke binary doesn't have to
    duplicate it.

func FinishEpisode(log *slog.Logger, env *config.Env, topic *config.DebateTopic)
    FinishEpisode mirrors the puzzle pipeline's archival step: copies the
    per-run script + audio + subtitles from env.OutDir into the persistent
    episode directory so the next episode's recap engine can read them.
    Best-effort: errors are logged but never propagated.

func PrepareEpisode(ctx context.Context, log *slog.Logger, env *config.Env,
	stage *video.SeriesStage, topic *config.DebateTopic, orch *contentcreator.Orchestrator,
)
    PrepareEpisode runs the full series-episode preparation pipeline:

     1. Ensures the on-disk archive directory exists.
     2. Walks prior episodes for recap input + reuse catalog.
     3. Calls the compression LLM (when available) for the "previously on …"
        preamble; episode 1 / no creds → empty recap.
     4. Plans the narration beats via scenes.PlanSeries (or fallback).
     5. Persists the plan to scene-plan.json.
     6. Trims the catalog to highlight ids surfaced by the recap.
     7. Decodes prior-episode PNGs into the cross-episode resolver map and hands
        them to the SeriesStage.
     8. Pushes the prepared inputs onto the orchestrator BEFORE Run.
     9. Generates music + sounds + per-beat narration PNGs in parallel, blocking
        on the first priorityCount narration variants + the music bed.

    Errors anywhere in the pipeline are logged but never propagated — degraded
    paths are designed to still produce a runnable episode (no recap,
    fallback plan, dry TTS, etc.). The function returns once the orchestrator
    has everything it needs to start emitting turns.
```
