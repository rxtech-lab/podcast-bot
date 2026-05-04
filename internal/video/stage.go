package video

import (
	"context"

	"github.com/sirily11/debate-bot/internal/eventbus"
)

// Stage is the per-content-type video composer. One Stage is bound to one
// Encoder; it subscribes to the event bus and translates orchestrator events
// into Renderer state updates so the live show is baked into the video stream.
//
// Two implementations live side-by-side: DebateStage (debate format) and
// PuzzleStage (situation-puzzle / 海龜湯). Both can run concurrently against
// the same Encoder — each gates internally on TopicMsg.Type so only the stage
// matching the active content drives the encoder. When a TopicMsg flips the
// channel from debate → puzzle (or vice versa), the previously-active stage
// goes idle and the matching stage takes over.
type Stage interface {
	Run(ctx context.Context, bus *eventbus.Bus)
}
