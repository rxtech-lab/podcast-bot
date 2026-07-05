package contentcreator

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tools"
)

const endAudioBookToolName = "end_audio_book"

type audioBookEndState struct {
	requested     atomic.Bool
	everRequested atomic.Bool
	done          atomic.Bool
}

func (s *audioBookEndState) RequestDone() {
	if s != nil {
		s.requested.Store(true)
		s.everRequested.Store(true)
	}
}

// EverRequested reports whether the narrator has asked to end at least once,
// including requests the backend later rejected. Past this point the loop is
// living on borrowed time — guards should fail toward stopping.
func (s *audioBookEndState) EverRequested() bool {
	return s != nil && s.everRequested.Load()
}

func (s *audioBookEndState) EndRequested() bool {
	return s != nil && s.requested.Load()
}

func (s *audioBookEndState) ClearRequest() {
	if s != nil {
		s.requested.Store(false)
	}
}

func (s *audioBookEndState) MarkDone() {
	if s != nil {
		s.requested.Store(false)
		s.done.Store(true)
	}
}

func (s *audioBookEndState) Done() bool {
	return s != nil && s.done.Load()
}

type endAudioBookTool struct {
	state *audioBookEndState
}

func (t endAudioBookTool) Name() string { return endAudioBookToolName }

func (t endAudioBookTool) Description() string {
	return "Call exactly once, only after the final planned audiobook chapter has been fully narrated. The backend will not mark audiobook narration complete until this tool is called."
}

func (t endAudioBookTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t endAudioBookTool) Call(_ context.Context, _ map[string]any, _ tools.AgentContext) (string, error) {
	t.state.RequestDone()
	return "audiobook completion requested; backend will verify the final planned beat before stopping", nil
}

// AudioBookPlanner keeps asking the narrator to continue until the narrator
// explicitly calls end_audio_book. A clean LLM stream close is not enough to
// finish the audiobook, because providers can stop mid-story without surfacing
// an error.
type AudioBookPlanner struct {
	topic    *config.DebateTopic
	registry *agent.Registry
	state    *audioBookEndState
	turnN    int
	// endRejections counts end_audio_book requests refused because the final
	// scene marker had not been reached. Accessed only from the producer
	// goroutine (via ValidateEndAfterTurn).
	endRejections int

	boundaryReason atomic.Value
	boundaryJudge  func(context.Context, string, string, int) (audioBookBoundaryDecision, bool)
}

// maxAudioBookEndRejections caps how many times the backend may refuse an
// end_audio_book request over a missing final scene marker. Past the cap the
// narrator clearly believes the book is done, and holding the loop open for
// a marker only produces filler narration — accept the end instead.
const maxAudioBookEndRejections = 2

func NewAudioBookPlanner(topic *config.DebateTopic, reg *agent.Registry, state *audioBookEndState) *AudioBookPlanner {
	return &AudioBookPlanner{topic: topic, registry: reg, state: state}
}

func (p *AudioBookPlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil || p.Done() || p.registry == nil || p.registry.SeriesHost == nil {
		return nil, false
	}
	p.turnN++
	directive := "narrate"
	if p.turnN > 1 {
		directive = "narrate continuation: Continue exactly from where the previous audiobook narration stopped. Do not restart, recap, summarize earlier material, or add filler. Keep following the planned chapter order. If the recent transcript already fully narrated the final planned chapter, call end_audio_book immediately with no spoken text. Otherwise call end_audio_book exactly once as soon as the final planned chapter is fully narrated."
		if p.endRejections > 0 {
			directive += " Your previous end_audio_book call was refused because the final illustration scene markers were missing. Do not narrate new story material: emit the remaining <scene N/> markers in order, each immediately before one short sentence that closes the matching beat, then call end_audio_book."
		}
	}
	if boundary := p.chapterBoundaryInstruction(); boundary != "" {
		directive = strings.TrimSpace(directive + "\n\n" + boundary)
	}
	budget := 30 * time.Minute
	if p.topic != nil && p.topic.TotalMinutes > 0 {
		budget = time.Duration(p.topic.TotalMinutes) * time.Minute
	}
	return &Turn{
		ID:        p.turnN,
		Phase:     agent.PhaseFreeSpeech,
		Speaker:   p.registry.SeriesHost,
		Directive: strings.TrimSpace(directive),
		Budget:    budget,
		TextOut:   make(chan string, 16),
	}, true
}

func (p *AudioBookPlanner) Done() bool {
	return p != nil && p.state.Done()
}

func (p *AudioBookPlanner) ValidateEndAfterTurn(maxSceneIndex, requiredFinalSceneIndex int) (requested, accepted bool) {
	if p == nil || p.state == nil || !p.state.EndRequested() {
		return false, false
	}
	if requiredFinalSceneIndex > 0 && maxSceneIndex < requiredFinalSceneIndex &&
		p.endRejections < maxAudioBookEndRejections {
		p.endRejections++
		p.state.ClearRequest()
		return true, false
	}
	p.state.MarkDone()
	return true, true
}

func (p *AudioBookPlanner) ForceDoneAtChapterBoundary() {
	if p != nil && p.state != nil {
		p.state.MarkDone()
	}
}

func (p *AudioBookPlanner) ReviewAudioBookLoop(ctx context.Context, generated string) bool {
	first, last, ok := p.chapterRange()
	if !ok || strings.TrimSpace(generated) == "" {
		return false
	}
	decision, ok := p.judgeChapterBoundary(ctx, "", generated, first, last)
	if !ok {
		// The judge is advisory while narration is still progressing, but
		// once the narrator has asked to end at least once, a judge failure
		// must not silently keep the loop alive — that is exactly the state
		// where the loop degenerates into sign-off filler.
		if p.state.EverRequested() {
			p.setBoundaryReason("boundary judge unavailable after narrator requested end")
			p.ForceDoneAtChapterBoundary()
			return true
		}
		return false
	}
	p.setBoundaryReason(decision.Reason)
	switch decision.Action {
	case "stop":
		p.ForceDoneAtChapterBoundary()
		return true
	default:
		return false
	}
}

func (p *AudioBookPlanner) ChapterBoundaryReason() string {
	if p == nil {
		return ""
	}
	if v := p.boundaryReason.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (p *AudioBookPlanner) setBoundaryReason(reason string) {
	if p != nil {
		p.boundaryReason.Store(strings.TrimSpace(reason))
	}
}

func (p *AudioBookPlanner) chapterBoundaryInstruction() string {
	first, last, ok := p.chapterRange()
	if !ok {
		return ""
	}
	if first == last {
		return "Chapter boundary: this generation run may narrate only Chapter " + strconv.Itoa(last) + ". The final planned chapter is Chapter " + strconv.Itoa(last) + ". Never invent, preview, title, or continue into any later chapter. Once Chapter " + strconv.Itoa(last) + " is complete, call end_audio_book immediately."
	}
	return "Chapter boundary: this generation run may narrate only Chapters " + strconv.Itoa(first) + " through " + strconv.Itoa(last) + ". The final planned chapter is Chapter " + strconv.Itoa(last) + ". Never invent, preview, title, or continue into any later chapter. Once Chapter " + strconv.Itoa(last) + " is complete, call end_audio_book immediately."
}

func (p *AudioBookPlanner) finalChapterNumber() (int, bool) {
	_, last, ok := p.chapterRange()
	return last, ok
}

func (p *AudioBookPlanner) chapterRange() (first, last int, ok bool) {
	if p == nil || p.topic == nil {
		return 0, 0, false
	}
	if len(p.topic.AudioBookChapterIndices) > 0 {
		for _, idx := range p.topic.AudioBookChapterIndices {
			if idx <= 0 {
				continue
			}
			if !ok || idx < first {
				first = idx
			}
			if !ok || idx > last {
				last = idx
			}
			ok = true
		}
		return first, last, ok
	}
	if len(p.topic.AudioBookChapters) == 0 {
		return 0, 0, false
	}
	return 1, len(p.topic.AudioBookChapters), true
}

type audioBookBoundaryDecision = agent.AudioBookBoundaryDecision

func (p *AudioBookPlanner) judgeChapterBoundary(ctx context.Context, accepted, candidate string, firstChapter, finalChapter int) (audioBookBoundaryDecision, bool) {
	if p != nil && p.boundaryJudge != nil {
		if decision, ok := p.boundaryJudge(ctx, accepted, candidate, finalChapter); ok {
			decision.Reason = strings.TrimSpace(decision.Reason)
			decision.Action = normalizeBoundaryAction(decision.Action)
			return decision, true
		}
	}
	judge := p.boundaryJudgeAgent()
	if judge == nil {
		return audioBookBoundaryDecision{}, false
	}
	judgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	decision, err := judge.Review(
		judgeCtx,
		fmt.Sprintf("%d-%d", firstChapter, finalChapter),
		boundaryJudgeSnippet(p.boundaryJudgeOutline(), 1200),
		boundaryJudgeSnippet(accepted, 900),
		boundaryJudgeSnippet(candidate, 900),
	)
	if err != nil {
		return audioBookBoundaryDecision{}, false
	}
	decision.Action = normalizeBoundaryAction(decision.Action)
	decision.Reason = strings.TrimSpace(decision.Reason)
	return decision, true
}

func (p *AudioBookPlanner) boundaryJudgeAgent() *agent.AudioBookBoundaryJudge {
	if p == nil || p.registry == nil || p.registry.SeriesHost == nil {
		return nil
	}
	withClient, ok := p.registry.SeriesHost.(interface{ LLM() *llm.Client })
	if !ok {
		return nil
	}
	client := withClient.LLM()
	if client == nil {
		return nil
	}
	base := agent.NewBase("Audiobook Boundary Judge", agent.RoleJudgement, client, nil, nil, nil, nil)
	return agent.NewAudioBookBoundaryJudge(base)
}

func boundaryJudgeSnippet(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}

func (p *AudioBookPlanner) boundaryJudgeOutline() string {
	if p == nil || p.topic == nil {
		return ""
	}
	var sb strings.Builder
	if summary := strings.TrimSpace(p.topic.Background); summary != "" {
		sb.WriteString("Overall summary: ")
		sb.WriteString(summary)
		sb.WriteString("\n")
	}
	first, _, _ := p.chapterRange()
	for i, ch := range p.topic.AudioBookChapters {
		number := i + 1
		if len(p.topic.AudioBookChapterIndices) == len(p.topic.AudioBookChapters) {
			number = p.topic.AudioBookChapterIndices[i]
		} else if first > 0 {
			number = first + i
		}
		sb.WriteString("Chapter ")
		sb.WriteString(strconv.Itoa(number))
		sb.WriteString(": ")
		sb.WriteString(strings.TrimSpace(ch.Title))
		if summary := strings.TrimSpace(ch.Summary); summary != "" {
			sb.WriteString(" — ")
			sb.WriteString(summary)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func normalizeBoundaryAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "stop":
		return "stop"
	default:
		return "keep"
	}
}
