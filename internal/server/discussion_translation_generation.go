package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

const translationBackgroundTimeout = 15 * time.Minute

type TranslationTaskPayload struct {
	DiscussionID   string `json:"discussion_id"`
	TargetLanguage string `json:"target_language"`
}

type translationSlot struct {
	ID    string
	Text  string
	Apply func(string)
}

type translationItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type translationResponse struct {
	Translations []translationItem `json:"translations"`
}

func supportedPodcastLanguage(code string) bool {
	switch normalizeTranslationLanguage(code) {
	case "en-US", "zh-CN", "zh-TW", "ja-JP", "ko-KR", "es-ES", "fr-FR", "de-DE":
		return true
	default:
		return false
	}
}

func podcastLanguageName(code string) string {
	switch normalizeTranslationLanguage(code) {
	case "en-US":
		return "English (United States)"
	case "zh-CN":
		return "Simplified Chinese"
	case "zh-TW":
		return "Traditional Chinese"
	case "ja-JP":
		return "Japanese"
	case "ko-KR":
		return "Korean"
	case "es-ES":
		return "Spanish"
	case "fr-FR":
		return "French"
	case "de-DE":
		return "German"
	default:
		return code
	}
}

func (s *Server) StartPodcastTranslation(ctx context.Context, d *Discussion, target string) (*DiscussionTranslationMeta, error) {
	if d == nil || s.d.Discussions == nil || s.d.MQ == nil {
		return nil, errors.New("translation generation is not configured")
	}
	target = normalizeTranslationLanguage(target)
	if !supportedPodcastLanguage(target) {
		return nil, fmt.Errorf("unsupported target language %q", target)
	}
	if strings.EqualFold(target, d.Language) {
		return nil, errors.New("target language must differ from the podcast language")
	}
	if existing, err := s.d.Discussions.TranslationFor(ctx, d.ID, target); err != nil {
		return nil, err
	} else if existing != nil && existing.Status == DiscussionTranslationGenerating {
		meta := existing.Meta()
		return &meta, nil
	}
	model := s.resolvedTranslationModel(ctx)
	if model == "" {
		return nil, errors.New("translation model is not configured")
	}
	if err := s.d.Discussions.BeginTranslation(ctx, d.ID, target, model); err != nil {
		return nil, err
	}
	task, err := mq.NewTask(mq.TaskTranslation, d.ID+"|"+target, TranslationTaskPayload{DiscussionID: d.ID, TargetLanguage: target})
	if err == nil {
		err = s.d.MQ.Publish(ctx, mq.QueueDocs, task)
	}
	if err != nil {
		_ = s.d.Discussions.FailTranslation(ctx, d.ID, target, "failed to enqueue translation")
		return nil, err
	}
	meta := DiscussionTranslationMeta{Language: target, Status: DiscussionTranslationGenerating, Pending: true}
	PublishDiscussionResourceUpdated(s.d.Bus, s.d.Env, d.JobID, d.ID, "Translation generating", "translations")
	return &meta, nil
}

func (s *Server) RunPodcastTranslationTask(ctx context.Context, p TranslationTaskPayload, owner string) error {
	ctx, cancel := context.WithTimeout(ctx, translationBackgroundTimeout)
	defer cancel()
	if s.d.Env == nil {
		return errors.New("translation environment is not configured")
	}
	d, err := s.d.Discussions.DiscussionWithTranscript(ctx, p.DiscussionID)
	if err != nil || d == nil {
		if err == nil {
			err = errors.New("discussion not found")
		}
		return err
	}
	model := s.resolvedTranslationModel(ctx)
	meter := &summaryUsageMeter{}
	client := llm.New(s.d.Env.OpenAIBaseURL, s.d.Env.OpenAIKey, model).
		WithUsageRecorder(meter.record).
		WithPricing(s.d.Env.LLMInputCostPerMillion, s.d.Env.LLMOutputCostPerMillion)

	var reserved, reserveLedgerID int64
	if s.d.Points != nil {
		reserved, reserveLedgerID, _, err = s.d.Points.ReserveTranslation(ctx, s.d.Env, owner, d.ID, p.TargetLanguage)
		if err != nil {
			return err
		}
		if reserved < 0 {
			return mq.Permanent(errors.New("insufficient points for translation"))
		}
	}

	bundle, slots, err := s.translationBundle(ctx, d, p.TargetLanguage)
	if err == nil {
		err = translateSlots(ctx, client, p.TargetLanguage, slots)
	}
	if err != nil {
		if s.d.Points != nil {
			_ = s.d.Points.SettleTranslation(ctx, owner, d.ID, p.TargetLanguage, reserveLedgerID, max(reserved, 0), 0, PointsUsageDetail{})
		}
		return err
	}

	usage := meter.snapshot()
	if s.d.Points != nil {
		actual := s.d.Points.SummaryPoints(s.d.Env, usage.CostUSD)
		_ = s.d.Points.SettleTranslation(ctx, owner, d.ID, p.TargetLanguage, reserveLedgerID, reserved, actual, PointsUsageDetail{
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			TotalTokens: usage.TotalTokens, LLMCostUSD: usage.CostUSD,
			LLMCostKnown: usage.CostKnown, CostUSD: usage.CostUSD,
		})
	}
	if err := s.d.Discussions.SaveTranslation(ctx, d.ID, p.TargetLanguage, *bundle, model, SummaryUsage{
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		TotalTokens: usage.TotalTokens, LLMCostUSD: usage.CostUSD,
	}); err != nil {
		return err
	}
	PublishDiscussionResourceUpdated(s.d.Bus, s.d.Env, d.JobID, d.ID, "Translation ready", "translations")
	return nil
}

func (s *Server) FailPodcastTranslationTask(p TranslationTaskPayload, cause error) {
	message := "translation failed"
	if cause != nil {
		message = cause.Error()
	}
	_ = s.d.Discussions.FailTranslation(context.Background(), p.DiscussionID, p.TargetLanguage, message)
	PublishDiscussionResourceUpdated(s.d.Bus, s.d.Env, "", p.DiscussionID, "Translation failed", "translations")
}

func (s *Server) translationBundle(ctx context.Context, d *Discussion, target string) (*DiscussionTranslationBundle, []translationSlot, error) {
	bundle := &DiscussionTranslationBundle{Language: target, Title: d.Title, Topic: d.Topic, Markdown: d.Markdown}
	var slots []translationSlot
	add := func(id string, value *string) {
		appendTranslationSlots(&slots, id, value)
	}
	add("title", &bundle.Title)
	add("topic", &bundle.Topic)
	add("plan.markdown", &bundle.Markdown)

	if d.Script != nil {
		raw, _ := json.Marshal(d.Script)
		var script config.DebateTopic
		if err := json.Unmarshal(raw, &script); err != nil {
			return bundle, nil, err
		}
		script.Language = target
		bundle.Script = &script
		collectScriptTranslationSlots(&script, add)
	}
	bundle.Lines = append([]DiscussionLine(nil), d.Lines...)
	for i := range bundle.Lines {
		add(fmt.Sprintf("line.%d.speaker", i), &bundle.Lines[i].Speaker)
		add(fmt.Sprintf("line.%d.text", i), &bundle.Lines[i].Text)
		add(fmt.Sprintf("line.%d.judgement", i), &bundle.Lines[i].JudgementComment)
	}

	vtt, err := s.translationSourceVTT(ctx, d.JobID)
	if err != nil {
		return bundle, nil, fmt.Errorf("load source captions: %w", err)
	}
	if strings.TrimSpace(vtt) != "" {
		bundle.CaptionsVTT = vtt
		collectVTTTranslationSlots(&bundle.CaptionsVTT, &slots)
	}
	if doc, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypeSummary); err == nil && doc != nil && doc.Status == SummaryReadyState {
		bundle.SummaryMarkdown = doc.Markdown
		add("summary", &bundle.SummaryMarkdown)
	}
	if doc, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, "text"); err == nil && doc != nil && doc.Status == SummaryReadyState {
		bundle.TextMarkdown = doc.Markdown
		add("text-content", &bundle.TextMarkdown)
	}
	if doc, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypeMindmap); err == nil && doc != nil && doc.Status == SummaryReadyState {
		var spec summarizer.MindmapSpec
		if json.Unmarshal([]byte(doc.Markdown), &spec) == nil {
			bundle.Mindmap = &spec
			collectMindmapTranslationSlots(spec.Root, "mindmap", add)
		}
	}
	return bundle, slots, nil
}

func appendTranslationSlots(slots *[]translationSlot, id string, value *string) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return
	}
	parts := splitTranslationText(*value, 6_000)
	translated := append([]string(nil), parts...)
	for i, part := range parts {
		idx := i
		slotID := id
		if len(parts) > 1 {
			slotID = fmt.Sprintf("%s.part.%d", id, i)
		}
		*slots = append(*slots, translationSlot{ID: slotID, Text: part, Apply: func(v string) {
			translated[idx] = v
			*value = strings.Join(translated, "")
		}})
	}
}

func splitTranslationText(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = 6_000
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	parts := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > maxRunes {
		cut := maxRunes
		for i := maxRunes; i >= maxRunes/2; i-- {
			if runes[i-1] == '\n' || runes[i-1] == ' ' {
				cut = i
				break
			}
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

func collectScriptTranslationSlots(script *config.DebateTopic, add func(string, *string)) {
	add("plan.title", &script.Title)
	for id, field := range map[string]*string{
		"background": &script.Background, "affirmative": &script.AffirmativePos,
		"negative": &script.NegativePos, "rules": &script.Rules,
		"surface": &script.Surface, "truth": &script.Truth, "show": &script.Show,
	} {
		add("plan."+id, field)
	}
	addAgent := func(id string, agent *config.AgentSpec) {
		add(id+".name", &agent.Name)
		add(id+".aspect", &agent.Aspect)
	}
	groups := [][]config.AgentSpec{script.Affirmative, script.Negative, script.Players, script.Discussants, script.Viewers}
	for gi := range groups {
		for i := range groups[gi] {
			addAgent(fmt.Sprintf("plan.agent.%d.%d", gi, i), &groups[gi][i])
		}
	}
	for id, agent := range map[string]*config.AgentSpec{
		"judge": &script.Judge, "puzzle_host": &script.PuzzleHost,
		"series_host": &script.SeriesHost, "host": &script.Host,
		"commander": &script.Commander, "audiobook_host": &script.AudioBookHost,
	} {
		addAgent("plan."+id, agent)
	}
	for i := range script.AudioBookSpeakers {
		add(fmt.Sprintf("plan.speaker.%d.name", i), &script.AudioBookSpeakers[i].Name)
		add(fmt.Sprintf("plan.speaker.%d.description", i), &script.AudioBookSpeakers[i].Description)
	}
	for i := range script.AudioBookChapters {
		add(fmt.Sprintf("plan.chapter.%d.title", i), &script.AudioBookChapters[i].Title)
		add(fmt.Sprintf("plan.chapter.%d.summary", i), &script.AudioBookChapters[i].Summary)
		for j := range script.AudioBookChapters[i].Speakers {
			add(fmt.Sprintf("plan.chapter.%d.speaker.%d", i, j), &script.AudioBookChapters[i].Speakers[j])
		}
	}
	for i := range script.UploadedAudioSpeakers {
		add(fmt.Sprintf("plan.uploaded_speaker.%d", i), &script.UploadedAudioSpeakers[i])
	}
	for i := range script.TranscriptSegments {
		add(fmt.Sprintf("plan.segment.%d.speaker", i), &script.TranscriptSegments[i].Speaker)
		add(fmt.Sprintf("plan.segment.%d.text", i), &script.TranscriptSegments[i].Text)
	}
	for i := range script.Sources {
		add(fmt.Sprintf("plan.source.%d.title", i), &script.Sources[i].Title)
		add(fmt.Sprintf("plan.source.%d.snippet", i), &script.Sources[i].Snippet)
		add(fmt.Sprintf("plan.source.%d.markdown", i), &script.Sources[i].Markdown)
	}
}

func collectMindmapTranslationSlots(node *summarizer.MindmapNode, prefix string, add func(string, *string)) {
	if node == nil {
		return
	}
	add(prefix+"."+node.ID+".title", &node.Title)
	add(prefix+"."+node.ID+".note", &node.Note)
	for _, child := range node.Children {
		collectMindmapTranslationSlots(child, prefix, add)
	}
}

func collectVTTTranslationSlots(vtt *string, slots *[]translationSlot) {
	lines := strings.Split(*vtt, "\n")
	for i := range lines {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || trimmed == "WEBVTT" || strings.Contains(trimmed, "-->") {
			continue
		}
		allDigits := true
		for _, r := range trimmed {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			continue
		}
		idx := i
		*slots = append(*slots, translationSlot{ID: fmt.Sprintf("caption.%d", i), Text: lines[i], Apply: func(v string) {
			lines[idx] = v
			*vtt = strings.Join(lines, "\n")
		}})
	}
}

func translateSlots(ctx context.Context, client *llm.Client, target string, slots []translationSlot) error {
	const maxBatchItems = 30
	const maxBatchRunes = 16_000
	for start := 0; start < len(slots); {
		end := start
		runes := 0
		for end < len(slots) && end-start < maxBatchItems {
			n := len([]rune(slots[end].Text))
			if end > start && runes+n > maxBatchRunes {
				break
			}
			runes += n
			end++
		}
		items := make([]translationItem, 0, end-start)
		for _, slot := range slots[start:end] {
			items = append(items, translationItem{ID: slot.ID, Text: slot.Text})
		}
		payload, _ := json.Marshal(map[string]any{"target_language": podcastLanguageName(target), "items": items})
		system := "Translate user-facing podcast text. Return strict JSON with a translations array of {id,text}. Preserve every id and item count exactly. Preserve Markdown structure, URLs, placeholders, and meaning. Translate or naturally transliterate speaker and character display names in fields whose id contains speaker or ends in .name; preserve other proper names. Never add commentary."
		raw, err := client.JSON(ctx, system, string(payload))
		if err != nil {
			return err
		}
		var response translationResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			return fmt.Errorf("translation model returned invalid JSON: %w", err)
		}
		if len(response.Translations) != len(items) {
			return fmt.Errorf("translation returned %d items, want %d", len(response.Translations), len(items))
		}
		byID := make(map[string]string, len(response.Translations))
		for _, item := range response.Translations {
			if _, duplicate := byID[item.ID]; duplicate {
				return fmt.Errorf("translation returned duplicate id %q", item.ID)
			}
			if strings.TrimSpace(item.Text) == "" {
				return fmt.Errorf("translation returned empty text for %q", item.ID)
			}
			byID[item.ID] = item.Text
		}
		for _, slot := range slots[start:end] {
			value, ok := byID[slot.ID]
			if !ok {
				return fmt.Errorf("translation omitted id %q", slot.ID)
			}
			slot.Apply(value)
		}
		start = end
	}
	return nil
}

func (s *Server) translationSourceVTT(ctx context.Context, jobID string) (string, error) {
	if strings.TrimSpace(jobID) == "" {
		return "", nil
	}
	job := &Job{ID: jobID}
	if s.d.Jobs != nil {
		if stored := s.d.Jobs.Get(jobID); stored != nil {
			job = stored
		}
	}
	data, err := s.loadJobCaptionVTT(ctx, job, "")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}
