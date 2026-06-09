package video

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/content_creator"
)

func TestSeriesStageMainPhaseResetsToNarrationFrameZero(t *testing.T) {
	r := renderForTest(t)
	enc := &Encoder{rend: r}
	stage := NewSeriesChannelStage(enc, "")
	stage.activate()

	mainFrame := solidRGBA(color.RGBA{R: 0xff, A: 0xff})
	recapFrame := solidRGBA(color.RGBA{G: 0xff, A: 0xff})
	stage.AttachNarrationFrame(0, mainFrame)
	stage.AttachImageRefs(map[string]*image.RGBA{"s1e1i3": recapFrame})

	stage.applyImageRef("s1e1i3")
	if got := currentSceneBackground(r); got != recapFrame {
		t.Fatalf("scene before phase change = %p, want recap frame %p", got, recapFrame)
	}

	stage.handlePhase(contentcreator.PhaseMsg{Phase: agent.PhaseFreeSpeech})
	if got := currentSceneBackground(r); got != mainFrame {
		t.Fatalf("scene after main phase = %p, want narration frame 0 %p", got, mainFrame)
	}
}

func TestSeriesStageSectionLabelTransitions(t *testing.T) {
	r := renderForTest(t)
	enc := &Encoder{rend: r}
	stage := NewSeriesChannelStage(enc, "")
	stage.activate()

	// Topic primes the cached title used by the main banner.
	stage.handleTopic(contentcreator.TopicMsg{
		Type:    "series",
		Title:   "迷霧裡的旅人",
		Show:    "Dreamers",
		Season:  1,
		Episode: 2,
	})

	// Recap section: banner held at full opacity until the phase changes.
	stage.handlePhase(contentcreator.PhaseMsg{Phase: agent.PhaseOpening})
	text, start, hold := currentSectionLabel(r)
	if text != "上集回顧" {
		t.Fatalf("recap label = %q, want 上集回顧", text)
	}
	if !hold {
		t.Fatalf("recap label hold = false, want true")
	}
	if start.IsZero() {
		t.Fatalf("recap label start time is zero")
	}
	recapStart := start

	// Main section: banner switches to "本集 — {title}" with hold=false
	// and a fresh start time so the 30 s fade clock anchors here.
	time.Sleep(2 * time.Millisecond)
	stage.handlePhase(contentcreator.PhaseMsg{Phase: agent.PhaseFreeSpeech})
	text, start, hold = currentSectionLabel(r)
	if want := "本集 — 迷霧裡的旅人"; text != want {
		t.Fatalf("main label = %q, want %q", text, want)
	}
	if hold {
		t.Fatalf("main label hold = true, want false")
	}
	if !start.After(recapStart) {
		t.Fatalf("main label start %v not after recap start %v", start, recapStart)
	}

	// PhaseEnded clears the banner so it doesn't linger.
	stage.handlePhase(contentcreator.PhaseMsg{Phase: agent.PhaseEnded})
	text, start, _ = currentSectionLabel(r)
	if text != "" || !start.IsZero() {
		t.Fatalf("ended label = %q (start %v), want cleared", text, start)
	}
}

func currentSectionLabel(r *Renderer) (string, time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seriesSectionLabelText, r.seriesSectionLabelStart, r.seriesSectionLabelHold
}

func solidRGBA(c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func currentSceneBackground(r *Renderer) *image.RGBA {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sceneBg
}
