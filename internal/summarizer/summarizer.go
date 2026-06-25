// Package summarizer runs a post-generation agent loop that writes a
// professional Markdown summary document for a finished podcast. The agent
// reads the full transcript and composes the summary across one or more
// write_summary_chunk tool calls (so a long document is never crammed into a
// single tool argument), then commits it with a terminal finalize_summary call.
//
// The summary leads with a Mermaid `flowchart TD` overview of the topics and
// ideas, then breaks down each participant's opinion, evidence, and sources,
// and closes with the points of agreement/disagreement and a conclusion. It is
// rendered client-side with a non-streaming Markdown view.
package summarizer

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// maxSummaryRounds caps the assistant↔tool ping-pong so a model that keeps
// re-calling tools can never loop forever. Generous enough for several
// write_summary_chunk calls plus the terminal finalize_summary.
const maxSummaryRounds = 16

// maxTranscriptChars bounds how much transcript text is fed to the model in a
// single run. Beyond this the transcript is segmented into labelled parts so the
// model still sees the whole conversation in clearly chunked form.
const maxTranscriptChars = 240_000

// transcriptSegmentChars is the target size of each labelled transcript segment
// when the conversation is long enough to chunk.
const transcriptSegmentChars = 40_000

// Line is one transcript turn handed to the summarizer.
type Line struct {
	Speaker string
	Role    string
	Text    string
}

// Input is everything the summarizer needs to write a summary.
type Input struct {
	Title    string
	Topic    string
	Language string
	Lines    []Line
}

// Result is the finished summary document.
type Result struct {
	Markdown string
}

// Generator wraps an LLM client configured with the podcast-summary model.
type Generator struct {
	client *llm.Client
	env    *config.Env
}

// New builds a Generator using the configured PodcastSummaryModel (falling back
// to HostModel is handled at config load time).
func New(env *config.Env) *Generator {
	model := env.PodcastSummaryModel
	if strings.TrimSpace(model) == "" {
		model = env.HostModel
	}
	client := llm.New(env.OpenAIBaseURL, env.OpenAIKey, model)
	return &Generator{client: client, env: env}
}

// Model returns the model id the summarizer will use.
func (g *Generator) Model() string {
	if g == nil || g.client == nil {
		return ""
	}
	return g.client.Model()
}

// WithUsageRecorder returns a Generator whose LLM calls report usage to record,
// applying the same pricing fallback the planner uses so cost is filled in even
// when the provider omits it from the usage payload.
func (g *Generator) WithUsageRecorder(record func(llm.Usage)) *Generator {
	if g == nil {
		return nil
	}
	next := *g
	next.client = g.client.
		WithUsageRecorder(record).
		WithPricing(g.env.LLMInputCostPerMillion, g.env.LLMOutputCostPerMillion)
	return &next
}

// Generate runs the agent loop and returns the assembled summary Markdown.
func (g *Generator) Generate(ctx context.Context, in Input) (*Result, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("summarizer not configured")
	}
	session := &summarySession{}
	system := summarySystemPrompt(in.Language)
	msgs := []llm.Message{{Role: llm.RoleUser, Content: buildUserPrompt(in)}}

	for round := 0; round < maxSummaryRounds; round++ {
		stream, err := g.client.Stream(ctx, system, msgs, summaryTools())
		if err != nil {
			return nil, fmt.Errorf("summary agent: %w", err)
		}
		var assistantText strings.Builder
		var tcDeltas []llm.DeltaToolCall
		for d := range stream.Deltas() {
			if d.Done {
				break
			}
			if d.TextChunk != "" {
				assistantText.WriteString(d.TextChunk)
			}
			if d.ToolCall != nil {
				tcDeltas = append(tcDeltas, *d.ToolCall)
			}
		}
		if err := stream.Err(); err != nil {
			return nil, fmt.Errorf("summary agent: %w", err)
		}

		calls := llm.AssembleToolCalls(tcDeltas)
		if len(calls) == 0 {
			// The model stopped calling tools. If it already wrote chunks, accept
			// what we have; otherwise nudge it once by treating an empty turn as a
			// failure to make progress.
			if session.hasParts() {
				return &Result{Markdown: session.assemble()}, nil
			}
			return nil, fmt.Errorf("summary agent produced no summary")
		}

		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   assistantText.String(),
			ToolCalls: calls,
		})
		for _, tc := range calls {
			result, terminal := session.dispatch(tc.Name, tc.Arguments)
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
			if terminal {
				return &Result{Markdown: session.assemble()}, nil
			}
		}
	}

	if session.hasParts() {
		return &Result{Markdown: session.assemble()}, nil
	}
	return nil, fmt.Errorf("summary agent did not finalize within %d rounds", maxSummaryRounds)
}

// buildUserPrompt renders the discussion metadata and the (possibly segmented)
// transcript the summarizer works from.
func buildUserPrompt(in Input) string {
	var sb strings.Builder
	sb.WriteString("Write a summary document for the following podcast.\n\n")
	if strings.TrimSpace(in.Title) != "" {
		fmt.Fprintf(&sb, "Title: %s\n", strings.TrimSpace(in.Title))
	}
	if strings.TrimSpace(in.Topic) != "" {
		fmt.Fprintf(&sb, "Topic: %s\n", strings.TrimSpace(in.Topic))
	}
	if strings.TrimSpace(in.Language) != "" {
		fmt.Fprintf(&sb, "Language: %s (write the summary in this language)\n", strings.TrimSpace(in.Language))
	}
	sb.WriteString("\n")

	transcript := renderTranscript(in.Lines)
	segments := segmentTranscript(transcript)
	if len(segments) <= 1 {
		sb.WriteString("Transcript:\n")
		sb.WriteString(transcript)
	} else {
		fmt.Fprintf(&sb, "The transcript is long and split into %d parts. Read every part before writing.\n", len(segments))
		for i, seg := range segments {
			fmt.Fprintf(&sb, "\n--- Transcript part %d/%d ---\n", i+1, len(segments))
			sb.WriteString(seg)
		}
	}
	return sb.String()
}

func renderTranscript(lines []Line) string {
	var sb strings.Builder
	for _, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		speaker := strings.TrimSpace(l.Speaker)
		if speaker == "" {
			speaker = "Speaker"
		}
		role := strings.TrimSpace(l.Role)
		if role != "" {
			fmt.Fprintf(&sb, "%s (%s): %s\n", speaker, role, text)
		} else {
			fmt.Fprintf(&sb, "%s: %s\n", speaker, text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// segmentTranscript splits an over-long transcript into labelled segments on
// line boundaries. A transcript within maxTranscriptChars is returned as a
// single segment.
func segmentTranscript(transcript string) []string {
	if len(transcript) <= maxTranscriptChars {
		return []string{transcript}
	}
	var segments []string
	var cur strings.Builder
	for _, line := range strings.Split(transcript, "\n") {
		if cur.Len() > 0 && cur.Len()+len(line)+1 > transcriptSegmentChars {
			segments = append(segments, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if strings.TrimSpace(cur.String()) != "" {
		segments = append(segments, strings.TrimSpace(cur.String()))
	}
	return segments
}
