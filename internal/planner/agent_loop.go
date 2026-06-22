package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

type planningAgentOptions struct {
	ResearchRequired         bool
	RequiredURLs             []string
	ExistingSources          []config.Source
	RequireSuccessfulURLRead bool
}

type planningToolSession struct {
	planner                  *Planner
	researchRequired         bool
	requiredURLs             []string
	requireSuccessfulURLRead bool
	sources                  []config.Source
	searched                 bool
	readURLs                 map[string]bool
	successfulURLReads       map[string]bool
	final                    *draft
}

func (p *Planner) runPlanningAgent(ctx context.Context, user string, attachments []Attachment, opts planningAgentOptions) (*draft, []config.Source, error) {
	client := llm.New(p.env.OpenAIBaseURL, p.env.OpenAIKey, p.scriptModel())
	session := &planningToolSession{
		planner:                  p,
		researchRequired:         opts.ResearchRequired,
		requiredURLs:             dedupeURLs(opts.RequiredURLs),
		requireSuccessfulURLRead: opts.RequireSuccessfulURLRead,
		sources:                  append([]config.Source(nil), opts.ExistingSources...),
		readURLs:                 map[string]bool{},
		successfulURLReads:       map[string]bool{},
	}

	const system = `You are a planning agent for a panel-discussion generator.

Run as an agent loop:
- Use tools to gather external context when required or useful.
- If web research is required, call web_search before creating the plan.
- If specific URLs are required, call read_url for each URL before creating the plan.
- After research/read_url tool results are returned, make one final assistant turn that calls only create_plan.
- Do not call create_plan in the same assistant turn as web_search or read_url.
- Do not output the plan as prose or JSON outside the create_plan tool call.

The final plan must be balanced, production-ready, and written in the requested language.`

	msgs := []llm.Message{{Role: llm.RoleUser, Parts: attachmentInputParts(user, attachments)}}
	for round := 0; round < maxPlanningToolRounds; round++ {
		// Status at the start of each turn: the model's "thinking" latency before
		// it commits to a tool is otherwise silent, which both looks stuck and
		// risks the client's idle timeout. Round 0 reflects whether we're about to
		// research; later rounds are usually composing the plan.
		if round == 0 {
			if opts.ResearchRequired {
				p.emit("thinking", "Researching the topic…")
			} else {
				p.emit("thinking", "Analyzing the topic…")
			}
		} else {
			p.emit("thinking", "Composing the plan…")
		}

		stream, err := client.Stream(ctx, system, msgs, planningTools())
		if err != nil {
			return nil, nil, fmt.Errorf("planning agent: %w", err)
		}

		var assistantText strings.Builder
		var tcDeltas []llm.DeltaToolCall
		// Emit a status the moment the model names a tool (the name streams ahead
		// of its arguments), and heartbeat while the long create_plan arguments
		// stream so the UI keeps moving and the SSE connection stays warm.
		activeTool := ""
		streamedArgs, nextTick := 0, planningHeartbeatBytes
		for d := range stream.Deltas() {
			if d.Done {
				break
			}
			if d.TextChunk != "" {
				assistantText.WriteString(d.TextChunk)
			}
			if d.ToolCall != nil {
				tcDeltas = append(tcDeltas, *d.ToolCall)
				if d.ToolCall.Name != "" && d.ToolCall.Name != activeTool {
					activeTool = d.ToolCall.Name
					streamedArgs, nextTick = 0, planningHeartbeatBytes
					p.emitToolStart(activeTool)
				}
				if activeTool == "create_plan" && d.ToolCall.Arguments != "" {
					streamedArgs += len(d.ToolCall.Arguments)
					if streamedArgs >= nextTick {
						nextTick += planningHeartbeatBytes
						p.emit("writing", "Writing the plan…")
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			return nil, nil, fmt.Errorf("planning agent: %w", err)
		}

		calls := llm.AssembleToolCalls(tcDeltas)
		if len(calls) == 0 {
			break
		}
		if err := validateCreatePlanTerminal(calls); err != nil {
			return nil, nil, err
		}

		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   assistantText.String(),
			ToolCalls: calls,
		})
		for _, tc := range calls {
			result, terminal, err := session.dispatch(ctx, tc.Name, tc.Arguments)
			if err != nil {
				return nil, nil, err
			}
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
			if terminal {
				return session.final, session.sources, nil
			}
		}
	}
	if session.final == nil {
		return nil, nil, fmt.Errorf("planning agent did not call create_plan")
	}
	return session.final, session.sources, nil
}

const maxPlanningToolRounds = 8

// planningHeartbeatBytes is how many bytes of streamed create_plan arguments to
// accumulate between "Writing the plan…" heartbeats. Small enough that the
// status refreshes (and the SSE connection stays warm) several times while a
// long plan streams, large enough not to spam.
const planningHeartbeatBytes = 1200

// emitToolStart surfaces a coarse status as soon as the model commits to a tool,
// before its (sometimes lengthy) arguments finish streaming.
func (p *Planner) emitToolStart(name string) {
	switch name {
	case "web_search":
		p.emit("search", "Searching the web…")
	case "read_url":
		p.emit("read", "Reading sources…")
	case "create_plan":
		p.emit("writing", "Writing the plan…")
	}
}

func planningTools() []openai.ChatCompletionToolParam {
	return []openai.ChatCompletionToolParam{
		toolDef("web_search", "Search the web through Firecrawl and return readable sources for planning.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query to research the discussion topic."},
			},
			"required": []string{"query"},
		}),
		toolDef("read_url", "Read a specific URL through Firecrawl and return clean markdown context.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "The http(s) URL to read."},
			},
			"required": []string{"url"},
		}),
		toolDef("create_plan", "Create the final panel-discussion plan. This must be the final tool call.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":      map[string]any{"type": "string"},
				"background": map[string]any{"type": "string", "description": "Two to four neutral paragraphs grounding the discussion."},
				"host": map[string]any{
					"type":       "object",
					"properties": map[string]any{"name": map[string]any{"type": "string"}},
					"required":   []string{"name"},
				},
				"discussants": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":   map[string]any{"type": "string"},
							"aspect": map[string]any{"type": "string"},
						},
						"required": []string{"name", "aspect"},
					},
					"minItems": 2,
				},
			},
			"required": []string{"title", "background", "host", "discussants"},
		}),
	}
}

func toolDef(name, description string, schema map[string]any) openai.ChatCompletionToolParam {
	return openai.ChatCompletionToolParam{
		Function: shared.FunctionDefinitionParam{
			Name:        name,
			Description: openai.String(description),
			Parameters:  schema,
		},
	}
}

func (s *planningToolSession) dispatch(ctx context.Context, name, jsonArgs string) (string, bool, error) {
	if s.final != nil {
		return "", false, fmt.Errorf("planning already finalized; %s is not allowed after create_plan", name)
	}
	switch name {
	case "web_search":
		query, err := stringArg(jsonArgs, "query")
		if err != nil {
			return "", false, err
		}
		s.searched = true
		s.planner.emit("search", "Searching the web for “"+truncate(query, 80)+"”")
		found, ok := s.planner.research(ctx, query)
		if !ok {
			return "web_search unavailable or returned no readable results. Continue with the user's context and any provided sources.", false, nil
		}
		s.sources = mergeSources(s.sources, found)
		s.planner.emit("sources", fmt.Sprintf("Found %d source%s so far", len(s.sources), plural(len(s.sources))))
		return sourceDigest("web_search results", found), false, nil
	case "read_url":
		url, err := stringArg(jsonArgs, "url")
		if err != nil {
			return "", false, err
		}
		url = normalizeURL(url)
		if s.readURLs == nil {
			s.readURLs = map[string]bool{}
		}
		s.readURLs[url] = true
		s.planner.emit("read", "Reading "+url)
		found := s.planner.crawlURLs(ctx, []string{url})
		if len(found) == 0 {
			return "read_url unavailable or returned no readable content for " + url + ". Continue if enough context is available.", false, nil
		}
		if s.successfulURLReads == nil {
			s.successfulURLReads = map[string]bool{}
		}
		s.successfulURLReads[url] = true
		s.sources = mergeSources(s.sources, found)
		return sourceDigest("read_url result", found), false, nil
	case "create_plan":
		if err := s.readyToCreate(); err != nil {
			if s.requireSuccessfulURLRead && s.allRequiredURLsAttempted() && !s.anySuccessfulURLRead() {
				return "", false, fmt.Errorf("none of the added links could be read")
			}
			return "create_plan blocked: " + err.Error(), false, nil
		}
		s.planner.emit("writing", "Writing the plan")
		d, err := decodeDraft(jsonArgs)
		if err != nil {
			return "", false, err
		}
		s.final = d
		return "plan accepted", true, nil
	default:
		return "", false, fmt.Errorf("unknown planning tool: %s", name)
	}
}

func (s *planningToolSession) readyToCreate() error {
	if s.researchRequired && !s.searched {
		return fmt.Errorf("call web_search before create_plan")
	}
	var missing []string
	if s.requireSuccessfulURLRead {
		if s.anySuccessfulURLRead() {
			return nil
		}
		for _, url := range s.requiredURLs {
			if !s.readURLs[normalizeURL(url)] {
				missing = append(missing, url)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("call read_url for these URLs first: %s", strings.Join(missing, ", "))
		}
		return fmt.Errorf("none of the added links could be read")
	}
	for _, url := range s.requiredURLs {
		if !s.readURLs[normalizeURL(url)] {
			missing = append(missing, url)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("call read_url for these URLs first: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (s *planningToolSession) allRequiredURLsAttempted() bool {
	if len(s.requiredURLs) == 0 {
		return false
	}
	for _, url := range s.requiredURLs {
		if !s.readURLs[normalizeURL(url)] {
			return false
		}
	}
	return true
}

func (s *planningToolSession) anySuccessfulURLRead() bool {
	for _, url := range s.requiredURLs {
		if s.successfulURLReads[normalizeURL(url)] {
			return true
		}
	}
	return false
}

func validateCreatePlanTerminal(calls []llm.ToolCall) error {
	createPlanIndex := -1
	for i, call := range calls {
		if call.Name != "create_plan" {
			continue
		}
		if createPlanIndex >= 0 {
			return fmt.Errorf("create_plan must be called exactly once as the final planning tool")
		}
		createPlanIndex = i
	}
	if createPlanIndex >= 0 && len(calls) != 1 {
		return fmt.Errorf("create_plan must be the only tool call in its assistant turn")
	}
	return nil
}

func decodeDraft(raw string) (*draft, error) {
	var d draft
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("decode create_plan args: %w", err)
	}
	if strings.TrimSpace(d.Title) == "" || len(d.Discussants) < 2 {
		return nil, fmt.Errorf("create_plan returned an incomplete draft")
	}
	return &d, nil
}

func stringArg(raw, key string) (string, error) {
	var args map[string]string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("decode %s args: %w", key, err)
	}
	value := strings.TrimSpace(args[key])
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func sourceDigest(label string, sources []config.Source) string {
	if len(sources) == 0 {
		return label + ": no readable sources"
	}
	var sb strings.Builder
	sb.WriteString(label)
	sb.WriteString(":\n")
	for i, src := range sources {
		fmt.Fprintf(&sb, "%d. %s\nURL: %s\nSnippet: %s\n\n", i+1, src.Title, src.URL, truncate(src.Snippet, 1000))
	}
	return strings.TrimSpace(sb.String())
}

func dedupeURLs(urls []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(urls))
	for _, url := range urls {
		url = normalizeURL(url)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		out = append(out, url)
	}
	return out
}

func normalizeURL(url string) string {
	return strings.TrimRight(strings.TrimSpace(url), ".,;:!?")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func planningRequirementsPrompt(research bool, urls []string) string {
	urls = dedupeURLs(urls)
	if !research && len(urls) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nAgent-loop requirements:\n")
	if research {
		sb.WriteString("- Call web_search for the topic before create_plan.\n")
	}
	for _, url := range urls {
		fmt.Fprintf(&sb, "- Call read_url for %s before create_plan.\n", url)
	}
	sb.WriteString("- After tool use, call create_plan with the final structured plan.\n")
	return sb.String()
}
