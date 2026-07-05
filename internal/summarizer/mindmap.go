package summarizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

const maxMindmapSourceChars = 45_000

// Generation-time shape limits. The model output is pruned to these; user
// edits are validated against the looser caps in ValidateMindmapSpec.
const (
	mindmapGenMaxDepth    = 4
	mindmapGenMaxChildren = 8
	mindmapGenMaxNodes    = 120
	mindmapGenTitleMax    = 60
	mindmapGenNoteMax     = 200
)

// User-edit limits (looser than generation so edits are not blocked).
const (
	mindmapUserMaxDepth = 8
	mindmapUserMaxNodes = 500
	mindmapUserTitleMax = 200
	mindmapUserNoteMax  = 500
)

// MindmapNode is one node of the discussion mindmap tree.
type MindmapNode struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Note     string         `json:"note,omitempty"`
	Children []*MindmapNode `json:"children,omitempty"`
}

// MindmapSpec is the JSON contract stored as the "mindmap" summary document
// body and served to clients as a typed tree.
type MindmapSpec struct {
	Version int          `json:"version"`
	Root    *MindmapNode `json:"root"`
}

// MindmapGenerator creates a mindmap node tree from a discussion transcript.
type MindmapGenerator struct {
	client *llm.Client
	env    *config.Env
}

// NewMindmapGenerator builds a mindmap generator using PodcastSummaryModel,
// falling back to HostModel.
func NewMindmapGenerator(env *config.Env) *MindmapGenerator {
	if env == nil {
		return &MindmapGenerator{}
	}
	model := strings.TrimSpace(env.PodcastSummaryModel)
	if model == "" {
		model = strings.TrimSpace(env.HostModel)
	}
	client := llm.New(env.OpenAIBaseURL, env.OpenAIKey, model)
	return &MindmapGenerator{client: client, env: env}
}

// Model returns the model id the mindmap generator will use.
func (g *MindmapGenerator) Model() string {
	if g == nil || g.client == nil {
		return ""
	}
	return g.client.Model()
}

// WithUsageRecorder returns a generator whose LLM calls report usage.
func (g *MindmapGenerator) WithUsageRecorder(record func(llm.Usage)) *MindmapGenerator {
	if g == nil || g.client == nil {
		return nil
	}
	next := *g
	next.client = g.client.WithUsageRecorder(record)
	if g.env != nil {
		next.client = next.client.WithPricing(g.env.LLMInputCostPerMillion, g.env.LLMOutputCostPerMillion)
	}
	return &next
}

// Generate creates and validates a MindmapSpec from the discussion transcript.
func (g *MindmapGenerator) Generate(ctx context.Context, in Input) (*MindmapSpec, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("mindmap generator not configured")
	}
	stream, err := g.client.Stream(ctx, mindmapSystemPrompt(in.Language), []llm.Message{{
		Role:    llm.RoleUser,
		Content: buildMindmapPrompt(in),
	}}, nil)
	if err != nil {
		return nil, fmt.Errorf("mindmap generator: %w", err)
	}
	var b strings.Builder
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		b.WriteString(d.TextChunk)
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("mindmap generator: %w", err)
	}
	spec, err := parseMindmapSpec(b.String())
	if err != nil {
		return nil, err
	}
	spec.normalize(in.Title)
	if err := ValidateMindmapSpec(spec, false); err != nil {
		return nil, err
	}
	return spec, nil
}

func mindmapSystemPrompt(language string) string {
	lang := strings.TrimSpace(language)
	if lang == "" {
		lang = "the podcast's language"
	}
	return `Create a mindmap JSON tree from a podcast discussion transcript.

Rules:
- Return only valid JSON. No Markdown fences, no commentary.
- Write all titles and notes in ` + lang + `.
- The root node is the discussion topic, phrased as a short headline.
- Level 1 under the root: 4 to 7 main themes or positions raised in the discussion.
- Deeper levels: concrete arguments, evidence, examples, and speaker stances. Attribute stances to speakers by name when the transcript names them; do not invent views.
- Titles are short noun phrases, at most ` + strconv.Itoa(mindmapGenTitleMax) + ` characters. Put any elaboration (1-2 sentences) in the node's "note".
- Maximum depth ` + strconv.Itoa(mindmapGenMaxDepth) + ` (root plus 3 levels). At most ` + strconv.Itoa(mindmapGenMaxChildren) + ` children per node.
- Give every node a short unique "id" (e.g. "n1", "n1a").
- JSON shape: {"version":1,"root":{"id":"root","title":"...","note":"...","children":[{"id":"n1","title":"...","note":"...","children":[...]}]}}.`
}

func buildMindmapPrompt(in Input) string {
	var sb strings.Builder
	sb.WriteString("Create a mindmap JSON tree for this podcast discussion.\n\n")
	if title := strings.TrimSpace(in.Title); title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", title)
	}
	if topic := strings.TrimSpace(in.Topic); topic != "" {
		fmt.Fprintf(&sb, "Topic: %s\n", topic)
	}
	if lang := strings.TrimSpace(in.Language); lang != "" {
		fmt.Fprintf(&sb, "Language: %s (write the mindmap in this language)\n", lang)
	}
	transcript := renderTranscript(in.Lines)
	if len(transcript) > maxMindmapSourceChars {
		transcript = transcript[:maxMindmapSourceChars]
	}
	sb.WriteString("\nTranscript:\n")
	sb.WriteString(transcript)
	return sb.String()
}

func parseMindmapSpec(raw string) (*MindmapSpec, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j >= i {
		raw = raw[i : j+1]
	}
	if raw == "" {
		return nil, errors.New("mindmap generator produced no JSON")
	}
	var spec MindmapSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("mindmap generator produced invalid JSON: %w", err)
	}
	return &spec, nil
}

// normalize trims text, assigns missing ids, and prunes the tree to the
// generation caps so a sloppy model output still yields a usable mindmap.
func (s *MindmapSpec) normalize(fallbackTitle string) {
	if s == nil {
		return
	}
	s.Version = 1
	if s.Root == nil {
		s.Root = &MindmapNode{}
	}
	s.Root.Title = trimText(s.Root.Title, mindmapGenTitleMax)
	if s.Root.Title == "" {
		s.Root.Title = trimText(fallbackTitle, mindmapGenTitleMax)
	}
	if s.Root.Title == "" {
		s.Root.Title = "Mindmap"
	}
	seen := map[string]bool{}
	count := 0
	var walk func(n *MindmapNode, depth int, path string)
	walk = func(n *MindmapNode, depth int, path string) {
		count++
		n.ID = strings.TrimSpace(n.ID)
		if n.ID == "" || seen[n.ID] {
			n.ID = path
		}
		seen[n.ID] = true
		n.Title = trimText(n.Title, mindmapGenTitleMax)
		n.Note = trimText(n.Note, mindmapGenNoteMax)
		if depth >= mindmapGenMaxDepth {
			n.Children = nil
			return
		}
		kept := make([]*MindmapNode, 0, len(n.Children))
		for i, c := range n.Children {
			if c == nil || strings.TrimSpace(c.Title) == "" {
				continue
			}
			if len(kept) >= mindmapGenMaxChildren || count >= mindmapGenMaxNodes {
				break
			}
			walk(c, depth+1, path+"."+strconv.Itoa(i+1))
			kept = append(kept, c)
		}
		n.Children = kept
	}
	walk(s.Root, 1, "n1")
}

// ValidateMindmapSpec checks structural integrity. With userLimits it applies
// the looser caps used for user edits; otherwise the generation caps apply.
func ValidateMindmapSpec(s *MindmapSpec, userLimits bool) error {
	if s == nil || s.Root == nil {
		return errors.New("mindmap is empty")
	}
	maxDepth, maxNodes, titleMax, noteMax := mindmapGenMaxDepth, mindmapGenMaxNodes, mindmapGenTitleMax, mindmapGenNoteMax
	if userLimits {
		maxDepth, maxNodes, titleMax, noteMax = mindmapUserMaxDepth, mindmapUserMaxNodes, mindmapUserTitleMax, mindmapUserNoteMax
	}
	if strings.TrimSpace(s.Root.Title) == "" {
		return errors.New("mindmap root is missing a title")
	}
	seen := map[string]bool{}
	count := 0
	var walk func(n *MindmapNode, depth int) error
	walk = func(n *MindmapNode, depth int) error {
		if n == nil {
			return errors.New("mindmap contains a null node")
		}
		if depth > maxDepth {
			return fmt.Errorf("mindmap exceeds max depth %d", maxDepth)
		}
		count++
		if count > maxNodes {
			return fmt.Errorf("mindmap exceeds max node count %d", maxNodes)
		}
		id := strings.TrimSpace(n.ID)
		if id == "" {
			return errors.New("mindmap node is missing an id")
		}
		if seen[id] {
			return fmt.Errorf("mindmap node id %q is duplicated", id)
		}
		seen[id] = true
		if strings.TrimSpace(n.Title) == "" {
			return fmt.Errorf("mindmap node %q is missing a title", id)
		}
		if len([]rune(n.Title)) > titleMax {
			return fmt.Errorf("mindmap node %q title exceeds %d characters", id, titleMax)
		}
		if len([]rune(n.Note)) > noteMax {
			return fmt.Errorf("mindmap node %q note exceeds %d characters", id, noteMax)
		}
		for _, c := range n.Children {
			if err := walk(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(s.Root, 1)
}
