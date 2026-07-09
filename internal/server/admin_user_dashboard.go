package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/rxtech-lab/admin-generator/admin"
	"github.com/sirily11/debate-bot/internal/config"
)

const userDashboardRecentDays = 30

type userDashboardDailyUsage struct {
	Date             string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	LLMCostUSD       float64
	TTSCostUSD       float64
	ImageCostUSD     float64
	MusicCostUSD     float64
	Podcasts         int64
	Ready            int64
	Generating       int64
	Failed           int64
}

type userDashboardTopup struct {
	Date   string
	Points int64
	Events int64
}

type userDashboardCount struct {
	Name  string
	Count int64
}

type userDashboardModelAssignment struct {
	Speaker string
	Model   string
}

type userDashboardVoiceAssignment struct {
	Speaker string
	Voice   string
}

type userDashboardData struct {
	UserID             string
	DisplayName        string
	Username           string
	AvatarURL          string
	Balance            int64
	SubscriptionPlan   string
	SubscriptionStatus string
	GeneratedPodcasts  int64
	TotalTokens        int64
	TTSCostUSD         float64
	ImageCostUSD       float64
	ImageGenerations   int64
	TopupPoints        int64
	TopupEvents        int64
	Daily              []userDashboardDailyUsage
	Topups             []userDashboardTopup
	Statuses           []userDashboardCount
	PodcastTypes       []userDashboardCount
	Models             []userDashboardCount
	Voices             []userDashboardCount
}

// UserDashboard returns private, admin-only analytics for one user. Detailed
// token/provider usage intentionally stays on this admin path and is never
// added to the user-facing discussion or points-history responses.
func (s *PointsStore) UserDashboard(ctx context.Context, userID string, days int) (userDashboardData, bool, error) {
	var out userDashboardData
	if s == nil {
		return out, false, errors.New("points store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return out, false, nil
	}
	exists, err := s.UserExists(ctx, userID)
	if err != nil || !exists {
		return out, exists, err
	}
	if days <= 0 {
		days = userDashboardRecentDays
	}
	if days > 90 {
		days = 90
	}

	out.UserID = userID
	out.DisplayName = userID
	out.SubscriptionPlan = "Free"
	out.SubscriptionStatus = "none"
	if err := s.loadUserDashboardAccount(ctx, &out); err != nil {
		return userDashboardData{}, false, err
	}

	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	daily := make(map[string]*userDashboardDailyUsage, days)
	orderedDates := make([]string, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		orderedDates = append(orderedDates, date)
		daily[date] = &userDashboardDailyUsage{Date: date}
	}

	topupsByDate, err := s.loadUserDashboardLedger(ctx, userID, start, daily, &out)
	if err != nil {
		return userDashboardData{}, false, err
	}
	modelAssignments, err := s.userDashboardModelAssignments(ctx, userID)
	if err != nil {
		return userDashboardData{}, false, err
	}
	voiceAssignments, err := s.userDashboardVoiceAssignments(ctx, userID)
	if err != nil {
		return userDashboardData{}, false, err
	}
	statusCounts, typeCounts, modelCounts, voiceCounts, err := s.loadUserDashboardDiscussions(ctx, userID, start, daily, modelAssignments, voiceAssignments, &out)
	if err != nil {
		return userDashboardData{}, false, err
	}

	out.Daily = make([]userDashboardDailyUsage, 0, len(orderedDates))
	for _, date := range orderedDates {
		out.Daily = append(out.Daily, *daily[date])
	}
	topupDates := make([]string, 0, len(topupsByDate))
	for date := range topupsByDate {
		topupDates = append(topupDates, date)
	}
	sort.Strings(topupDates)
	out.Topups = make([]userDashboardTopup, 0, len(topupDates))
	for _, date := range topupDates {
		out.Topups = append(out.Topups, *topupsByDate[date])
	}
	out.Statuses = orderedDashboardCounts(statusCounts, []string{
		string(DiscussionReady), string(DiscussionGenerating), string(DiscussionFailed), string(DiscussionPlanning),
	}, 0)
	out.PodcastTypes = sortedDashboardCounts(typeCounts, 8)
	out.Models = sortedDashboardCounts(modelCounts, 8)
	out.Voices = sortedDashboardCounts(voiceCounts, 8)
	return out, true, nil
}

func (s *PointsStore) loadUserDashboardAccount(ctx context.Context, out *userDashboardData) error {
	balance, err := s.Balance(ctx, out.UserID)
	if err != nil {
		return err
	}
	out.Balance = balance
	var displayName, username, avatarURL string
	err = s.db.QueryRowContext(ctx, `SELECT display_name, username, avatar_url
		FROM creator_profiles WHERE user_id = ?`, out.UserID).Scan(&displayName, &username, &avatarURL)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if strings.TrimSpace(displayName) != "" {
		out.DisplayName = strings.TrimSpace(displayName)
	}
	out.Username = strings.TrimSpace(username)
	out.AvatarURL = strings.TrimSpace(avatarURL)

	sub, err := s.Subscription(ctx, out.UserID)
	if err != nil {
		return err
	}
	if sub != nil {
		out.SubscriptionPlan = strings.TrimSpace(sub.DisplayName)
		if out.SubscriptionPlan == "" {
			out.SubscriptionPlan = strings.TrimSpace(sub.ProductID)
		}
		out.SubscriptionStatus = strings.TrimSpace(sub.Status)
	}
	return nil
}

func (s *PointsStore) loadUserDashboardLedger(ctx context.Context, userID string, recentStart time.Time, daily map[string]*userDashboardDailyUsage, out *userDashboardData) (map[string]*userDashboardTopup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT delta, reason, cost_usd, prompt_tokens, completion_tokens, total_tokens,
			llm_cost_usd, tts_cost_usd, music_cost_usd, created_at
		FROM points_ledger WHERE user_id = ? ORDER BY created_at ASC, id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	topups := map[string]*userDashboardTopup{}
	for rows.Next() {
		var delta, promptTokens, completionTokens, totalTokens, createdAt int64
		var reason string
		var costUSD, llmCostUSD, ttsCostUSD, musicCostUSD float64
		if err := rows.Scan(&delta, &reason, &costUSD, &promptTokens, &completionTokens, &totalTokens,
			&llmCostUSD, &ttsCostUSD, &musicCostUSD, &createdAt); err != nil {
			return nil, err
		}
		out.TotalTokens += totalTokens
		out.TTSCostUSD += ttsCostUSD
		if reason == pointsReasonImageGeneration {
			out.ImageCostUSD += costUSD
			out.ImageGenerations++
		}
		date := time.UnixMilli(createdAt).UTC().Format("2006-01-02")
		if time.UnixMilli(createdAt).UTC().Before(recentStart) {
			// Top-up history below is all-time; only usage charts are bounded.
		} else if day := daily[date]; day != nil {
			day.PromptTokens += promptTokens
			day.CompletionTokens += completionTokens
			day.TotalTokens += totalTokens
			day.LLMCostUSD += llmCostUSD
			day.TTSCostUSD += ttsCostUSD
			day.MusicCostUSD += musicCostUSD
			if reason == pointsReasonImageGeneration {
				day.ImageCostUSD += costUSD
			}
		}
		if delta > 0 && (reason == pointsReasonAdminTopup || strings.HasPrefix(reason, pointsReasonPurchase+":")) {
			out.TopupPoints += delta
			out.TopupEvents++
			item := topups[date]
			if item == nil {
				item = &userDashboardTopup{Date: date}
				topups[date] = item
			}
			item.Points += delta
			item.Events++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return topups, nil
}

func (s *PointsStore) userDashboardModelAssignments(ctx context.Context, userID string) (map[string]map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sm.discussion_id, sm.speaker_name, sm.model
		FROM native_discussion_speaker_models sm
		JOIN native_discussions d ON d.id = sm.discussion_id
		WHERE d.owner_user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var discussionID, speaker, model string
		if err := rows.Scan(&discussionID, &speaker, &model); err != nil {
			return nil, err
		}
		speaker = strings.TrimSpace(speaker)
		model = strings.TrimSpace(model)
		if speaker != "" && model != "" {
			if out[discussionID] == nil {
				out[discussionID] = map[string]string{}
			}
			out[discussionID][speaker] = model
		}
	}
	return out, rows.Err()
}

func (s *PointsStore) userDashboardVoiceAssignments(ctx context.Context, userID string) (map[string]map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sv.discussion_id, sv.speaker_name, sv.voice
		FROM native_discussion_speaker_voices sv
		JOIN native_discussions d ON d.id = sv.discussion_id
		WHERE d.owner_user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var discussionID, speaker, voice string
		if err := rows.Scan(&discussionID, &speaker, &voice); err != nil {
			return nil, err
		}
		speaker = strings.TrimSpace(speaker)
		voice = strings.TrimSpace(voice)
		if speaker != "" && voice != "" {
			if out[discussionID] == nil {
				out[discussionID] = map[string]string{}
			}
			out[discussionID][speaker] = voice
		}
	}
	return out, rows.Err()
}

func (s *PointsStore) loadUserDashboardDiscussions(ctx context.Context, userID string, recentStart time.Time, daily map[string]*userDashboardDailyUsage, modelAssignments, voiceAssignments map[string]map[string]string, out *userDashboardData) (map[string]int64, map[string]int64, map[string]int64, map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, script_json, created_at
		FROM native_discussions WHERE owner_user_id = ? ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer rows.Close()
	statusCounts := map[string]int64{}
	typeCounts := map[string]int64{}
	modelCounts := map[string]int64{}
	voiceCounts := map[string]int64{}
	for rows.Next() {
		var discussionID, status, scriptJSON string
		var createdAt int64
		if err := rows.Scan(&discussionID, &status, &scriptJSON, &createdAt); err != nil {
			return nil, nil, nil, nil, err
		}
		status = strings.TrimSpace(status)
		if status == "" {
			status = "unknown"
		}
		statusCounts[status]++
		if status == string(DiscussionPlanning) {
			continue
		}
		out.GeneratedPodcasts++

		var topic config.DebateTopic
		_ = json.Unmarshal([]byte(scriptJSON), &topic)
		contentType := strings.TrimSpace(topic.Type)
		if contentType == "" {
			contentType = "unknown"
		}
		typeCounts[contentType]++
		assignments := dashboardTopicModelAssignments(topic)
		overrides := modelAssignments[discussionID]
		if len(assignments) == 0 {
			for speaker, model := range overrides {
				assignments = append(assignments, userDashboardModelAssignment{Speaker: speaker, Model: model})
			}
		}
		for _, assignment := range assignments {
			model := assignment.Model
			if override := overrides[strings.TrimSpace(assignment.Speaker)]; override != "" {
				model = override
			}
			if model = strings.TrimSpace(model); model != "" {
				modelCounts[model]++
			}
		}
		voices := dashboardTopicVoiceAssignments(topic)
		voiceOverrides := voiceAssignments[discussionID]
		if len(voices) == 0 {
			for speaker, voice := range voiceOverrides {
				voices = append(voices, userDashboardVoiceAssignment{Speaker: speaker, Voice: voice})
			}
		}
		for _, assignment := range voices {
			voice := assignment.Voice
			if override := voiceOverrides[strings.TrimSpace(assignment.Speaker)]; override != "" {
				voice = override
			}
			if voice = strings.TrimSpace(voice); voice != "" {
				voiceCounts[voice]++
			}
		}

		created := time.UnixMilli(createdAt).UTC()
		if created.Before(recentStart) {
			continue
		}
		day := daily[created.Format("2006-01-02")]
		if day == nil {
			continue
		}
		day.Podcasts++
		switch status {
		case string(DiscussionReady):
			day.Ready++
		case string(DiscussionGenerating):
			day.Generating++
		case string(DiscussionFailed):
			day.Failed++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	return statusCounts, typeCounts, modelCounts, voiceCounts, nil
}

func dashboardTopicModelAssignments(topic config.DebateTopic) []userDashboardModelAssignment {
	models := make([]userDashboardModelAssignment, 0, 12)
	add := func(spec config.AgentSpec) {
		if model := strings.TrimSpace(spec.Model); model != "" {
			models = append(models, userDashboardModelAssignment{Speaker: strings.TrimSpace(spec.Name), Model: model})
		}
	}
	add(topic.Host)
	add(topic.Commander)
	add(topic.Judge)
	add(topic.PuzzleHost)
	add(topic.SeriesHost)
	add(topic.AudioBookHost)
	for _, specs := range [][]config.AgentSpec{topic.Discussants, topic.Affirmative, topic.Negative, topic.Players, topic.Viewers} {
		for _, spec := range specs {
			add(spec)
		}
	}
	for _, speaker := range topic.AudioBookSpeakers {
		if model := strings.TrimSpace(speaker.Model); model != "" {
			models = append(models, userDashboardModelAssignment{Speaker: strings.TrimSpace(speaker.Name), Model: model})
		}
	}
	return models
}

func dashboardTopicVoiceAssignments(topic config.DebateTopic) []userDashboardVoiceAssignment {
	voices := make([]userDashboardVoiceAssignment, 0, 12)
	add := func(spec config.AgentSpec) {
		if speaker := strings.TrimSpace(spec.Name); speaker != "" {
			voices = append(voices, userDashboardVoiceAssignment{Speaker: speaker, Voice: strings.TrimSpace(spec.Voice)})
		}
	}
	add(topic.Host)
	add(topic.Judge)
	add(topic.PuzzleHost)
	add(topic.SeriesHost)
	add(topic.AudioBookHost)
	for _, specs := range [][]config.AgentSpec{topic.Discussants, topic.Affirmative, topic.Negative, topic.Players, topic.Viewers} {
		for _, spec := range specs {
			add(spec)
		}
	}
	for _, speaker := range topic.AudioBookSpeakers {
		if name := strings.TrimSpace(speaker.Name); name != "" {
			voices = append(voices, userDashboardVoiceAssignment{Speaker: name, Voice: strings.TrimSpace(speaker.Voice)})
		}
	}
	return voices
}

func sortedDashboardCounts(counts map[string]int64, limit int) []userDashboardCount {
	out := make([]userDashboardCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, userDashboardCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Name < out[j].Name
		}
		return out[i].Count > out[j].Count
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func orderedDashboardCounts(counts map[string]int64, preferred []string, limit int) []userDashboardCount {
	out := make([]userDashboardCount, 0, len(counts))
	seen := map[string]bool{}
	for _, name := range preferred {
		if count, ok := counts[name]; ok {
			out = append(out, userDashboardCount{Name: name, Count: count})
			seen[name] = true
		}
	}
	rest := map[string]int64{}
	for name, count := range counts {
		if !seen[name] {
			rest[name] = count
		}
	}
	out = append(out, sortedDashboardCounts(rest, 0)...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func userDashboardCustomPage(req admin.Request, data userDashboardData) admin.CustomResourcePage {
	dailyRows := make([]map[string]any, 0, len(data.Daily))
	for _, day := range data.Daily {
		dailyRows = append(dailyRows, map[string]any{
			"date":              day.Date,
			"llm_cost_usd":      roundCents(day.LLMCostUSD),
			"audio_cost_usd":    roundCents(day.TTSCostUSD),
			"image_cost_usd":    roundCents(day.ImageCostUSD),
			"music_cost_usd":    roundCents(day.MusicCostUSD),
			"prompt_tokens":     day.PromptTokens,
			"completion_tokens": day.CompletionTokens,
			"total_tokens":      day.TotalTokens,
			"podcasts":          day.Podcasts,
			"ready":             day.Ready,
			"generating":        day.Generating,
			"failed":            day.Failed,
		})
	}
	topupRows := make([]map[string]any, 0, len(data.Topups))
	for _, topup := range data.Topups {
		topupRows = append(topupRows, map[string]any{
			"date": topup.Date, "points": topup.Points, "events": topup.Events,
		})
	}
	countRows := func(items []userDashboardCount, label string) []map[string]any {
		rows := make([]map[string]any, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]any{label: item.Name, "count": item.Count})
		}
		return rows
	}

	displayName := strings.TrimSpace(data.DisplayName)
	if displayName == "" {
		displayName = data.UserID
	}
	accountLines := []string{
		"User ID: " + data.UserID,
		"Subscription: " + data.SubscriptionPlan + " (" + data.SubscriptionStatus + ")",
	}
	if data.Username != "" {
		accountLines = append(accountLines, "Username: @"+strings.TrimPrefix(data.Username, "@"))
	}
	if data.AvatarURL != "" {
		accountLines = append(accountLines, "Avatar: "+data.AvatarURL)
	}

	userPath := req.BasePath + "/users/" + url.PathEscape(data.UserID)
	return admin.CustomResourcePage{
		UIType: "custom",
		Type:   admin.ActionView,
		ActionButtons: []admin.ActionButton{
			{Type: admin.ButtonSecondary, Label: "Back to users", Icon: "arrow-left", Behavior: admin.BehaviorNavigate, ActionType: admin.ActionView, OnClick: req.BasePath + "/users"},
			{Type: admin.ButtonPrimary, Label: "Manage user", Icon: "settings", Behavior: admin.BehaviorNavigate, ActionType: admin.ActionEdit, OnClick: userPath + "?action=edit"},
			{Type: admin.ButtonSecondary, Label: "Refresh", Icon: "refresh-cw", Behavior: admin.BehaviorNavigate, ActionType: admin.ActionView, OnClick: userPath},
		},
		Sections: []admin.CustomPageSection{
			{
				Type:        admin.CustomPageSectionStatistics,
				Title:       displayName,
				Description: "Private user analytics. Recent charts use the last 30 UTC days; rankings and top-ups are all-time.",
				Statistics: []admin.Statistic{
					{Label: "Current balance", Value: formatDelimitedInt(data.Balance), Description: "points"},
					{Label: "Generated podcasts", Value: formatDelimitedInt(data.GeneratedPodcasts), Description: "generating, ready, or failed"},
					{Label: "LLM tokens", Value: formatCompactInt(data.TotalTokens), Description: formatDelimitedInt(data.TotalTokens) + " all-time"},
					{Label: "Audio generation", Value: formatUSD(data.TTSCostUSD), Description: "all-time TTS provider spend"},
					{Label: "Image generations", Value: formatDelimitedInt(data.ImageGenerations), Description: formatUSD(data.ImageCostUSD) + " provider spend"},
					{Label: "Top-ups", Value: formatDelimitedInt(data.TopupPoints), Description: fmt.Sprintf("%s events", formatDelimitedInt(data.TopupEvents))},
				},
			},
			{Type: admin.CustomPageSectionText, Title: "Account", Body: strings.Join(accountLines, "\n")},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Recent usage",
				Description: "Metered provider usage and podcast generation activity by UTC day.",
				Children: []admin.Chart{
					{Type: admin.ChartTypeBar, Title: "Provider usage", Data: dailyRows, XKey: "date", Series: []admin.ChartSeries{
						{Key: "llm_cost_usd", Label: "LLM", Color: "#2563eb"},
						{Key: "audio_cost_usd", Label: "Audio / TTS", Color: "#16a34a"},
						{Key: "image_cost_usd", Label: "Image", Color: "#dc2626"},
						{Key: "music_cost_usd", Label: "Music", Color: "#9333ea"},
					}},
					{Type: admin.ChartTypeBar, Title: "Audio generation status", Data: dailyRows, XKey: "date", Series: []admin.ChartSeries{
						{Key: "ready", Label: "Ready", Color: "#16a34a"},
						{Key: "generating", Label: "Generating", Color: "#2563eb"},
						{Key: "failed", Label: "Failed", Color: "#dc2626"},
					}},
				},
			},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Tokens over time",
				Description: "Prompt, completion, and total tokens from metered ledger events.",
				Children: []admin.Chart{{Type: admin.ChartTypeLine, Title: "Daily tokens", Data: dailyRows, XKey: "date", Series: []admin.ChartSeries{
					{Key: "prompt_tokens", Label: "Prompt", Color: "#2563eb"},
					{Key: "completion_tokens", Label: "Completion", Color: "#16a34a"},
					{Key: "total_tokens", Label: "Total", Color: "#dc2626"},
				}}},
			},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Generated podcasts",
				Description: "All-time generated output grouped by content type and current status.",
				Children: []admin.Chart{
					{Type: admin.ChartTypeBar, Title: "Podcasts by type", Data: countRows(data.PodcastTypes, "type"), XKey: "type", YKey: "count"},
					{Type: admin.ChartTypeBar, Title: "Current status", Data: countRows(data.Statuses, "status"), XKey: "status", YKey: "count"},
				},
			},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Most used models and Azure voices",
				Description: "Counts generated-role model assignments and persisted Azure voice selections.",
				Children: []admin.Chart{
					{Type: admin.ChartTypeBar, Title: "Models", Data: countRows(data.Models, "model"), XKey: "model", YKey: "count"},
					{Type: admin.ChartTypeBar, Title: "Azure voices", Data: countRows(data.Voices, "voice"), XKey: "voice", YKey: "count"},
				},
			},
			{
				Type:        admin.CustomPageSectionCharts,
				Title:       "Top-up history over time",
				Description: "All-time RevenueCat purchases and admin top-ups, grouped by UTC day.",
				Children: []admin.Chart{{Type: admin.ChartTypeLine, Title: "Points added", Data: topupRows, XKey: "date", Series: []admin.ChartSeries{
					{Key: "points", Label: "Points", Color: "#16a34a"},
					{Key: "events", Label: "Top-up events", Color: "#2563eb"},
				}}},
			},
		},
	}
}
