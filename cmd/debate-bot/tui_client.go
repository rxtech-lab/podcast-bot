package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/tui"
)

// runTUIClient is the loopback bridge that satisfies the user's "migrate the
// CLI to consume HTTP streams" requirement. It:
//   * subscribes to /api/events (SSE) and forwards typed messages to the TUI.
//   * subscribes to /api/audio/stream and pipes bytes to a long-running
//     ffplay subprocess.
//   * forwards TUI text-input events to /api/messages via POST.
//
// Returns when the TUI exits or ctx is cancelled.
func runTUIClient(ctx context.Context, log *slog.Logger, baseURL string) error {
	userIn := make(chan string, 16)
	model := tui.NewModel(userIn)
	prog, wait := tui.Run(model)

	clientCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go forwardUserMessages(clientCtx, log, baseURL, userIn)
	go streamEvents(clientCtx, log, baseURL, prog)
	go playAudio(clientCtx, log, baseURL)

	err := wait()
	cancel()
	return err
}

// forwardUserMessages drains the TUI's input channel and POSTs each line to
// /api/messages. Best-effort: failures are logged.
func forwardUserMessages(ctx context.Context, log *slog.Logger, baseURL string, in <-chan string) {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case s, ok := <-in:
			if !ok {
				return
			}
			body, _ := json.Marshal(map[string]string{"text": s})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/messages", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				log.Warn("tui→server message failed", "err", err)
				continue
			}
			resp.Body.Close()
		}
	}
}

// streamEvents connects to /api/events, parses SSE frames, and re-publishes
// each as the corresponding tea.Msg into the TUI program.
func streamEvents(ctx context.Context, log *slog.Logger, baseURL string, prog *tea.Program) {
	for ctx.Err() == nil {
		if err := streamEventsOnce(ctx, baseURL, prog); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("event stream disconnected, retrying", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func streamEventsOnce(ctx context.Context, baseURL string, prog *tea.Program) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	r := bufio.NewReader(resp.Body)
	var event string
	var data bytes.Buffer
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Dispatch accumulated event.
			if event != "" && data.Len() > 0 {
				if msg := decodeEvent(event, data.Bytes()); msg != nil {
					prog.Send(msg)
				}
			}
			event = ""
			data.Reset()
			continue
		}
		switch {
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(strings.TrimSpace(line[len("data:"):]), " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		}
	}
}

// decodeEvent converts an SSE (event-name, json-data) pair back into the
// concrete debate.* message types the TUI's Update method already handles.
func decodeEvent(event string, data []byte) tea.Msg {
	switch event {
	case "transcript":
		var p struct {
			Speaker string `json:"speaker"`
			Role    string `json:"role"`
			Side    string `json:"side"`
			Text    string `json:"text"`
			Done    bool   `json:"done"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.TranscriptMsg{
			Speaker: p.Speaker, Role: agent.Role(p.Role), Side: p.Side,
			Text: p.Text, Done: p.Done,
		}
	case "tick":
		var p struct {
			ElapsedMs   int64 `json:"elapsed_ms"`
			RemainingMs int64 `json:"remaining_ms"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.TickMsg{
			Elapsed:   time.Duration(p.ElapsedMs) * time.Millisecond,
			Remaining: time.Duration(p.RemainingMs) * time.Millisecond,
		}
	case "phase":
		var p struct {
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.PhaseMsg{Phase: parsePhase(p.Phase)}
	case "status":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.StatusMsg{Text: p.Text}
	case "error":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.ErrorMsg{Err: fmt.Errorf("%s", p.Text)}
	case "ended":
		var p struct {
			TranscriptPath string `json:"transcript_path"`
			AudioPath      string `json:"audio_path"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.EndedMsg{TranscriptPath: p.TranscriptPath, AudioPath: p.AudioPath}
	case "topic":
		var p struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Index int    `json:"index"`
			Total int    `json:"total"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil
		}
		return debate.TopicMsg{ID: p.ID, Title: p.Title, Index: p.Index, Total: p.Total}
	}
	return nil
}

func parsePhase(s string) agent.Phase {
	switch s {
	case "setup":
		return agent.PhaseSetup
	case "opening":
		return agent.PhaseOpening
	case "free-speech":
		return agent.PhaseFreeSpeech
	case "closing":
		return agent.PhaseClosing
	case "verdict":
		return agent.PhaseVerdict
	case "conclusion":
		return agent.PhaseConclusion
	case "ended":
		return agent.PhaseEnded
	}
	return agent.PhaseSetup
}

// playAudio fetches /api/audio/stream and pipes the chunked MP3 bytes into a
// long-running ffplay process. ffplay decodes paced audio (the server pre-paces
// via ffmpeg -re) so this side does no rate limiting of its own.
func playAudio(ctx context.Context, log *slog.Logger, baseURL string) {
	for ctx.Err() == nil {
		if err := playAudioOnce(ctx, baseURL); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("audio stream disconnected, retrying", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func playAudioOnce(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/audio/stream", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	cmd := exec.CommandContext(ctx, "ffplay",
		"-nodisp", "-autoexit", "-loglevel", "quiet", "-i", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	_, copyErr := io.Copy(stdin, resp.Body)
	return copyErr
}
