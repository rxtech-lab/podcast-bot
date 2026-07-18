package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// This file is the dedicated news-broadcast AI flow. Unlike the discussion
// format — where every turn is an open-ended live LLM improvisation — a news
// broadcast is SCRIPTED: the NewsScriptWriter pre-writes each segment's
// on-air lines from the story rundown (so the desk tells the story instead of
// debating it, and audio production never stalls waiting for a model), and
// the NewsAnchor / NewsCommentator speakers replay those lines verbatim. The
// only live LLM turns left are listener interactions (address-user /
// answer-user), which cannot be known ahead of air.

// ScriptedDirectivePrefix marks a directive whose payload is the exact
// pre-written text the speaker must deliver.
const ScriptedDirectivePrefix = "scripted:"

// ScriptedPayload extracts the pre-written text from a "scripted:" directive.
func ScriptedPayload(directive string) (string, bool) {
	rest, ok := strings.CutPrefix(directive, ScriptedDirectivePrefix)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// NewsScriptLine is one on-air line of a pre-written broadcast segment.
type NewsScriptLine struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// NewsBeat names one co-host and the beat (focus area) they report from.
type NewsBeat struct {
	Name string
	Beat string
}

// Segment kinds the script writer knows how to write.
const (
	NewsSegmentIntro   = "intro"
	NewsSegmentStory   = "story"
	NewsSegmentClosing = "closing"
)

// NewsSegmentRequest describes one broadcast segment for the script writer.
type NewsSegmentRequest struct {
	Kind string

	// Story fields (Kind == NewsSegmentStory). StoryNumber is 1-based;
	// PrevHeadline (empty for the first story) prompts a one-sentence bridge.
	StoryNumber  int
	StoryTotal   int
	Headline     string
	Summary      string
	KeyFacts     []string
	PrevHeadline string

	// AddOnSpeakers are the co-hosts who must deliver this segment's add-ons,
	// pre-picked by the planner so airtime rotates fairly across the desk.
	AddOnSpeakers []string

	// TargetSeconds is the segment's approximate airtime budget.
	TargetSeconds int
}

// NewsScriptWriter pre-writes broadcast segments. It is not a speaking agent:
// it has no voice and never appears in the registry — the planner calls
// WriteSegment ahead of air and feeds the resulting lines to the speakers as
// scripted directives.
type NewsScriptWriter struct {
	llmC         *llm.Client
	anchor       string
	commentators []NewsBeat
	language     string
	background   string
	sourceDocs   string
	rundown      string
	headlines    []string
}

// NewNewsScriptWriter constructs the segment writer. rundown is the numbered
// full story list used for the intro and story scripts; headlines is the
// detail-free list used only for a multi-story sign-off. background/sourceDocs
// ground the substantive segments in the plan's research and uploaded material.
func NewNewsScriptWriter(llmC *llm.Client, anchor string, commentators []NewsBeat,
	language, background, sourceDocs, rundown string, headlines []string,
) *NewsScriptWriter {
	return &NewsScriptWriter{
		llmC:         llmC,
		anchor:       anchor,
		commentators: commentators,
		language:     language,
		background:   background,
		sourceDocs:   sourceDocs,
		rundown:      rundown,
		headlines:    append([]string(nil), headlines...),
	}
}

func (w *NewsScriptWriter) system() string {
	var roster strings.Builder
	fmt.Fprintf(&roster, "Anchor: %s\n", w.anchor)
	for _, c := range w.commentators {
		if b := strings.TrimSpace(c.Beat); b != "" {
			fmt.Fprintf(&roster, "Co-host: %s — beat: %s\n", c.Name, b)
		} else {
			fmt.Fprintf(&roster, "Co-host: %s\n", c.Name)
		}
	}
	lang := strings.TrimSpace(w.language)
	if lang == "" {
		lang = "the rundown's language"
	}
	return fmt.Sprintf(`You write the on-air script for a live radio news broadcast.
This is a NEWS BROADCAST, not a panel discussion and not a debate. Nobody argues, nobody trades opinions, nobody comments on another speaker's take. The desk tells the news story itself, in different roles:
- The anchor carries the broadcast: presents each story crisply — headline first, then the facts in a clear order, with dates and source attributions.
- When the anchor hands over by name, a co-host adds ONE short factual add-on from their beat: a key number, essential background, the timeline, or what happens next. Add-ons report additional facts; they never re-read the anchor's report, never evaluate what another speaker said, and never use debate language ("I agree", "I'd push back", "as X said").

The desk:
%s
Writing rules:
- Plain spoken prose only: no markdown, no stage directions, no sound cues, no headlines-as-titles.
- Attribute facts to their sources and keep the dates the material provides. If the material marks something unconfirmed, say on air that it is unconfirmed.
- Never invent facts beyond the material given in the request.
- Broadcast register: crisp, warm, professional. Short sentences that read well aloud.
- Write every line in %s.
Reply with STRICT JSON only: {"lines":[{"speaker":"<exact roster name>","text":"<what they say on air>"}]}. "speaker" must copy a roster name exactly.`, strings.TrimRight(roster.String(), "\n"), lang)
}

func (w *NewsScriptWriter) segmentPrompt(req NewsSegmentRequest) string {
	var b strings.Builder
	switch req.Kind {
	case NewsSegmentIntro:
		fmt.Fprintf(&b, "Write the broadcast's OPENING (about %d seconds of airtime). The anchor speaks alone: welcome listeners, preview today's top stories from the rundown below (headlines only, in order), and introduce each co-host by name and beat. ", req.TargetSeconds)
		if strings.TrimSpace(w.sourceDocs) != "" {
			b.WriteString("Mention in one sentence that today's coverage draws on the source material provided to the desk. ")
		}
		b.WriteString("End by leading into the first story.\n\n# Today's rundown\n")
		b.WriteString(w.rundown)
	case NewsSegmentClosing:
		fmt.Fprintf(&b, "Write the broadcast's SIGN-OFF (no more than %d seconds of airtime). The anchor speaks alone. ", req.TargetSeconds)
		if len(w.headlines) <= 1 {
			b.WriteString("Do NOT recap or repeat the headline, story, facts, dates, numbers, or source attributions; the audience just heard the only story. ")
		} else {
			b.WriteString("Recap only the headline names below in one short sentence. Do NOT repeat any story details, facts, dates, numbers, or source attributions.\n\n# Headline names only\n")
			for _, headline := range w.headlines {
				if headline = strings.TrimSpace(headline); headline != "" {
					fmt.Fprintf(&b, "- %s\n", headline)
				}
			}
		}
		b.WriteString("Thank the co-hosts by name and the listeners, then sign off like a news program in one or two short sentences total.")
	default: // NewsSegmentStory
		fmt.Fprintf(&b, "Write STORY SEGMENT %d of %d (about %d seconds of airtime total).\n", req.StoryNumber, req.StoryTotal, req.TargetSeconds)
		b.WriteString("Structure:\n")
		if req.PrevHeadline != "" {
			fmt.Fprintf(&b, "1. The anchor opens with a one-sentence bridge out of the previous story (%q) into this one.\n", req.PrevHeadline)
		}
		b.WriteString("2. The anchor's report: the headline, then the summary and key facts in a clear order with their dates and attributions — 5 to 9 sentences.\n")
		if len(req.AddOnSpeakers) > 0 {
			fmt.Fprintf(&b, "3. The anchor hands over BY NAME to each of these co-hosts in turn — %s — and each delivers one short factual add-on (2-4 sentences) from their beat: extra facts, background, numbers, or what to watch next. Additive reporting only. REQUIRED OUTPUT CONTRACT: the JSON lines array must contain at least one non-empty line for EVERY listed co-host, using each exact name; omitting any listed co-host is invalid.\n", strings.Join(req.AddOnSpeakers, ", "))
		}
		b.WriteString("4. The anchor closes the segment in one sentence.\n\n# The story\n")
		fmt.Fprintf(&b, "Headline: %s\nSummary: %s\n", req.Headline, req.Summary)
		for _, f := range req.KeyFacts {
			if f = strings.TrimSpace(f); f != "" {
				fmt.Fprintf(&b, "- %s\n", f)
			}
		}
	}
	// A sign-off is intentionally not grounded with the full research payload:
	// supplying it here invites the model to report the story a second time.
	if req.Kind != NewsSegmentClosing {
		if bg := strings.TrimSpace(w.background); bg != "" {
			b.WriteString("\n\n# Background (shared desk research)\n")
			b.WriteString(bg)
		}
		if docs := strings.TrimSpace(w.sourceDocs); docs != "" {
			b.WriteString("\n\n# Source documents (the user's original uploaded material — primary source, quote short passages verbatim with attribution when they carry the story)\n")
			b.WriteString(docs)
		}
	}
	return b.String()
}

// WriteSegment writes one segment's on-air lines. Lines from speakers not on
// the roster are re-attributed to the anchor; empty lines are dropped.
func (w *NewsScriptWriter) WriteSegment(ctx context.Context, req NewsSegmentRequest) ([]NewsScriptLine, error) {
	raw, err := w.llmC.JSON(ctx, w.system(), w.segmentPrompt(req))
	if err != nil {
		return nil, err
	}
	var out struct {
		Lines []NewsScriptLine `json:"lines"`
	}
	if err := json.Unmarshal([]byte(cleanDirectorJSON(string(raw))), &out); err != nil {
		return nil, fmt.Errorf("decode news segment script: %w", err)
	}
	return w.ensureRequiredAddOns(req, w.sanitize(out.Lines)), nil
}

// sanitize drops empty lines and re-attributes unknown speakers to the anchor
// so a hallucinated name can never crash the roster lookup downstream.
func (w *NewsScriptWriter) sanitize(lines []NewsScriptLine) []NewsScriptLine {
	known := map[string]string{strings.ToLower(strings.TrimSpace(w.anchor)): w.anchor}
	for _, c := range w.commentators {
		known[strings.ToLower(strings.TrimSpace(c.Name))] = c.Name
	}
	out := make([]NewsScriptLine, 0, len(lines))
	for _, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		speaker, ok := known[strings.ToLower(strings.TrimSpace(l.Speaker))]
		if !ok {
			speaker = w.anchor
		}
		out = append(out, NewsScriptLine{Speaker: speaker, Text: text})
	}
	return out
}

// ensureRequiredAddOns guarantees that every co-host selected by the planner
// gets an on-air line even when the script model omits one from otherwise-valid
// JSON. The fallback is copied verbatim from the planned story material, which
// is already in the broadcast language, so this guard never invents a fact.
func (w *NewsScriptWriter) ensureRequiredAddOns(req NewsSegmentRequest, lines []NewsScriptLine) []NewsScriptLine {
	if req.Kind != NewsSegmentStory || len(req.AddOnSpeakers) == 0 {
		return lines
	}
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		seen[strings.ToLower(strings.TrimSpace(line.Speaker))] = true
	}
	for i, name := range req.AddOnSpeakers {
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		text := ""
		if len(req.KeyFacts) > 0 {
			text = strings.TrimSpace(req.KeyFacts[i%len(req.KeyFacts)])
		}
		if text == "" {
			text = strings.TrimSpace(req.Summary)
		}
		if text == "" {
			text = strings.TrimSpace(req.Headline)
		}
		if text != "" {
			lines = append(lines, NewsScriptLine{Speaker: name, Text: text})
			seen[key] = true
		}
	}
	return lines
}

// NewsAnchor is the news broadcast's anchor. Scripted directives replay the
// pre-written text with zero model latency; the only live LLM turns are
// listener interactions (address-user) and the fallback closing.
type NewsAnchor struct {
	*Base
	system string
}

// NewNewsAnchor builds the anchor. roster lists everyone on air with exact
// name spellings; rundown is the numbered story list — both ground the
// anchor's live (non-scripted) turns.
func NewNewsAnchor(b *Base, roster, rundown string) *NewsAnchor {
	system := newsAnchorSystem
	if r := strings.TrimSpace(roster); r != "" {
		system += "\n\nThe people on air with you. These are the ONLY names you may use — copy their exact spellings:\n" + r
	}
	if rd := strings.TrimSpace(rundown); rd != "" {
		system += "\n\nToday's rundown (already covered or being covered on air):\n" + rd
	}
	return &NewsAnchor{Base: b, system: system}
}

const newsAnchorSystem = `You are the anchor of a live radio news broadcast.
Style: crisp, authoritative, warm — a professional broadcast-newsroom register. Plain prose only; never narrate stage directions or emit markdown.
This is a news broadcast, not a discussion: you present and steer, you never debate.
Responsibilities by directive:
- address-user:<text>: a listener wrote in — the audience may have sent one or multiple questions/comments separated by " | ". Paraphrase them early in one short sentence, then hand the floor to the co-host named in "[hand off to: <name>]". Do NOT answer yourself. Keep this entire turn to two short sentences.
- closing: thank the co-hosts by name and the listeners, then sign off like a news program. Do not recap or repeat any headline or story detail.
HARD RULE: when a directive names the next speaker, address and hand off to exactly that person — never call on anyone else, and never invent a name that is not on your roster.
Keep each response under 4 sentences.`

// Speak replays a scripted line verbatim (no model call) or runs the live
// anchor prompt for listener interactions.
func (a *NewsAnchor) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	if text, ok := ScriptedPayload(p.Instructions); ok {
		return llm.NewStaticStream(text), nil
	}
	return a.runStream(ctx, a.system, p)
}

// NewsCommentator is a co-host on the news desk. Scripted add-ons replay the
// pre-written text; the only live LLM turn is answering a listener question
// the anchor handed over.
type NewsCommentator struct {
	*Base
	system string
}

// NewNewsCommentator builds a co-host. focus is the beat they report from;
// roster lists everyone on air with exact name spellings.
func NewNewsCommentator(b *Base, focus, roster string) *NewsCommentator {
	return &NewsCommentator{Base: b, system: newsCommentatorSystem(b.Name(), focus, roster)}
}

func newsCommentatorSystem(name, focus, roster string) string {
	beat := strings.TrimSpace(focus)
	if beat == "" {
		beat = "general assignment"
	}
	system := fmt.Sprintf(`You are %s, a co-host on a live radio news broadcast. Your beat: %s.
This is a news broadcast, not a discussion or debate: you report facts, you never argue or grade other speakers' takes.
Accuracy rules: attribute facts to their sources and include dates when you have them. Never present unconfirmed reports as confirmed. If you do not know, say plainly what is and is not known — never invent facts.
Source grounding rule: when a "Source documents" section is present in your prompt, it is the user's original uploaded material — ground your answer in it and quote a short passage verbatim (attributed by document name) when it directly answers the question.
Output rules: plain prose only — no stage directions, no markdown.
Directives:
  "answer-user:<text>" — a listener asked the supplied question and the anchor handed you the floor. Answer it directly in 2-3 sentences with facts from the rundown, the source documents, or the transcript, then hand back to the anchor by name.
  "closing" — one sentence wrapping up your part of today's coverage.`, name, beat)
	if r := strings.TrimSpace(roster); r != "" {
		system += "\n\nThe people on air with you. When you address someone, use ONLY these names with their exact spellings:\n" + r
	}
	return system
}

// Speak replays a scripted line verbatim (no model call) or runs the live
// co-host prompt for listener questions.
func (c *NewsCommentator) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	if text, ok := ScriptedPayload(p.Instructions); ok {
		return llm.NewStaticStream(text), nil
	}
	return c.runStream(ctx, c.system, p)
}
