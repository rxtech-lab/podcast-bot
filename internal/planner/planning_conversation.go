package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// maxConversationRounds caps the assistant↔tool ping-pong within a single
// conversational turn so a misbehaving model can never loop forever. A turn ends
// when the model produces no tool call, calls ask_question (pause), or hits this
// cap.
const maxConversationRounds = 12

// ConvEventKind identifies what a ConvEvent carries.
type ConvEventKind string

const (
	ConvText       ConvEventKind = "text"        // streamed assistant text delta
	ConvToolStart  ConvEventKind = "tool_start"  // model committed to a tool (name known)
	ConvToolDelta  ConvEventKind = "tool_delta"  // streamed tool-call argument delta
	ConvAssistant  ConvEventKind = "assistant"   // a full assistant turn (text + calls) is ready to persist
	ConvToolCall   ConvEventKind = "tool_call"   // a single tool call is about to run
	ConvToolResult ConvEventKind = "tool_result" // a tool finished
	ConvPlan       ConvEventKind = "plan"        // write_plan/update_plan produced a plan
	ConvQuestion   ConvEventKind = "question"    // ask_question — the turn pauses after this
)

// ConvEvent is the single event type the conversational loop emits to its sink.
// The HTTP layer translates each event into DB persistence + an SSE frame.
type ConvEvent struct {
	Kind ConvEventKind

	// Text: the streamed delta. Assistant: the full assistant text.
	Text string
	// Assistant: every tool call assembled for the turn.
	Calls []llm.ToolCall
	// ToolStart: the tool name. ToolCall/ToolResult/Plan/Question: the call.
	ToolName   string
	ToolCallID string
	Call       llm.ToolCall

	// ToolResult: the result string fed back to the model + whether it errored.
	Output  string
	IsError bool

	// Plan: the assembled plan result (also used to mirror onto the discussion).
	Plan *Result

	// Question: the raw `questions` JSON array from the ask_question arguments.
	QuestionsJSON string
}

// ConversationOptions carries the non-creative scaffolding the plan tools need
// to assemble a full DebateTopic from the model's draft.
type ConversationOptions struct {
	Language         string
	Channel          string
	Discussants      int
	Template         string
	AgentModel       string
	ExistingSources  []config.Source
	ExistingPlan     *config.DebateTopic
	ExistingMarkdown string
}

const conversationSystemBase = `You are a conversational planning agent for a panel-discussion (podcast) generator.

You run as an agent loop with tools:
- search_sources: search the web for candidate source URLs and snippets.
- crawl_sources: scrape/read specific promising URLs for clean markdown content.
- ask_question: ask the user structured questions when their intent is ambiguous. Prefer asking over guessing.
- write_plan: write the initial plan once you have enough context.
- update_plan: revise the current plan (provide the full updated plan).
- show_plan: show the current saved plan in the app.

Guidelines:
- If the user's request is unclear, underspecified, or could go several meaningful directions, call ask_question BEFORE writing a plan. It is fine to end your turn with only questions and no plan.
- Gather web context when it would materially improve the plan: call search_sources first for regular web context, inspect the candidate URLs/snippets, then call crawl_sources for the best relevant source(s) before writing when source substance is needed.
- Do not scrape every search result. Use crawl_sources only for URLs that look worth grounding the plan.
- When you have enough to proceed, call write_plan. The app will not show that draft until you call show_plan.
- Call show_plan only when the current plan should be visible to the user. Do not call it for internal drafts.
- Afterwards you may keep refining with update_plan in response to the user, then call show_plan again only when the revised plan should replace the visible plan.
- Keep the plan balanced, production-ready, and written in the requested language.
- Do not output the plan as prose or JSON outside the write_plan / update_plan tool calls.
- After show_plan succeeds, do not summarize or restate the plan. Reply with one short plain-text sentence in the requested language, meaning: "The plan is ready above. Ask me any questions or tell me what you'd like to change."
- That reply must be normal user-facing text only: no JSON, no object/dictionary, no key/value pairs, no code block, and no bilingual translation map.`

const conversationResearchToolInstructions = `Additional research-template tools:
- search_research_papers: search academic papers through Firecrawl Research Index.
- read_research_paper: read relevant passages from a selected research paper.
- For the research template, use search_research_papers before general web search when academic evidence is relevant, then call read_research_paper for strong candidate papers.`

func conversationSystem(template string) string {
	system := conversationSystemBase
	if IsResearchTemplate(template) {
		system += "\n\n" + conversationResearchToolInstructions
	}
	if instructions := TemplateInstructions(template); instructions != "" {
		system += "\n\n" + instructions
	}
	return system
}

// convDispatchKind classifies a tool result so the loop knows how to record it.
type convDispatchKind int

const (
	dispatchTool convDispatchKind = iota
	dispatchPlan
	dispatchQuestion
)

type conversationSession struct {
	planner     *Planner
	opts        ConversationOptions
	sources     []config.Source
	currentPlan *Result
}

// RunConversationTurn runs one conversational planning turn: it streams the
// model over the rebuilt `history`, dispatches tools, and emits events to `emit`.
// It returns paused=true when the model called ask_question (the turn ends and
// the conversation waits for the user's answer). History must already be a valid
// OpenAI message sequence (assembled from persisted turns by the caller).
func (p *Planner) RunConversationTurn(ctx context.Context, history []llm.Message, opts ConversationOptions, emit func(ConvEvent)) (bool, error) {
	client := llm.New(p.env.OpenAIBaseURL, p.env.OpenAIKey, p.scriptModel())
	if p.usageRecorder != nil {
		client = client.
			WithUsageRecorder(p.usageRecorder).
			WithPricing(p.env.LLMInputCostPerMillion, p.env.LLMOutputCostPerMillion)
	}
	session := &conversationSession{
		planner: p,
		opts:    opts,
		sources: append([]config.Source(nil), opts.ExistingSources...),
	}
	if opts.ExistingPlan != nil {
		session.currentPlan = &Result{
			Script:     opts.ExistingPlan,
			Markdown:   opts.ExistingMarkdown,
			Sources:    opts.ExistingSources,
			Researched: len(opts.ExistingSources) > 0,
		}
	}
	msgs := append([]llm.Message(nil), history...)

	for round := 0; round < maxConversationRounds; round++ {
		if round == 0 {
			p.emit("thinking", "Thinking…")
		} else {
			p.emit("thinking", "Working…")
		}
		stream, err := client.Stream(ctx, conversationSystem(opts.Template), msgs, conversationTools(opts.Template))
		if err != nil {
			return false, fmt.Errorf("planning conversation: %w", err)
		}

		var assistantText strings.Builder
		var tcDeltas []llm.DeltaToolCall
		activeTool := ""
		activeToolID := ""
		toolIDs := map[int]string{}
		toolNames := map[int]string{}
		streamedArgs, nextTick := 0, planningHeartbeatBytes
		for d := range stream.Deltas() {
			if d.Done {
				break
			}
			if d.TextChunk != "" {
				assistantText.WriteString(d.TextChunk)
				emit(ConvEvent{Kind: ConvText, Text: d.TextChunk})
			}
			if d.ToolCall != nil {
				tcDeltas = append(tcDeltas, *d.ToolCall)
				if d.ToolCall.ID != "" {
					toolIDs[d.ToolCall.Index] = d.ToolCall.ID
				}
				if d.ToolCall.Name != "" {
					toolNames[d.ToolCall.Index] = d.ToolCall.Name
				}
				if d.ToolCall.Name != "" && d.ToolCall.Name != activeTool {
					activeTool = d.ToolCall.Name
					activeToolID = toolIDs[d.ToolCall.Index]
					if activeToolID == "" {
						activeToolID = d.ToolCall.ID
					}
					streamedArgs, nextTick = 0, planningHeartbeatBytes
					emit(ConvEvent{Kind: ConvToolStart, ToolName: activeTool, ToolCallID: activeToolID})
					p.emitToolStart(conversationStatusName(activeTool))
				}
				if d.ToolCall.Arguments != "" {
					toolID := toolIDs[d.ToolCall.Index]
					if toolID == "" {
						toolID = activeToolID
					}
					toolName := toolNames[d.ToolCall.Index]
					if toolName == "" {
						toolName = activeTool
					}
					emit(ConvEvent{
						Kind:       ConvToolDelta,
						ToolName:   toolName,
						ToolCallID: toolID,
						Text:       d.ToolCall.Arguments,
					})
				}
				if (activeTool == "write_plan" || activeTool == "update_plan") && d.ToolCall.Arguments != "" {
					streamedArgs += len(d.ToolCall.Arguments)
					if streamedArgs >= nextTick {
						nextTick += planningHeartbeatBytes
						p.emit("writing", "Writing the plan…")
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			return false, fmt.Errorf("planning conversation: %w", err)
		}

		calls := llm.AssembleToolCalls(tcDeltas)
		text := assistantText.String()

		// No tool call: the model spoke (or said nothing). Persist the assistant
		// text and end the turn — the conversation stays open for the next user
		// message.
		if len(calls) == 0 {
			if strings.TrimSpace(text) != "" {
				emit(ConvEvent{Kind: ConvAssistant, Text: text})
			}
			return false, nil
		}

		emit(ConvEvent{Kind: ConvAssistant, Text: text, Calls: calls})
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: calls})

		pause := false
		for _, tc := range calls {
			emit(ConvEvent{Kind: ConvToolCall, Call: tc, ToolName: tc.Name})
			output, kind, res, questionsJSON, isErr := session.dispatch(ctx, tc)
			switch kind {
			case dispatchPlan:
				emit(ConvEvent{Kind: ConvPlan, Call: tc, Output: output, Plan: res})
				msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: tc.ID})
			case dispatchQuestion:
				// The turn pauses: persist the question and emit it, but do NOT add a
				// tool result yet — it is written when the user answers, closing the
				// assistant(tool_calls)→tool pairing on resume.
				emit(ConvEvent{Kind: ConvQuestion, Call: tc, QuestionsJSON: questionsJSON})
				pause = true
			default:
				emit(ConvEvent{Kind: ConvToolResult, Call: tc, Output: output, IsError: isErr, Plan: res})
				msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: tc.ID})
			}
		}
		if pause {
			return true, nil
		}
	}
	return false, nil
}

// dispatch executes one tool call. The returned kind tells the loop how to
// record the result (plain tool result, a plan, or a pausing question).
func (s *conversationSession) dispatch(ctx context.Context, tc llm.ToolCall) (output string, kind convDispatchKind, res *Result, questionsJSON string, isErr bool) {
	switch tc.Name {
	case "search_research_papers":
		query, err := stringArg(tc.Arguments, "query")
		if err != nil {
			return err.Error(), dispatchTool, nil, "", true
		}
		s.planner.emit("search", "Searching research papers for “"+truncate(query, 80)+"”")
		found, ok := s.planner.researchPapers(ctx, query)
		if !ok {
			return "search_research_papers returned no readable papers. Continue with the user's context, existing sources, or general web search if useful.", dispatchTool, nil, "", false
		}
		s.sources = mergeSources(s.sources, found)
		s.planner.emit("sources", fmt.Sprintf("Found %d research source%s so far", len(s.sources), plural(len(s.sources))))
		return sourceDigest("search_research_papers results", found), dispatchTool, nil, "", false
	case "read_research_paper":
		paperID, err := stringArg(tc.Arguments, "paper_id")
		if err != nil {
			return err.Error(), dispatchTool, nil, "", true
		}
		query := optionalStringArg(tc.Arguments, "query")
		s.planner.emit("read", "Reading research paper "+truncate(paperID, 80))
		found, ok := s.planner.readResearchPaper(ctx, paperID, query)
		if !ok {
			return "read_research_paper could not read relevant passages for " + paperID + ". Continue if enough context is available.", dispatchTool, nil, "", false
		}
		s.sources = mergeSources(s.sources, []config.Source{found})
		return sourceDigest("read_research_paper result", []config.Source{found}), dispatchTool, nil, "", false
	case "search_sources":
		query, err := stringArg(tc.Arguments, "query")
		if err != nil {
			return err.Error(), dispatchTool, nil, "", true
		}
		s.planner.emit("search", "Searching the web for “"+truncate(query, 80)+"”")
		found, ok := s.planner.research(ctx, query)
		if !ok {
			return "search_sources returned no readable results. Continue with the user's context and any existing sources.", dispatchTool, nil, "", false
		}
		s.sources = mergeSources(s.sources, found)
		s.planner.emit("sources", fmt.Sprintf("Found %d source%s so far", len(s.sources), plural(len(s.sources))))
		return sourceDigest("search_sources results", found), dispatchTool, nil, "", false
	case "crawl_sources":
		urls, err := stringsArg(tc.Arguments, "urls")
		if err != nil {
			return err.Error(), dispatchTool, nil, "", true
		}
		urls = dedupeURLs(urls)
		if len(urls) == 0 {
			return "crawl_sources requires at least one url", dispatchTool, nil, "", true
		}
		found := s.planner.crawlURLs(ctx, urls)
		if len(found) == 0 {
			return "crawl_sources could not read any of the URLs. Continue if enough context is available.", dispatchTool, nil, "", false
		}
		s.sources = mergeSources(s.sources, found)
		return sourceDigest("crawl_sources results", found), dispatchTool, nil, "", false
	case "write_plan", "update_plan":
		s.planner.emit("writing", "Writing the plan…")
		d, err := decodeDraft(tc.Arguments)
		if err != nil {
			return "plan rejected: " + err.Error(), dispatchTool, nil, "", true
		}
		if err := s.validateDraft(d); err != nil {
			return "plan rejected: " + err.Error(), dispatchTool, nil, "", true
		}
		result, err := s.planner.assembleWithModel(d, s.planLanguage(), s.opts.Channel, s.sources, s.planModel())
		if err != nil {
			return "plan rejected: " + err.Error(), dispatchTool, nil, "", true
		}
		s.currentPlan = result
		return "Plan saved internally. Call show_plan when this plan should be visible to the user.", dispatchTool, result, "", false
	case "show_plan":
		if s.currentPlan == nil || s.currentPlan.Script == nil {
			return "no plan has been written yet; call write_plan or update_plan first", dispatchTool, nil, "", true
		}
		return `Plan shown to the user above. Do not summarize or restate it. Reply with one short plain-text sentence in the requested language, meaning: "The plan is ready above. Ask me any questions or tell me what you'd like to change." The reply must be normal user-facing text only: no JSON, no object/dictionary, no key/value pairs, no code block, and no bilingual translation map.`, dispatchPlan, s.currentPlan, "", false
	case "ask_question":
		questionsJSON, err := questionsArg(tc.Arguments)
		if err != nil {
			return err.Error(), dispatchTool, nil, "", true
		}
		return "", dispatchQuestion, nil, questionsJSON, false
	default:
		return "unknown tool: " + tc.Name, dispatchTool, nil, "", true
	}
}

func (s *conversationSession) validateDraft(d *draft) error {
	if d == nil {
		return fmt.Errorf("draft is required")
	}
	n := s.opts.Discussants
	if n < 2 {
		return nil
	}
	if len(d.Discussants) != n {
		return fmt.Errorf("use exactly %d discussants; got %d", n, len(d.Discussants))
	}
	return nil
}

func (s *conversationSession) planLanguage() string {
	if strings.TrimSpace(s.opts.Language) != "" {
		return s.opts.Language
	}
	return "en-US"
}

func (s *conversationSession) planModel() string {
	if strings.TrimSpace(s.opts.AgentModel) != "" {
		return strings.TrimSpace(s.opts.AgentModel)
	}
	return s.planner.agentModel()
}

// conversationStatusName maps the conversational tool names onto the coarse
// status phases emitToolStart understands.
func conversationStatusName(name string) string {
	switch name {
	case "search_research_papers", "search_sources":
		return "web_search"
	case "read_research_paper", "crawl_sources":
		return "read_url"
	case "write_plan", "update_plan":
		return "create_plan"
	default:
		return name
	}
}

// stringsArg extracts a []string argument (e.g. crawl_sources urls).
func stringsArg(raw, key string) ([]string, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("decode %s args: %w", key, err)
	}
	v, ok := args[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	var list []string
	if err := json.Unmarshal(v, &list); err != nil {
		// Tolerate a single string value.
		var single string
		if json.Unmarshal(v, &single) == nil && strings.TrimSpace(single) != "" {
			return []string{single}, nil
		}
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	return list, nil
}

// questionsArg validates the ask_question arguments and returns the raw
// `questions` array JSON so it can be persisted and forwarded to the client
// unchanged (matching the iOS QuestionItem shape).
func questionsArg(raw string) (string, error) {
	var args struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("decode ask_question args: %w", err)
	}
	var items []struct {
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(args.Questions, &items); err != nil || len(items) == 0 {
		return "", fmt.Errorf("ask_question requires a non-empty questions array")
	}
	return string(args.Questions), nil
}

// AttachmentsText folds a prompt and any uploaded document attachments into a
// single text blob suitable for persisting as the conversation's user turn.
// Image attachments are not supported in the conversational flow (the history is
// rebuilt as text on every resume).
func AttachmentsText(prompt string, attachments []Attachment) string {
	return strings.TrimSpace(prompt) + attachmentsPrompt(attachments)
}

// ConversationMessageText persists a visible user message plus hidden current
// planning settings for the model. The server's client-facing projection strips
// the hidden settings block back out before rendering the user bubble.
func ConversationMessageText(prompt string, attachments []Attachment, language string) string {
	visible := strings.TrimSpace(prompt)
	lang := strings.TrimSpace(language)
	if lang == "" {
		return AttachmentsText(visible, attachments)
	}
	var sb strings.Builder
	sb.WriteString(visible)
	sb.WriteString("\n\nCurrent plan settings:\n")
	sb.WriteString("- Language for all names and text: " + lang + "\n")
	sb.WriteString(attachmentsPrompt(attachments))
	return strings.TrimSpace(sb.String())
}

// ConversationInitialText is the first persisted user turn for a newly-created
// conversational plan. It carries the user's prompt plus non-creative settings
// that the agent must honor when writing the plan.
func ConversationInitialText(req PlanRequest) string {
	topic := strings.TrimSpace(req.Topic)
	lang := strings.TrimSpace(req.Language)
	if lang == "" {
		lang = "en-US"
	}
	n := req.Discussants
	if n < 2 {
		n = 3
	}
	if n > 6 {
		n = 6
	}
	var sb strings.Builder
	sb.WriteString("Design a panel discussion about the following topic.\n\n")
	sb.WriteString("Topic: " + topic + "\n\n")
	sb.WriteString("Plan settings:\n")
	sb.WriteString("- Language for all names and text: " + lang + "\n")
	sb.WriteString(fmt.Sprintf("- Number of discussants: %d\n", n))
	if instructions := TemplateInstructions(req.Template); instructions != "" {
		sb.WriteString("\nTemplate instructions:\n")
		sb.WriteString(instructions)
		sb.WriteString("\n\n")
	}
	if req.Research {
		sb.WriteString("- Research live sources when it would improve the plan.\n")
	} else {
		sb.WriteString("- Do not use live web research unless the user explicitly asks for it later.\n")
	}
	sb.WriteString(fmt.Sprintf("\nUse exactly %d discussants. Each discussant must have a distinct perspective.\n", n))
	sb.WriteString(referencePrompt(req.Reference))
	return AttachmentsText(sb.String(), req.Attachments)
}
