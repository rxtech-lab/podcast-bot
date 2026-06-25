package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Judgement is a silent fact-checker for panel-discussion turns. It is not
// scheduled as a speaker; the pipeline calls Analyze after a discussant turn
// and attaches the returned comment to the transcript when useful.
type Judgement struct{ *Base }

func NewJudgement(b *Base) *Judgement { return &Judgement{Base: b} }

func (j *Judgement) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	return nil, fmt.Errorf("judgement agent is silent and does not speak")
}

type JudgementResult struct {
	ShouldComment bool               `json:"should_comment"`
	Comment       string             `json:"comment"`
	Sources       []TranscriptSource `json:"sources,omitempty"`
}

func (j *Judgement) Analyze(ctx context.Context, topic string, line TranscriptLine, recent []TranscriptLine) (JudgementResult, error) {
	system := `You are the silent evidence judge for a live discussion podcast.
Your job is to check the latest speaker's factual claims. Use web/search tools when a claim is concrete, current, numerical, legal, medical, financial, or otherwise easy to verify. Do not comment on opinions, framing, or harmless uncertainty.
Be selective: most turns should return should_comment=false. Return should_comment=true only when the latest turn appears unsupported, materially wrong, or lacks evidence for an important factual claim.
Reply only as strict JSON: {"should_comment": bool, "comment": "<short natural host-facing note>", "sources": [{"title":"...", "url":"...", "snippet":"..."}]}.
When should_comment=true, write comment as one short sentence a host could naturally insert, e.g. "That point needs stronger evidence before we lean on it."`

	user := strings.Join([]string{
		"# Topic",
		topic,
		"",
		"# Recent transcript",
		fallback(formatRecent(recent), "(none)"),
		"",
		"# Latest turn to judge",
		fmt.Sprintf("%s: %s", line.Speaker, oneLine(line.Text)),
	}, "\n")

	hist := []llm.Message{{Role: llm.RoleUser, Content: user}}
	stream, err := j.llmC.StreamWithTools(ctx, system, hist, j.reg.AsOpenAIParams(),
		func(ctx context.Context, name, jsonArgs string) (string, error) {
			j.EmitActivity(classifyToolActivity(name), name)
			res, err := j.reg.Dispatch(ctx, name, jsonArgs, j)
			j.EmitActivity("speaking", "")
			return res, err
		})
	if err != nil {
		return JudgementResult{}, err
	}
	var b strings.Builder
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		b.WriteString(d.TextChunk)
	}
	if err := stream.Err(); err != nil {
		return JudgementResult{}, err
	}
	raw := cleanDirectorJSON(b.String())
	var out JudgementResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return JudgementResult{}, fmt.Errorf("decode judgement result: %w (raw=%s)", err, raw)
	}
	out.Comment = strings.TrimSpace(out.Comment)
	out.Sources = compactTranscriptSources(out.Sources)
	if !out.ShouldComment || out.Comment == "" {
		out.ShouldComment = false
		out.Comment = ""
	}
	return out, nil
}

func compactTranscriptSources(in []TranscriptSource) []TranscriptSource {
	seen := map[string]bool{}
	out := make([]TranscriptSource, 0, len(in))
	for _, s := range in {
		s.URL = strings.TrimSpace(s.URL)
		if s.URL == "" || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		s.Title = strings.TrimSpace(s.Title)
		s.Snippet = strings.TrimSpace(s.Snippet)
		out = append(out, s)
		if len(out) >= 5 {
			break
		}
	}
	return out
}
