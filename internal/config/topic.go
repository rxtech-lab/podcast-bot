package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentSpec describes one agent declared in topic.md frontmatter.
// BaseURL/APIKey are optional per-agent overrides; otherwise the env defaults are used.
type AgentSpec struct {
	Name    string `yaml:"name" json:"name"`
	Model   string `yaml:"model" json:"model"`
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	// Aspect is the perspective/angle a discussant argues from (e.g.
	// "economic", "ethical", "technical"). Discussion content type only;
	// ignored by every other format. Optional — a blank aspect just means
	// the discussant speaks from no pre-assigned angle.
	Aspect string `yaml:"aspect,omitempty" json:"aspect,omitempty"`
	// Voice is an optional TTS voice override (Azure neural voice ShortName,
	// e.g. "en-US-AvaMultilingualNeural"). Blank means the voice picker
	// auto-assigns one at generation time.
	Voice string `yaml:"voice,omitempty" json:"voice,omitempty"`
	// Gender is an optional voice-casting hint ("male"/"female") for the
	// automatic voice picker; blank falls back to inferring from Name.
	Gender string `yaml:"gender,omitempty" json:"gender,omitempty"`
}

// TTS provider identifiers used in topic.md `tts_provider:` field.
const (
	TTSProviderAzure  = "azure"
	TTSProviderEleven = "eleven"
)

// Output resolutions selectable from topic.md `resolution:` field. The renderer
// composites at 1920×1080 by default; ffmpeg only scales when callers request
// a different delivery size.
const (
	Resolution720p  = "720p"
	Resolution1080p = "1080p"
	Resolution4K    = "4k"
)

// Content types selectable via the `type:` field in topic.md frontmatter. The
// orchestrator picks an agent set + planner based on this value.
const (
	ContentTypeDebate          = "debate"
	ContentTypeSituationPuzzle = "situation-puzzle"
	// ContentTypeDiscussion is a multi-participant panel discussion. Several
	// discussants, each assigned a distinct aspect/perspective, talk through
	// one topic and respond to each other; a moderator (host) opens, hands
	// off, and closes; a single silent "commander" drives the background
	// image + music on the fly to match the mood. Each discussant gets
	// research tools (firecrawl MCP + a data-store scratchpad). See
	// discussion_planner.go / discussion_director.go / discussion_stage.go.
	ContentTypeDiscussion = "discussion"
	// ContentTypeAudioBook turns uploaded long-form source material into a
	// chaptered narrated audiobook. Planning produces a high-level outline
	// (speakers, overall summary, chapters) while generation narrates the
	// chapter plan as an audio-only feed.
	ContentTypeAudioBook = "audio-book"
	// ContentTypeSeries is a host-only narrated TV-style episode. Episodes
	// declare show + season + episode in frontmatter; the pipeline writes
	// every episode's assets (scene plan, generated PNGs, music, recap-
	// ready script) into a stable on-disk archive at
	// `<persistent-root>/tv-series/<show>/s<season>/e<episode>/`. Episode
	// N+1 reads that archive to (a) build a "previously on …" preamble
	// via the compression LLM and (b) re-use specific past beats by
	// emitting `<season-S-episode-E-image-N/>` markers in the host's
	// stream.
	ContentTypeSeries = "series"
	// ContentTypeUploadedAudio wraps user-provided audio: the user uploads a
	// finished recording, the server transcribes it with speaker diarization,
	// and the plan carries the sentence-level transcript segments instead of
	// a generative roster. Generation skips TTS entirely — the uploaded audio
	// becomes the podcast audio and the segments become captions/transcript.
	ContentTypeUploadedAudio = "uploaded-audio"
	// ContentTypeNews is a radio-news broadcast: an anchor reads/presents an
	// ordered story rundown (NewsStories) while one or more commentators —
	// stored in Discussants, each with a beat in Aspect — add context and
	// reactions between reads. It reuses the discussion live runtime (host +
	// discussants + silent commander, audience messages, research tools), so
	// the discussion roster fields double as the news roster: Host is the
	// anchor, Discussants are the commentators. Distinct from
	// AudioBookStyleNews, which is a narration style of the audio-book type.
	ContentTypeNews = "news"
)

// Research-scratchpad storage backends selectable via the `storage:` field in
// topic.md frontmatter (discussion content type only). Discussants research
// with firecrawl and stash findings through a data-store tool; the backend is
// either a built-in plain-text file store or the MongoDB MCP server.
const (
	// StoragePlaintext gives discussants a built-in file-backed data-store
	// tool (save/load/list under <out>/datastore). The default.
	StoragePlaintext = "plaintext"
	// StorageMongo expects a MongoDB MCP server declared in mcp.json; no
	// built-in store tool is registered, so discussants persist findings
	// through the MongoDB MCP tools instead.
	StorageMongo = "mongodb"
)

// AudioBookSpeaker is one voice role in an audiobook plan. The narrator is
// stored separately in AudioBookHost; these entries represent optional quoted
// characters or recurring voices the narration can switch to.
type AudioBookSpeaker struct {
	Name        string `yaml:"name" json:"name"`
	Gender      string `yaml:"gender,omitempty" json:"gender,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Model       string `yaml:"model,omitempty" json:"model,omitempty"`
	Voice       string `yaml:"voice,omitempty" json:"voice,omitempty"`
}

// AudioBookChapter is one proposed chapter in an audiobook plan.
//
// Mode is the narration style the planner chose for this chapter:
// "narration" (single narrator prose, the default) or "dialogue" (a
// conversational exchange where the listed Speakers take turns, each
// rendered in their own neural voice via <char-N> markers). Empty is
// treated as "narration". Speakers is the subset of AudioBookSpeakers
// (by name) that talk in this chapter — surfaced to the host so it knows
// which voices to alternate between for a dialogue chapter.
type AudioBookChapter struct {
	Title    string   `yaml:"title" json:"title"`
	Summary  string   `yaml:"summary" json:"summary"`
	Mode     string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Speakers []string `yaml:"speakers,omitempty" json:"speakers,omitempty"`
}

// Audiobook style and chapter narration modes.
const (
	AudioBookStyleNews           = "news"
	AudioBookStyleConversational = "conversational"
	AudioBookStyleAudioBook      = "audiobook"
	AudioBookStylePodcast        = "podcast"
	AudioBookStyleMeeting        = "meeting"

	AudioBookModeNarration = "narration"
	AudioBookModeDialogue  = "dialogue"
)

// NewsStory is one item in a news podcast's rundown: the on-air headline, a
// short summary the anchor reads from, and optional dated/attributed key
// facts. Stories are presented in order; each drives one on-air segment
// (anchor read → commentator reactions → bridge to the next story).
type NewsStory struct {
	Headline string   `yaml:"headline" json:"headline"`
	Summary  string   `yaml:"summary" json:"summary"`
	KeyFacts []string `yaml:"key_facts,omitempty" json:"key_facts,omitempty"`
}

// TranscriptSegment is one sentence-level piece of an uploaded-audio
// transcript: who said it, where it sits on the audio timeline, and the text.
// Segments are the source of truth for uploaded-audio podcasts — the plan
// chat edits Speaker/Text, direct user edits may also correct the time range,
// and publish renders subtitles and the transcript from the saved result. The
// AI planner still treats offsets/durations as fixed source-audio metadata.
type TranscriptSegment struct {
	Speaker    string `yaml:"speaker" json:"speaker"`
	OffsetMS   int64  `yaml:"offset_ms" json:"offset_ms"`
	DurationMS int64  `yaml:"duration_ms" json:"duration_ms"`
	Text       string `yaml:"text" json:"text"`
}

// DebateTopic is the full topic.md content: YAML frontmatter + named markdown
// sections. Despite the name, it now covers every supported content type
// (debate + situation-puzzle); the active subset of fields depends on Type.
type DebateTopic struct {
	Title             string `yaml:"title" json:"title"`
	Type              string `yaml:"type" json:"type"`
	Language          string `yaml:"language" json:"language"`
	TotalMinutes      int    `yaml:"total_minutes" json:"total_minutes"`
	SegmentMaxSeconds int    `yaml:"segment_max_seconds" json:"segment_max_seconds"`
	TTSProvider       string `yaml:"tts_provider,omitempty" json:"tts_provider,omitempty"`
	Resolution        string `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	// Channel is the id of the TV-style channel this debate belongs to.
	// Channels are defined in channels.json. Multiple debates with the same
	// channel id are queued and play sequentially within that channel; debates
	// on different channels run in parallel as independent video streams.
	// Required — startup fails if the id isn't defined in channels.json.
	Channel string `yaml:"channel" json:"channel"`

	// Debate-only roster.
	Affirmative []AgentSpec `yaml:"affirmative,omitempty" json:"affirmative,omitempty"`
	Negative    []AgentSpec `yaml:"negative,omitempty" json:"negative,omitempty"`
	Judge       AgentSpec   `yaml:"judge,omitempty" json:"judge,omitempty"`

	// Situation-puzzle-only roster. PuzzleHost is the 出題者 who knows the
	// hidden truth and answers player questions with 是/不是/與此無關.
	// Players are 解題者 trying to deduce the truth.
	PuzzleHost AgentSpec   `yaml:"puzzle_host,omitempty" json:"puzzle_host,omitempty"`
	Players    []AgentSpec `yaml:"players,omitempty" json:"players,omitempty"`

	// Series-only roster + metadata. Show is the human-readable show name
	// (slugified for the on-disk archive directory). Season + Episode are
	// 1-based; the recap engine treats lexicographic (season, episode)
	// order as canonical "before this episode" (so s2e1 follows s1e9).
	// SeriesHost is the single narrator agent; series episodes are
	// non-interactive (no players, no Q&A, no live audience).
	Show       string    `yaml:"show,omitempty" json:"show,omitempty"`
	Season     int       `yaml:"season,omitempty" json:"season,omitempty"`
	Episode    int       `yaml:"episode,omitempty" json:"episode,omitempty"`
	SeriesHost AgentSpec `yaml:"series_host,omitempty" json:"series_host,omitempty"`

	// Audio-book-only roster + outline. AudioBookHost is the narrator; optional
	// AudioBookSpeakers are voice roles for quoted material; AudioBookChapters
	// is the chapter plan generated from long source content. AudioBookStyle is
	// the high-level format chosen during planning (news, conversational,
	// audiobook, podcast, or meeting).
	AudioBookHost     AgentSpec          `yaml:"audio_book_host,omitempty" json:"audio_book_host,omitempty"`
	AudioBookStyle    string             `yaml:"audio_book_style,omitempty" json:"audio_book_style,omitempty"`
	AudioBookSpeakers []AudioBookSpeaker `yaml:"audio_book_speakers,omitempty" json:"audio_book_speakers,omitempty"`
	AudioBookChapters []AudioBookChapter `yaml:"audio_book_chapters,omitempty" json:"audio_book_chapters,omitempty"`
	// AudioBookChapterIndices are the 1-based positions, in the root plan's
	// full chapter list, that THIS script narrates. Set on derived batch
	// scripts so a batch covering chapters 6-8 keeps global numbering and the
	// chapter-progress UI knows what each generation covered. Empty means the
	// script narrates all of AudioBookChapters (legacy single-shot audiobooks).
	AudioBookChapterIndices []int `yaml:"audio_book_chapter_indices,omitempty" json:"audio_book_chapter_indices,omitempty"`

	// Discussion-only roster. Discussants each carry an Aspect (the angle
	// they speak from) and respond to one another; Host moderates; Commander
	// is the single silent director that drives background image + music on
	// the fly (it never speaks). Storage picks the research-scratchpad
	// backend (StoragePlaintext / StorageMongo); empty defaults to plaintext.
	Discussants []AgentSpec `yaml:"discussants,omitempty" json:"discussants,omitempty"`
	Host        AgentSpec   `yaml:"host,omitempty" json:"host,omitempty"`
	Commander   AgentSpec   `yaml:"commander,omitempty" json:"commander,omitempty"`
	Storage     string      `yaml:"storage,omitempty" json:"storage,omitempty"`

	// News-only rundown. News reuses the discussion roster above (Host = the
	// anchor, Discussants = the commentators, Commander = silent director);
	// NewsStories is the ordered story list the anchor presents on air.
	NewsStories []NewsStory `yaml:"news_stories,omitempty" json:"news_stories,omitempty"`

	// Uploaded-audio-only fields. UploadedAudioKey is the durable storage key
	// of the user's original upload; the duration and max-speaker setting come
	// from transcription. TranscriptSegments carry yaml tags (unlike Sources)
	// because they must survive the RenderMarkdown → LoadTopic round trip the
	// generation submit path performs.
	UploadedAudioKey         string              `yaml:"uploaded_audio_key,omitempty" json:"uploaded_audio_key,omitempty"`
	UploadedAudioDurationMS  int64               `yaml:"uploaded_audio_duration_ms,omitempty" json:"uploaded_audio_duration_ms,omitempty"`
	UploadedAudioMaxSpeakers int                 `yaml:"uploaded_audio_max_speakers,omitempty" json:"uploaded_audio_max_speakers,omitempty"`
	UploadedAudioSpeakers    []string            `yaml:"uploaded_audio_speakers,omitempty" json:"uploaded_audio_speakers,omitempty"`
	TranscriptSegments       []TranscriptSegment `yaml:"transcript_segments,omitempty" json:"transcript_segments,omitempty"`

	// Shared across both content types.
	Viewers []AgentSpec `yaml:"viewers,omitempty" json:"viewers,omitempty"`

	// Sources are researched references gathered during the planning phase
	// when research is enabled. Advisory: surfaced to the planning UI (and the
	// iOS app's plan view) and used to ground the background; not serialized to
	// frontmatter or parsed back from markdown.
	Sources []Source `yaml:"-" json:"sources,omitempty"`

	// Body sections, populated from markdown after frontmatter.
	// Debate sections:
	Background string `yaml:"-" json:"background,omitempty"`
	// SourceDocuments is a digest of the user's uploaded reference documents
	// (built from the planning conversation at generation-submit time, never
	// persisted on the stored plan). Discussion runs inject it into the host
	// and discussant prompts so the host can summarize the source material
	// and discussants can quote it. Heading-like lines are sanitized by the
	// digest builder because parseSections treats any "## " line as a
	// section boundary.
	SourceDocuments string `yaml:"-" json:"source_documents,omitempty"`
	AffirmativePos  string `yaml:"-" json:"affirmative_position,omitempty"`
	NegativePos     string `yaml:"-" json:"negative_position,omitempty"`
	Rules           string `yaml:"-" json:"rules,omitempty"`
	// Situation-puzzle sections:
	Surface string `yaml:"-" json:"surface,omitempty"` // 湯面 — visible to everyone
	Truth   string `yaml:"-" json:"truth,omitempty"`   // 湯底 — only the puzzle host's prompt sees it
}

// Source is one researched reference gathered during planning: a human-facing
// title, the canonical URL, a short snippet, and the full markdown content
// when the research provider returned it.
type Source struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

// LoadTopic parses a debate.md file with YAML frontmatter and markdown body.
func LoadTopic(path string) (*DebateTopic, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read debate: %w", err)
	}
	front, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return nil, err
	}
	var t DebateTopic
	if err := yaml.Unmarshal([]byte(front), &t); err != nil {
		return nil, fmt.Errorf("parse debate frontmatter: %w", err)
	}
	parseSections(body, &t)
	if err := validateTopic(&t); err != nil {
		return nil, err
	}
	if t.Language == "" {
		t.Language = "en-US"
	}
	if t.SegmentMaxSeconds == 0 {
		t.SegmentMaxSeconds = 60
	}
	if t.TotalMinutes == 0 {
		t.TotalMinutes = 30
	}
	if t.TTSProvider == "" {
		t.TTSProvider = TTSProviderAzure
	}
	if t.Resolution == "" {
		t.Resolution = Resolution1080p
	}
	if (t.Type == ContentTypeDiscussion || t.Type == ContentTypeNews) && t.Storage == "" {
		t.Storage = StoragePlaintext
	}
	return &t, nil
}

func splitFrontmatter(s string) (front, body string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 1<<20), 1<<22)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("topic.md must start with --- frontmatter fence")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", fmt.Errorf("topic.md frontmatter not closed with ---")
	}
	front = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return front, body, nil
}

func parseSections(body string, t *DebateTopic) {
	// Series content can name its synopsis section "Surface" (matches the
	// puzzle convention) or "Series" / "Series summary" (more idiomatic
	// for a TV-series episode). Both feed the same Surface field — the
	// downstream pipeline doesn't care about the heading text.
	sections := map[string]*string{
		"background":           &t.Background,
		"source documents":     &t.SourceDocuments,
		"affirmative position": &t.AffirmativePos,
		"negative position":    &t.NegativePos,
		"rules":                &t.Rules,
		"surface":              &t.Surface,
		"series":               &t.Surface,
		"series summary":       &t.Surface,
		"synopsis":             &t.Surface,
		"truth":                &t.Truth,
	}
	var current *string
	var buf strings.Builder
	flush := func() {
		if current != nil {
			*current = strings.TrimSpace(buf.String())
		}
		buf.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "## ") {
			flush()
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "## ")))
			current = sections[heading]
			continue
		}
		if current != nil {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
}

func validateTopic(t *DebateTopic) error {
	if t.Title == "" {
		return fmt.Errorf("topic title is required")
	}
	if t.Channel == "" {
		return fmt.Errorf("channel is required (set `channel: <id>` in frontmatter; ids are defined in channels.json)")
	}
	switch t.Type {
	case ContentTypeDebate, ContentTypeSituationPuzzle, ContentTypeSeries, ContentTypeDiscussion, ContentTypeAudioBook, ContentTypeUploadedAudio, ContentTypeNews:
	default:
		return fmt.Errorf("type must be one of %q, %q, %q, %q, %q, %q, %q (got %q)",
			ContentTypeDebate, ContentTypeSituationPuzzle, ContentTypeSeries, ContentTypeDiscussion, ContentTypeAudioBook, ContentTypeUploadedAudio, ContentTypeNews, t.Type)
	}
	switch t.TTSProvider {
	case "", TTSProviderAzure, TTSProviderEleven:
	default:
		return fmt.Errorf("tts_provider must be %q or %q (got %q)",
			TTSProviderAzure, TTSProviderEleven, t.TTSProvider)
	}
	switch t.Resolution {
	case "", Resolution720p, Resolution1080p, Resolution4K:
	default:
		return fmt.Errorf("resolution must be one of %q, %q, %q (got %q)",
			Resolution720p, Resolution1080p, Resolution4K, t.Resolution)
	}
	for _, v := range t.Viewers {
		if v.Name == "" || v.Model == "" {
			return fmt.Errorf("viewer entry needs name and model")
		}
	}
	switch t.Type {
	case ContentTypeDebate:
		return validateDebate(t)
	case ContentTypeSituationPuzzle:
		return validateSituationPuzzle(t)
	case ContentTypeSeries:
		return validateSeries(t)
	case ContentTypeDiscussion:
		return validateDiscussion(t)
	case ContentTypeAudioBook:
		return validateAudioBook(t)
	case ContentTypeUploadedAudio:
		return validateUploadedAudio(t)
	case ContentTypeNews:
		return validateNews(t)
	}
	return nil
}

func validateUploadedAudio(t *DebateTopic) error {
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" ||
		t.PuzzleHost.Model != "" || len(t.Players) > 0 || t.SeriesHost.Model != "" ||
		len(t.Discussants) > 0 || t.Host.Model != "" || t.Commander.Model != "" ||
		t.AudioBookHost.Model != "" {
		return fmt.Errorf("type=uploaded-audio must not declare a generative roster — the transcript defines the speakers")
	}
	if strings.TrimSpace(t.UploadedAudioKey) == "" {
		return fmt.Errorf("uploaded_audio_key is required for type=uploaded-audio")
	}
	if len(t.TranscriptSegments) == 0 {
		return fmt.Errorf("type=uploaded-audio requires at least one transcript_segments entry")
	}
	seenSpeakers := make(map[string]struct{}, len(t.UploadedAudioSpeakers))
	for _, speaker := range t.UploadedAudioSpeakers {
		name := strings.TrimSpace(speaker)
		if name == "" {
			return fmt.Errorf("uploaded_audio_speakers entries must not be empty")
		}
		key := strings.ToLower(name)
		if _, exists := seenSpeakers[key]; exists {
			return fmt.Errorf("uploaded_audio_speakers entries must be unique")
		}
		seenSpeakers[key] = struct{}{}
	}
	for _, seg := range t.TranscriptSegments {
		if strings.TrimSpace(seg.Speaker) == "" || strings.TrimSpace(seg.Text) == "" {
			return fmt.Errorf("transcript_segments entries need speaker and text")
		}
	}
	return nil
}

// UploadedAudioSpeakerNames returns the persisted roster followed by any
// transcript speakers missing from it. It keeps older plans (which predate the
// explicit roster) compatible while allowing newly added, not-yet-assigned
// speakers to remain available in the transcript editor.
func UploadedAudioSpeakerNames(t *DebateTopic) []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t.UploadedAudioSpeakers)+len(t.TranscriptSegments))
	seen := make(map[string]struct{}, cap(names))
	appendName := func(raw string) {
		name := strings.TrimSpace(raw)
		key := strings.ToLower(name)
		if name == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	for _, name := range t.UploadedAudioSpeakers {
		appendName(name)
	}
	for _, segment := range t.TranscriptSegments {
		appendName(segment.Speaker)
	}
	return names
}

func validateAudioBook(t *DebateTopic) error {
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=audio-book must not declare affirmative/negative/judge — use audio_book_host")
	}
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 || t.SeriesHost.Model != "" {
		return fmt.Errorf("type=audio-book must not declare puzzle_host/players/series_host — use audio_book_host")
	}
	if len(t.Discussants) > 0 || t.Host.Model != "" || t.Commander.Model != "" {
		return fmt.Errorf("type=audio-book must not declare discussion host/discussants/commander — use audio_book_host and audio_book_speakers")
	}
	if t.AudioBookHost.Model == "" {
		return fmt.Errorf("audio_book_host.model is required for type=audio-book")
	}
	switch strings.TrimSpace(t.AudioBookStyle) {
	case "", AudioBookStyleNews, AudioBookStyleConversational, AudioBookStyleAudioBook, AudioBookStylePodcast, AudioBookStyleMeeting:
	default:
		return fmt.Errorf("audio_book_style must be one of %q, %q, %q, %q, %q (got %q)",
			AudioBookStyleNews, AudioBookStyleConversational, AudioBookStyleAudioBook, AudioBookStylePodcast, AudioBookStyleMeeting, t.AudioBookStyle)
	}
	for _, s := range t.AudioBookSpeakers {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("audio_book_speakers entries need name")
		}
	}
	if len(t.AudioBookChapters) == 0 {
		return fmt.Errorf("type=audio-book requires at least one audio_book_chapters entry")
	}
	for _, ch := range t.AudioBookChapters {
		if strings.TrimSpace(ch.Title) == "" || strings.TrimSpace(ch.Summary) == "" {
			return fmt.Errorf("audio_book_chapters entries need title and summary")
		}
	}
	if strings.TrimSpace(t.Background) == "" {
		return fmt.Errorf("type=audio-book requires an overall summary in `## Background`")
	}
	if strings.TrimSpace(t.Surface) == "" {
		return fmt.Errorf("type=audio-book requires a chapter outline in `## Surface`")
	}
	return nil
}

func validateDiscussion(t *DebateTopic) error {
	// Discussion is its own roster: discussants + host + commander. Reject
	// debate/puzzle/series fields so a copy-paste from another fixture
	// doesn't silently build agents that never speak.
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=discussion must not declare affirmative/negative/judge — use discussants/host/commander")
	}
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 || t.SeriesHost.Model != "" {
		return fmt.Errorf("type=discussion must not declare puzzle_host/players/series_host")
	}
	if len(t.Discussants) < 2 {
		return fmt.Errorf("type=discussion requires at least two discussants")
	}
	for _, s := range t.Discussants {
		if s.Name == "" || s.Model == "" {
			return fmt.Errorf("discussant entry needs name and model")
		}
	}
	if t.Host.Model == "" {
		return fmt.Errorf("host.model is required for type=discussion (the moderator)")
	}
	if t.Commander.Model == "" {
		return fmt.Errorf("commander.model is required for type=discussion (the silent visual/music director)")
	}
	switch t.Storage {
	case "", StoragePlaintext, StorageMongo:
	default:
		return fmt.Errorf("storage must be %q or %q (got %q)", StoragePlaintext, StorageMongo, t.Storage)
	}
	return nil
}

func validateNews(t *DebateTopic) error {
	// News rides the discussion roster (Host = anchor, Discussants =
	// commentators, Commander = silent director) plus its own rundown.
	// Reject other formats' fields like validateDiscussion does.
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=news must not declare affirmative/negative/judge — use host/discussants/commander")
	}
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 || t.SeriesHost.Model != "" {
		return fmt.Errorf("type=news must not declare puzzle_host/players/series_host")
	}
	if t.AudioBookHost.Model != "" {
		return fmt.Errorf("type=news must not declare audio_book_host — use host/discussants/commander")
	}
	if len(t.Discussants) < 1 {
		return fmt.Errorf("type=news requires at least one commentator (discussants)")
	}
	for _, s := range t.Discussants {
		if s.Name == "" || s.Model == "" {
			return fmt.Errorf("commentator (discussant) entry needs name and model")
		}
	}
	if t.Host.Model == "" {
		return fmt.Errorf("host.model is required for type=news (the anchor)")
	}
	if t.Commander.Model == "" {
		return fmt.Errorf("commander.model is required for type=news (the silent visual/music director)")
	}
	if len(t.NewsStories) == 0 {
		return fmt.Errorf("type=news requires at least one news_stories entry")
	}
	for _, s := range t.NewsStories {
		if strings.TrimSpace(s.Headline) == "" || strings.TrimSpace(s.Summary) == "" {
			return fmt.Errorf("news_stories entries need headline and summary")
		}
	}
	switch t.Storage {
	case "", StoragePlaintext, StorageMongo:
	default:
		return fmt.Errorf("storage must be %q or %q (got %q)", StoragePlaintext, StorageMongo, t.Storage)
	}
	return nil
}

func validateSeries(t *DebateTopic) error {
	// Series episodes are host-only — no debaters, no judge, no puzzle host,
	// no players. Reject those fields with a clear message rather than
	// silently ignoring them; otherwise a copy-paste from a debate fixture
	// would build extra agents that never speak.
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=series must not declare affirmative/negative/judge — series uses series_host only")
	}
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 {
		return fmt.Errorf("type=series must not declare puzzle_host or players — series uses series_host only")
	}
	if t.SeriesHost.Model == "" {
		return fmt.Errorf("series_host.model is required for type=series")
	}
	if strings.TrimSpace(t.Show) == "" {
		return fmt.Errorf("type=series requires a non-empty `show` frontmatter field (used to namespace the on-disk archive)")
	}
	if t.Season < 1 {
		return fmt.Errorf("type=series requires `season` >= 1 (got %d)", t.Season)
	}
	if t.Episode < 1 {
		return fmt.Errorf("type=series requires `episode` >= 1 (got %d)", t.Episode)
	}
	if strings.TrimSpace(t.Surface) == "" {
		return fmt.Errorf("type=series requires a synopsis section — `## Surface`, `## Series`, `## Series summary`, or `## Synopsis`")
	}
	if strings.TrimSpace(t.Truth) != "" {
		return fmt.Errorf("type=series must not declare a `## Truth` section — series episodes are not puzzles")
	}
	return nil
}

func validateDebate(t *DebateTopic) error {
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 {
		return fmt.Errorf("type=debate must not declare puzzle_host or players")
	}
	if len(t.Affirmative) == 0 {
		return fmt.Errorf("at least one affirmative candidate required")
	}
	if len(t.Negative) == 0 {
		return fmt.Errorf("at least one negative candidate required")
	}
	if t.Judge.Model == "" {
		return fmt.Errorf("judge.model is required")
	}
	for _, s := range t.Affirmative {
		if s.Name == "" || s.Model == "" {
			return fmt.Errorf("affirmative entry needs name and model")
		}
	}
	for _, s := range t.Negative {
		if s.Name == "" || s.Model == "" {
			return fmt.Errorf("negative entry needs name and model")
		}
	}
	return nil
}

func validateSituationPuzzle(t *DebateTopic) error {
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=situation-puzzle must not declare affirmative/negative/judge — use puzzle_host and players instead")
	}
	if t.PuzzleHost.Model == "" {
		return fmt.Errorf("puzzle_host.model is required for type=situation-puzzle")
	}
	if len(t.Players) == 0 {
		return fmt.Errorf("at least one player required for type=situation-puzzle")
	}
	for _, p := range t.Players {
		if p.Name == "" || p.Model == "" {
			return fmt.Errorf("player entry needs name and model")
		}
	}
	if strings.TrimSpace(t.Surface) == "" {
		return fmt.Errorf("type=situation-puzzle requires a `## Surface` section (湯面)")
	}
	if strings.TrimSpace(t.Truth) == "" {
		return fmt.Errorf("type=situation-puzzle requires a `## Truth` section (湯底)")
	}
	return nil
}
