package contentcreator

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// NewsScriptSource pre-writes one broadcast segment's on-air lines. Satisfied
// by *agent.NewsScriptWriter; tests substitute a stub.
type NewsScriptSource interface {
	WriteSegment(ctx context.Context, req agent.NewsSegmentRequest) ([]agent.NewsScriptLine, error)
}

// NewsPlanner drives the radio-news-broadcast format as a SCRIPTED show: a
// background feeder pre-generates every segment's on-air lines (intro, one
// segment per rundown story, sign-off) via the NewsScriptSource, running well
// ahead of playout, and the planner replays those lines one turn at a time
// with "scripted:" directives that the news agents deliver verbatim — no
// per-turn model call, so the broadcast has no dead air between speakers, and
// no open-ended improvisation, so the desk tells the news instead of debating
// it. The only live LLM turns are listener interactions: the anchor
// paraphrases a listener message (address-user) and hands off to a co-host
// who answers (answer-user), after which the script resumes where it left
// off. The commander (silent visual/music director) is NOT scheduled here —
// it runs independently in the DiscussionDirector loop.
type NewsPlanner struct {
	topic      *config.DebateTopic
	tracker    *Tracker
	registry   *agent.Registry
	queue      *userQueue
	transcript *Transcript
	writer     NewsScriptSource // nil → verbatim rundown fallback (tests / degraded)

	turnN int
	state newsState

	// feedOnce starts the feeder on the first Next call, whose ctx spans the
	// whole production run. segCh carries the intro + story segments in
	// broadcast order and is buffered to hold the entire rundown, so the
	// feeder generates the full script as fast as the model allows.
	// closingCh carries the sign-off, generated SECOND (right after the
	// intro) so an early /end or timeout never waits behind story generation.
	feedOnce  sync.Once
	segCh     chan []agent.NewsScriptLine
	closingCh chan []agent.NewsScriptLine

	// cur is the segment currently on air; curIdx the next line within it.
	cur    []agent.NewsScriptLine
	curIdx int

	// pendingAnswerAgent is the co-host named in the anchor's most recent
	// address-user handoff; the following answer-user turn is routed to it.
	pendingAnswerAgent agent.Agent
}

type newsState struct {
	phase        agent.Phase
	introFetched bool
	rrIdx        int // round-robin cursor for listener-answer handoffs
	endRequested bool

	closingStarted bool
	closingLines   []agent.NewsScriptLine
	closingIdx     int

	pendingAnswerUser     bool
	pendingAnswerUserText string
}

const (
	newsIntroSeconds   = 45
	newsClosingSeconds = 40
	// newsClosingThreshold is how much tracker budget must remain to start
	// another story segment; below it the broadcast signs off.
	newsClosingThreshold = 90 * time.Second
)

// NewNewsPlanner constructs the news-format planner. writer may be nil, in
// which case every segment falls back to a verbatim reading of the rundown.
func NewNewsPlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry,
	q *userQueue, tr *Transcript, writer NewsScriptSource,
) *NewsPlanner {
	stories := 0
	if topic != nil {
		stories = len(topic.NewsStories)
	}
	return &NewsPlanner{
		topic:      topic,
		tracker:    tracker,
		registry:   reg,
		queue:      q,
		transcript: tr,
		writer:     writer,
		state:      newsState{phase: agent.PhaseOpening},
		segCh:      make(chan []agent.NewsScriptLine, stories+1),
		closingCh:  make(chan []agent.NewsScriptLine, 1),
	}
}

// storySeconds is each story segment's airtime target: the time budget minus
// intro + sign-off, split across the rundown, clamped so a huge budget over a
// short rundown doesn't demand half-hour segments and a tiny budget doesn't
// starve the anchor's read.
func (p *NewsPlanner) storySeconds() int {
	stories := 1
	if p.topic != nil && len(p.topic.NewsStories) > 0 {
		stories = len(p.topic.NewsStories)
	}
	minutes := 30
	if p.topic != nil && p.topic.TotalMinutes > 0 {
		minutes = p.topic.TotalMinutes
	}
	per := (minutes*60 - newsIntroSeconds - newsClosingSeconds) / stories
	if per < 60 {
		return 60
	}
	if per > 240 {
		return 240
	}
	return per
}

// addOnsPerStory is how many co-hosts add on after the anchor's read: every
// co-host on a single-story deep dive, two on a small rundown, one on a brisk
// roundup — never more than the desk has.
func (p *NewsPlanner) addOnsPerStory() int {
	commentators := len(p.registry.Discussants)
	if commentators == 0 {
		return 0
	}
	stories := 1
	if p.topic != nil {
		stories = len(p.topic.NewsStories)
	}
	want := 1
	switch {
	case stories <= 1:
		want = 3
	case stories <= 4:
		want = 2
	}
	if want > commentators {
		want = commentators
	}
	return want
}

func (p *NewsPlanner) startFeeder(ctx context.Context) {
	p.feedOnce.Do(func() { go p.feed(ctx) })
}

// feed pre-generates the whole broadcast script: intro, sign-off (early, on
// its own channel), then one segment per story with the add-on speakers
// rotated round-robin so airtime spreads across the desk.
func (p *NewsPlanner) feed(ctx context.Context) {
	defer close(p.segCh)
	intro := p.writeOrFallback(ctx, agent.NewsSegmentRequest{
		Kind: agent.NewsSegmentIntro, TargetSeconds: newsIntroSeconds,
	})
	if !p.sendSegment(ctx, p.segCh, intro) {
		return
	}
	closing := p.writeOrFallback(ctx, agent.NewsSegmentRequest{
		Kind: agent.NewsSegmentClosing, TargetSeconds: newsClosingSeconds,
	})
	if !p.sendSegment(ctx, p.closingCh, closing) {
		return
	}

	storySec := p.storySeconds()
	addOns := p.addOnsPerStory()
	cursor := 0
	prev := ""
	var stories []config.NewsStory
	if p.topic != nil {
		stories = p.topic.NewsStories
	}
	for i, s := range stories {
		var speakers []string
		for k := 0; k < addOns; k++ {
			speakers = append(speakers, p.registry.Discussants[cursor%len(p.registry.Discussants)].Name())
			cursor++
		}
		lines := p.writeOrFallback(ctx, agent.NewsSegmentRequest{
			Kind:          agent.NewsSegmentStory,
			StoryNumber:   i + 1,
			StoryTotal:    len(stories),
			Headline:      s.Headline,
			Summary:       s.Summary,
			KeyFacts:      s.KeyFacts,
			PrevHeadline:  prev,
			AddOnSpeakers: speakers,
			TargetSeconds: storySec,
		})
		if !p.sendSegment(ctx, p.segCh, lines) {
			return
		}
		prev = s.Headline
	}
}

func (p *NewsPlanner) sendSegment(ctx context.Context, ch chan []agent.NewsScriptLine, lines []agent.NewsScriptLine) bool {
	select {
	case ch <- lines:
		return true
	case <-ctx.Done():
		return false
	}
}

// writeOrFallback asks the script writer for the segment and falls back to a
// verbatim reading of the rundown material when the writer is missing or
// fails — the broadcast degrades to dry copy instead of dying.
func (p *NewsPlanner) writeOrFallback(ctx context.Context, req agent.NewsSegmentRequest) []agent.NewsScriptLine {
	if p.writer != nil && ctx.Err() == nil {
		if lines, err := p.writer.WriteSegment(ctx, req); err == nil && len(lines) > 0 {
			return lines
		}
	}
	return p.fallbackLines(req)
}

// fallbackLines builds speaker copy straight from the plan material (which is
// already in the broadcast's language). Story fallback still gives every
// selected co-host a factual line, so a writer failure cannot silence the desk.
func (p *NewsPlanner) fallbackLines(req agent.NewsSegmentRequest) []agent.NewsScriptLine {
	anchor := ""
	if p.registry != nil && p.registry.Host != nil {
		anchor = p.registry.Host.Name()
	}
	switch req.Kind {
	case agent.NewsSegmentIntro:
		var heads []string
		if p.topic != nil {
			for _, s := range p.topic.NewsStories {
				if h := strings.TrimSpace(s.Headline); h != "" {
					heads = append(heads, h)
				}
			}
		}
		if len(heads) == 0 {
			return nil
		}
		return []agent.NewsScriptLine{{Speaker: anchor, Text: strings.Join(heads, ". ") + "."}}
	case agent.NewsSegmentClosing:
		// Let nextClosing use the anchor's live detail-free sign-off prompt. A
		// headline-only fallback would still repeat a single-story broadcast.
		return nil
	default:
		parts := []string{strings.TrimSpace(req.Headline), strings.TrimSpace(req.Summary)}
		var kept []string
		for _, s := range parts {
			if s != "" {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			return nil
		}
		lines := []agent.NewsScriptLine{{Speaker: anchor, Text: strings.Join(kept, " ")}}
		for i, speaker := range req.AddOnSpeakers {
			text := ""
			if len(req.KeyFacts) > 0 {
				text = strings.TrimSpace(req.KeyFacts[i%len(req.KeyFacts)])
			}
			if text == "" {
				text = strings.TrimSpace(req.Summary)
			}
			if text != "" {
				lines = append(lines, agent.NewsScriptLine{Speaker: speaker, Text: text})
			}
		}
		return lines
	}
}

func (p *NewsPlanner) speakerByName(name string) agent.Agent {
	if p.registry.Host != nil && p.registry.Host.Name() == name {
		return p.registry.Host
	}
	for _, d := range p.registry.Discussants {
		if d.Name() == name {
			return d
		}
	}
	return p.registry.Host
}

// commitNextCommentator advances the round-robin cursor across the co-hosts
// and returns the pick (used only for listener-answer handoffs).
func (p *NewsPlanner) commitNextCommentator() agent.Agent {
	ds := p.registry.Discussants
	if len(ds) == 0 {
		return nil
	}
	ag := ds[p.state.rrIdx%len(ds)]
	p.state.rrIdx++
	return ag
}

// Next produces the next Turn or returns false to end the broadcast.
func (p *NewsPlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	p.startFeeder(ctx)

	queued, end := p.queue.drain()
	if end {
		p.state.endRequested = true
	}

	// A co-host must answer the question the anchor just paraphrased. The
	// anchor's address-user directive already named this co-host on-air, so
	// route the answer to exactly that agent.
	if p.state.pendingAnswerUser {
		text := p.state.pendingAnswerUserText
		p.state.pendingAnswerUser = false
		p.state.pendingAnswerUserText = ""
		ag := p.pendingAnswerAgent
		p.pendingAnswerAgent = nil
		if ag == nil {
			ag = p.commitNextCommentator()
		}
		if ag != nil {
			for _, q := range queued {
				p.queue.push(q)
			}
			return p.makeTurn(ag, "answer-user:"+text, p.segmentSeconds()), true
		}
	}

	// Listener messages take priority over the script: the anchor
	// paraphrases, a co-host answers on the next call, then the script
	// resumes exactly where it left off.
	if len(queued) > 0 && !p.state.endRequested && !p.state.closingStarted {
		debounceCtx, cancel := context.WithTimeout(ctx, userDebounceWindow)
		<-debounceCtx.Done()
		cancel()
		if more, e := p.queue.drain(); len(more) > 0 {
			queued = append(queued, more...)
			if e {
				p.state.endRequested = true
			}
		} else if e {
			p.state.endRequested = true
		}
		parts := make([]string, len(queued))
		for i, m := range queued {
			if m.Username != "" {
				parts[i] = m.Username + ": " + m.Text
			} else {
				parts[i] = m.Text
			}
		}
		text := strings.Join(parts, " | ")
		p.state.pendingAnswerUser = true
		p.state.pendingAnswerUserText = text
		// Pick the answerer NOW and embed their name in the anchor directive
		// so the co-host the anchor names on-air is the one who answers.
		ans := p.commitNextCommentator()
		p.pendingAnswerAgent = ans
		directive := "address-user:" + text
		if ans != nil {
			directive += "\n[hand off to: " + ans.Name() + "]"
		}
		return p.makeTurn(p.registry.Host, directive, p.budgetSeconds(20)), true
	}

	if p.state.endRequested || p.state.closingStarted {
		return p.nextClosing(ctx)
	}
	return p.nextScripted(ctx)
}

// nextScripted replays the pre-generated script line by line. Between
// segments it checks the clock — a fresh story never starts with less than
// newsClosingThreshold remaining — and switches to the sign-off once the
// rundown is exhausted.
func (p *NewsPlanner) nextScripted(ctx context.Context) (*Turn, bool) {
	for p.curIdx >= len(p.cur) {
		if p.state.introFetched && p.tracker.Remaining() < newsClosingThreshold {
			return p.nextClosing(ctx)
		}
		select {
		case seg, ok := <-p.segCh:
			if !ok {
				return p.nextClosing(ctx)
			}
			if p.state.introFetched {
				p.state.phase = agent.PhaseFreeSpeech
			}
			p.state.introFetched = true
			p.cur, p.curIdx = seg, 0
		case <-ctx.Done():
			return nil, false
		}
	}
	line := p.cur[p.curIdx]
	p.curIdx++
	ag := p.speakerByName(line.Speaker)
	if ag == nil {
		return p.nextScripted(ctx)
	}
	return p.makeTurn(ag, agent.ScriptedDirectivePrefix+line.Text, p.segmentSeconds()), true
}

// nextClosing plays the pre-generated sign-off lines, then ends the show. If
// the feeder hasn't produced the sign-off yet (an early /end), it waits — the
// sign-off is generated right after the intro, so the wait is short. An empty
// sign-off script falls back to one live "closing" turn by the anchor.
func (p *NewsPlanner) nextClosing(ctx context.Context) (*Turn, bool) {
	if !p.state.closingStarted {
		p.state.closingStarted = true
		p.state.phase = agent.PhaseClosing
		select {
		case lines := <-p.closingCh:
			p.state.closingLines = lines
		case <-ctx.Done():
			return nil, false
		}
		if len(p.state.closingLines) == 0 && p.registry.Host != nil {
			p.state.phase = agent.PhaseEnded
			return p.makeTurn(p.registry.Host, "closing", p.budgetSeconds(newsClosingSeconds)), true
		}
	}
	if p.state.closingIdx < len(p.state.closingLines) {
		line := p.state.closingLines[p.state.closingIdx]
		p.state.closingIdx++
		if p.state.closingIdx == len(p.state.closingLines) {
			// The final sign-off line carries the Ended phase so the UI's
			// phase chip flips with the last words, mirroring the other
			// planners' sign-off turns.
			p.state.phase = agent.PhaseEnded
		}
		ag := p.speakerByName(line.Speaker)
		if ag == nil {
			return nil, false
		}
		return p.makeTurn(ag, agent.ScriptedDirectivePrefix+line.Text, p.budgetSeconds(newsClosingSeconds)), true
	}
	return nil, false
}

func (p *NewsPlanner) makeTurn(ag agent.Agent, directive string, budget time.Duration) *Turn {
	p.turnN++
	return &Turn{
		ID:        p.turnN,
		Phase:     p.state.phase,
		Speaker:   ag,
		Directive: directive,
		Budget:    budget,
		TextOut:   make(chan string, 16),
	}
}

func (p *NewsPlanner) segmentSeconds() time.Duration {
	if p.topic == nil || p.topic.SegmentMaxSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.topic.SegmentMaxSeconds) * time.Second
}

func (p *NewsPlanner) budgetSeconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
