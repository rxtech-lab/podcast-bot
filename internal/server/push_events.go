package server

import (
	"context"
	"strings"
	"time"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

func (s *Server) watchPushEvents() {
	sub, unsub := s.d.Bus.Subscribe(128)
	defer unsub()
	for v := range sub {
		msg, ok := v.(contentcreator.TranscriptMsg)
		if !ok || !msg.Done || msg.IsUserMessage || strings.TrimSpace(string(msg.Role)) == "user" {
			continue
		}
		jobID := contentcreator.MsgChannelID(v)
		if jobID == "" || !s.markPodcastStartPushSent(jobID) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		d, err := s.d.Discussions.GetByJobID(ctx, jobID)
		if err != nil {
			s.logger().Warn("podcast start push discussion lookup failed", "job", jobID, "err", err)
			cancel()
			continue
		}
		if d != nil {
			s.notifyPodcastStarted(ctx, d)
		}
		cancel()
	}
}

func (s *Server) markPodcastStartPushSent(jobID string) bool {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.podcastStartSent[jobID] {
		return false
	}
	s.podcastStartSent[jobID] = true
	return true
}

func (s *Server) notifyPlanReady(ctx context.Context, d *Discussion) {
	if d == nil {
		return
	}
	s.notifyUser(ctx, d.OwnerUserID, PushNotification{
		Kind:         PushKindPlanReady,
		DiscussionID: d.ID,
		Title:        "Plan ready",
		Body:         pushDiscussionTitle(d, "Your discussion plan is ready."),
		URL:          s.discussionDeepLink(d.ID),
	})
}

func (s *Server) notifyPodcastStarted(ctx context.Context, d *Discussion) {
	s.notifyUser(ctx, d.OwnerUserID, PushNotification{
		Kind:         PushKindPodcastStarted,
		DiscussionID: d.ID,
		Title:        "Podcast started",
		Body:         pushDiscussionTitle(d, "Your podcast has started."),
		URL:          s.discussionDeepLink(d.ID),
	})
}

func (s *Server) notifyPodcastReady(ctx context.Context, d *Discussion) {
	s.notifyUser(ctx, d.OwnerUserID, PushNotification{
		Kind:         PushKindPodcastReady,
		DiscussionID: d.ID,
		Title:        "Podcast finished",
		Body:         pushDiscussionTitle(d, "Your podcast is ready to play."),
		URL:          s.discussionDeepLink(d.ID),
	})
}

func (s *Server) notifySummaryReady(ctx context.Context, d *Discussion) {
	s.notifyUser(ctx, d.OwnerUserID, PushNotification{
		Kind:         PushKindSummaryReady,
		DiscussionID: d.ID,
		Title:        "Summary ready",
		Body:         pushDiscussionTitle(d, "Your podcast summary is ready."),
		URL:          s.discussionDeepLink(d.ID),
	})
}

func (s *Server) notifyMarketLike(ctx context.Context, ownerID string, d *Discussion, likerName string) {
	if d == nil || strings.TrimSpace(ownerID) == "" {
		return
	}
	body := pushDiscussionTitle(d, "Someone liked your station.")
	if likerName = strings.TrimSpace(likerName); likerName != "" {
		body = likerName + " liked " + pushDiscussionTitle(d, "your station")
	}
	s.notifyUser(ctx, ownerID, PushNotification{
		Kind:         PushKindMarketLike,
		DiscussionID: d.ID,
		Title:        "New marketplace like",
		Body:         body,
		URL:          s.discussionDeepLink(d.ID),
	})
}

func pushDiscussionTitle(d *Discussion, fallback string) string {
	if d == nil {
		return fallback
	}
	if title := strings.TrimSpace(d.Title); title != "" {
		return title
	}
	if topic := strings.TrimSpace(d.Topic); topic != "" {
		return topic
	}
	return fallback
}
