---
slug: code/internal/server
title: Package internal/server
description: Auto-generated go doc reference for the internal/server package.
---

# Package `internal/server`

_Generated with `go doc -all ./internal/server`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package server // import "github.com/sirily11/debate-bot/internal/server"

Package server hosts the HTTP API for a debate run.

The server always operates in TV-channel mode: one channels.json defines the
available channels, debate.md files declare which channel they belong to,
and each channel runs its own queue of debates sequentially while all channels
run in parallel. Each channel has its own LiveStream + Encoder + HLS dir;
channels with no assigned debates are listed as off-air.

Endpoints:

    GET  /api/topics                        — channel list (number, title, off-air, debates queue).
    GET  /api/transcript?channel=<id>       — JSON snapshot of that channel's live transcript.
    GET  /api/events[?channel=<id>]         — Server-Sent Events; channel filter is optional.
    GET  /api/audio/<id>/stream             — chunked MP3 audio for that channel.
    GET  /api/video/<id>/<file>             — HLS playlist + segments for that channel.
    POST /api/messages?channel=<id>         — push a user message into that channel's orchestrator
                                              (uses the viewer's `debate-bot-username` cookie).
    GET  /api/me                            — return the viewer's username; issues + sets a cookie
                                              on first request.
    POST /api/me                            — change the viewer's username (body: {username}).
    GET  /                                  — embedded web UI.

TYPES

type ChannelInfo struct {
	ID              string    `json:"id"`
	Number          int       `json:"number"`
	Title           string    `json:"title"`
	OffAir          bool      `json:"off_air"`
	Debates         []Session `json:"debates"`
	CurrentDebateID string    `json:"current_debate_id,omitempty"`
}
    ChannelInfo is the JSON-facing description of a channel surfaced via
    /api/topics. Off-air channels (no debates assigned) are still listed so the
    frontend can render an "off air" placeholder for them.

type ChannelResources struct {
	Orch          *contentcreator.Orchestrator
	HLSDir        string
	LiveStream    *audio.LiveStream
	CurrentDBPath string
}
    ChannelResources bundles the per-channel runtime state HTTP handlers need
    when serving /api/video/<id>, /api/audio/<id>, /api/transcript?channel=<id>.

    CurrentDBPath is the sqlite file for the channel's currently airing or
    most-recently-aired debate. It outlives Orch — when a debate ends and Orch
    becomes nil, the path stays so the server can keep serving the transcript
    from disk.

type Deps struct {
	Mode       string
	Bus        *eventbus.Bus
	Sessions   *SessionRegistry
	Jobs       *JobRegistry
	Log        *slog.Logger
	UploadRoot string
	SubmitJob  func(jobID string, sub JobSubmission) error
}
    Deps wires the server to the event bus and the registry that tracks
    every channel + its current orchestrator. Per-channel streaming resources
    (LiveStream, HLS dir) are reached through Sessions.ChannelResources(id).

    Mode selects the HTTP surface: "stream" (default) mounts the channel / HLS /
    live-chat routes; "video" mounts /api/jobs/* on top of the same embedded SPA
    so a browser can upload a script.md and receive a downloadable mp4 + (for
    series) a zip archive.

    Jobs / SubmitJob are only consulted when Mode == "video". SubmitJob is a
    closure provided by main; the handler creates the job id, saves the upload
    files under UploadRoot/<jobID>/, registers the pending job, and hands off to
    SubmitJob which runs the orchestrator pipeline asynchronously and updates
    JobRegistry as the run progresses.

    UploadRoot is the directory where uploaded scripts + priors zips land.
    Each job gets its own subdirectory keyed by jobID.

type Job struct {
	ID          string    `json:"id"`
	Status      JobStatus `json:"status"`
	Title       string    `json:"title,omitempty"`
	Type        string    `json:"type,omitempty"`
	Show        string    `json:"show,omitempty"`
	Season      int       `json:"season,omitempty"`
	Episode     int       `json:"episode,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	VideoPath   string    `json:"-"`
	ArchivePath string    `json:"-"`
	HasVideo    bool      `json:"has_video"`
	HasArchive  bool      `json:"has_archive"`

	ElapsedMS   int64    `json:"elapsed_ms,omitempty"`
	RemainingMS int64    `json:"remaining_ms,omitempty"`
	Phase       string   `json:"phase,omitempty"`
	PhaseLabel  string   `json:"phase_label,omitempty"`
	Logs        []JobLog `json:"logs,omitempty"`
}
    Job is one upload-and-render request tracked by the registry.

    VideoPath / ArchivePath are absolute on-disk paths populated when the job
    finishes successfully. The HTTP layer serves them via /api/jobs/<id>/video
    and /api/jobs/<id>/archive; clients never see the underlying paths, only the
    URLs.

    Topic / Show / Type are echoed back so the SPA can render a finished- job
    header without re-parsing the user's upload.

type JobLog struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}
    JobLog is one persisted progress/log line for a video-mode job.

type JobRegistry struct {
	// Has unexported fields.
}
    JobRegistry persists video-mode jobs and progress logs to SQLite.

func NewJobRegistry(dbPath string) (*JobRegistry, error)
    NewJobRegistry opens or creates a SQLite-backed registry at dbPath.

func (r *JobRegistry) Add(id string) *Job
    Add inserts a fresh pending job. Caller picks the id.

func (r *JobRegistry) AppendLog(jobID, kind, text string, payload any)
    AppendLog persists one user-visible progress line for a job.

func (r *JobRegistry) Get(id string) *Job
    Get returns a snapshot of the named job, or nil when unknown.

func (r *JobRegistry) List() []Job
    List returns a stable-order snapshot of every known job (newest first by
    CreatedAt). Useful for an admin/debug endpoint; the frontend only reads its
    own job by id today.

func (r *JobRegistry) Update(id string, fn func(j *Job))
    Update applies fn to a snapshot and writes it back. No-op when the id is
    unknown.

type JobStatus string
    JobStatus is the lifecycle of a video-mode upload job.

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobError   JobStatus = "error"
)
type JobSubmission struct {
	ScriptPath        string
	PriorsZipPath     string
	SoftSubs          bool
	BurnSubs          bool
	Resolution        string
	SubtitleLanguages []string
}
    JobSubmission is the parsed multipart payload the server hands off to the
    job runner. ScriptPath / PriorsZipPath are absolute paths in the session
    OutDir where the HTTP handler saved the uploaded files.

    SoftSubs / BurnSubs are forwarded verbatim from the form. The runner is
    responsible for validating that the topic actually permits them (series
    only); the HTTP handler does a coarse pre-check based on the raw form
    values.

    Resolution overrides the topic.md `resolution:` field when non-empty — empty
    means "respect the script's declared resolution" so users who don't pick
    from the UI still get the topic-author's intent.

type Server struct {
	// Has unexported fields.
}
    Server is the HTTP front-end.

func New(d Deps) *Server
    New builds a Server with all routes mounted.

func (s *Server) Handler() http.Handler
    Handler exposes the underlying mux (useful for tests / custom mounting).

func (s *Server) ListenAndServe(ctx context.Context, addr string, started func(*net.TCPAddr)) error
    ListenAndServe binds to addr and serves until ctx is cancelled. addr like
    ":8080" or "127.0.0.1:0" (random port). The actual bound address is returned
    via the started callback so callers can discover a random port.

type Session struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Status         SessionStatus `json:"status"`
	TranscriptPath string        `json:"transcript_path,omitempty"`
	AudioPath      string        `json:"audio_path,omitempty"`
	// DBPath points at the per-debate sqlite file. The server uses it to
	// serve /api/transcript snapshots after a debate ends (when there's no
	// longer a live orchestrator holding the in-memory transcript).
	// Not exposed in JSON — clients shouldn't see filesystem paths.
	DBPath string `json:"-"`
}
    Session is one debate's metadata view. Sessions are grouped under channels
    in the registry.

type SessionRegistry struct {
	// Has unexported fields.
}
    SessionRegistry tracks every channel and its queue of debates. Channels
    are declared up front (from channels.json + the channel ids found on each
    debate.md); the live orchestrator within a channel rotates as that channel's
    queue plays out.

func NewSessionRegistry() *SessionRegistry
    NewSessionRegistry builds an empty registry. Channels are added with
    RegisterChannel; debates are seeded with SeedChannelDebates.

func (r *SessionRegistry) AppendChannelDebate(channelID string, sess Session) bool
    AppendChannelDebate adds a single debate to the end of a channel's queue.
    Used by the folder watcher when a new debate.md is dropped into the watched
    directory at runtime. Returns false if the channel is unknown or a debate
    with the same id already exists in this channel's queue (callers should
    generate a unique id before calling).

func (r *SessionRegistry) ChannelResources(id string) *ChannelResources
    ChannelResources returns the HTTP-facing resources for a channel id. Returns
    nil when the channel is unknown.

func (r *SessionRegistry) HasDebate(channelID, debateID string) bool
    HasDebate reports whether the named debate already exists on this channel.
    Lets the watcher dedupe before generating ids / loading topic files twice.

func (r *SessionRegistry) List() []ChannelInfo
    List returns the full channel list (with debate queues) for /api/topics.

func (r *SessionRegistry) RegisterChannel(id string, number int, title, hlsDir string, live *audio.LiveStream)
    RegisterChannel declares a channel up front with its display metadata and
    streaming resources. live/hlsDir may be empty when the channel is off-air
    (no debates assigned). Calling RegisterChannel a second time for the same id
    replaces the metadata + resources but keeps the existing debate queue.

func (r *SessionRegistry) RemoveChannelDebate(channelID, debateID string) (SessionStatus, bool)
    RemoveChannelDebate drops a debate from the channel's queue. Only Pending
    entries are removable — a Running debate is mid-flight (killing its
    metadata while audio/video keep streaming would leave the UI inconsistent),
    and Done/Error entries are kept as history. Returns the removed debate's
    status and ok=true on success; ok=false when the debate isn't found or isn't
    pending. The returned status lets callers log *why* a removal was skipped
    (running vs unknown).

func (r *SessionRegistry) SeedChannelDebates(channelID string, debates []Session)
    SeedChannelDebates installs the queue of debates for a channel (in play
    order). All entries start as pending.

func (r *SessionRegistry) SetCurrentOrch(channelID, debateID string, orch *contentcreator.Orchestrator)
    SetCurrentOrch installs (or clears) the live orchestrator for a channel
    as its queue advances. debateID identifies which debate just became live;
    pass "" + nil to clear between debates. When orch is non-nil we also latch
    its per-debate sqlite path so the server can keep serving /api/transcript
    from disk after the orchestrator exits.

func (r *SessionRegistry) SetDebateOutputs(channelID, debateID, transcriptPath, audioPath string)
    SetDebateOutputs records on-disk artefacts produced by a finished debate.

func (r *SessionRegistry) SetDebateStatus(channelID, debateID string, status SessionStatus)
    SetDebateStatus updates a single debate's lifecycle status within its
    channel.

type SessionStatus string
    SessionStatus enumerates the lifecycle of one debate within its channel's
    queue.

const (
	StatusPending SessionStatus = "pending"
	StatusRunning SessionStatus = "running"
	StatusDone    SessionStatus = "done"
	StatusError   SessionStatus = "error"
)
```
