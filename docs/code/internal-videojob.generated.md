---
slug: code/internal/videojob
title: Package internal/videojob
description: Auto-generated go doc reference for the internal/videojob package.
---

# Package `internal/videojob`

_Generated with `go doc -all ./internal/videojob`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package videojob // import "github.com/sirily11/debate-bot/internal/videojob"

Package videojob runs the upload-and-render flow for the modeVideo HTTP server:
validate the user-supplied script.md, build a per-job orchestrator + encoder,
run the pipeline, then stitch the resulting HLS into a downloadable .mp4 (and
zip the persistent series archive).

Lives in its own package — between cmd/debate-bot and content_creator — to
break what would otherwise be an import cycle. content_creator already exports
the orchestrator + series helpers; server holds the JobRegistry the HTTP layer
reads. videojob is the glue that consumes both.

FUNCTIONS

func Submit(ctx context.Context, deps Deps, jobID string, sub server.JobSubmission) error
    Submit validates the request synchronously and enqueues the run. Returns nil
    on accept; returns an error when the upload is malformed (bad frontmatter,
    subtitle flag on non-series, etc.) so the HTTP layer can surface the reason.

    Validation runs upfront because:
      - the SPA shows the error inline rather than after a long wait;
      - the JobRegistry entry stays in JobError state with a descriptive
        message, so a user retrying through the UI gets feedback fast.

    The actual heavy work (asset gen, ffmpeg, zip) runs through the process-wide
    goqueue worker pool so video mode cannot start unbounded parallel encoders.


TYPES

type Deps struct {
	Env    *config.Env
	MCPCfg *config.MCPConfig
	Bus    *eventbus.Bus
	Jobs   *server.JobRegistry
	Queue  Queue
	Log    *slog.Logger
}
    Deps wires the runner to long-lived process state. Env is the
    LoadEnv-produced config (its OutDir is the session root, not the per-job
    dir — the runner appends jobs/<id>/). MCPCfg is forwarded to each per-job
    orchestrator; today most uploads run with empty mcp configs but the seam is
    here for future tools.

type Queue interface {
	Add(context.Context, func(context.Context))
}
    Queue is the small subset of goqueue.Queue that Submit needs.
```
