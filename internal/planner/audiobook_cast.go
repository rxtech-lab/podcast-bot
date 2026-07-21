package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Cast-extraction bounds: chapters longer than the chunk limit are walked in
// pieces so no single call overflows the model, and at most
// audioBookCastConcurrency chapter calls run at once.
const (
	audioBookCastChunkChars  = 24000
	audioBookCastConcurrency = 4
)

// extractedCharacter is one speaking voice found by walking a chapter's real
// text.
type extractedCharacter struct {
	Name        string `json:"name"`
	Gender      string `json:"gender"`
	Description string `json:"description"`
	Chapters    []int  `json:"-"`
}

const audioBookCastSystem = `You identify the speaking cast in a book excerpt for audiobook voice casting.

Return strict JSON: {"characters": [{"name": "...", "gender": "male"|"female", "description": "..."}]}.

Rules:
- Include only voices that actually speak or are directly quoted in THIS excerpt: named characters, interviewees, quoted speakers, recurring point-of-view voices.
- Skip unnamed, background, or purely mentioned people who never speak.
- Never include the book's narrator voice itself.
- gender is the voice gender for TTS casting, inferred from the text ("male" or "female"; pick the most plausible when unstated).
- description is a concrete voice-casting brief grounded in the text: approximate age, vocal tone and register, personality, speaking energy.
- Use the character's name exactly as the text spells it.
- Return {"characters": []} when no one speaks in the excerpt.`

// extractAudioBookCast walks every chapter's actual text with a bounded LLM
// map-reduce and returns the merged speaking cast. It is metered through the
// planner's usage recorder like every other planning call and emits progress
// per chapter so the planning stream stays live.
func (p *Planner) extractAudioBookCast(ctx context.Context, slices []chapterSlice) ([]extractedCharacter, error) {
	if len(slices) == 0 {
		return nil, nil
	}
	// No LLM configured (unit tests) or e2e fakellm mode: the walk would only
	// produce noise — the draft's cast stands on its own.
	if p.env == nil || p.env.E2EMode || strings.TrimSpace(p.env.OpenAIKey) == "" {
		return nil, nil
	}
	client := llm.New(p.env.OpenAIBaseURL, p.env.OpenAIKey, p.scriptModel())
	if p.usageRecorder != nil {
		client = client.WithUsageRecorder(p.usageRecorder)
	}

	type chapterResult struct {
		chapter int
		cast    []extractedCharacter
		err     error
	}
	results := make([]chapterResult, len(slices))
	sem := make(chan struct{}, audioBookCastConcurrency)
	var wg sync.WaitGroup
	for i, sl := range slices {
		wg.Add(1)
		go func(i int, sl chapterSlice) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p.emit("reading", fmt.Sprintf("Identifying characters in chapter %d/%d…", sl.Index, len(slices)))
			cast, err := extractChapterCast(ctx, client, sl.Content)
			results[i] = chapterResult{chapter: sl.Index, cast: cast, err: err}
		}(i, sl)
	}
	wg.Wait()

	merged := make(map[string]*extractedCharacter)
	var order []string
	genderVotes := make(map[string]map[string]int)
	failures := 0
	for _, r := range results {
		if r.err != nil {
			failures++
			continue
		}
		for _, c := range r.cast {
			name := strings.TrimSpace(c.Name)
			key := normalizedSpeakerName(name)
			if name == "" || key == "" {
				continue
			}
			cur, ok := merged[key]
			if !ok {
				cc := c
				cc.Name = name
				cc.Chapters = nil
				merged[key] = &cc
				cur = merged[key]
				order = append(order, key)
				genderVotes[key] = make(map[string]int)
			}
			if g := normalizeSpeakerGender(c.Gender); g != "" {
				genderVotes[key][g]++
			}
			if len(strings.TrimSpace(c.Description)) > len(strings.TrimSpace(cur.Description)) {
				cur.Description = strings.TrimSpace(c.Description)
			}
			cur.Chapters = append(cur.Chapters, r.chapter)
		}
	}
	if failures == len(slices) {
		return nil, fmt.Errorf("character extraction failed for all %d chapters", len(slices))
	}
	out := make([]extractedCharacter, 0, len(order))
	for _, key := range order {
		c := merged[key]
		c.Gender = majorityGender(genderVotes[key])
		sort.Ints(c.Chapters)
		out = append(out, *c)
	}
	return out, nil
}

func majorityGender(votes map[string]int) string {
	best, bestN := "", 0
	for g, n := range votes {
		if n > bestN || (n == bestN && g < best) {
			best, bestN = g, n
		}
	}
	return best
}

// extractChapterCast runs the extraction call over one chapter, chunking long
// chapters so each call stays bounded.
func extractChapterCast(ctx context.Context, client *llm.Client, content string) ([]extractedCharacter, error) {
	var cast []extractedCharacter
	chunks := chunkText(content, audioBookCastChunkChars)
	for _, chunk := range chunks {
		raw, err := client.JSON(ctx, audioBookCastSystem, "Book excerpt:\n\n"+chunk)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Characters []extractedCharacter `json:"characters"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("decode cast extraction result: %w", err)
		}
		cast = append(cast, parsed.Characters...)
	}
	return cast, nil
}

// chunkText splits text into pieces of at most limit bytes, preferring
// paragraph boundaries so quotes are not cut mid-exchange.
func chunkText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(text) > limit {
		cut := strings.LastIndex(text[:limit], "\n\n")
		if cut < limit/2 {
			cut = limit
		}
		chunks = append(chunks, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}
