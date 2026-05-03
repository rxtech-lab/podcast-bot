package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/debate"
)

// Model is the bubbletea Model for the live debate TUI.
type Model struct {
	vp    viewport.Model
	ti    textinput.Model
	lines []renderedLine

	width  int
	height int

	phase     agent.Phase
	elapsed   time.Duration
	remaining time.Duration
	status    string

	current renderedLine // in-progress line — appended to lines on Done
	ended   bool

	UserOut chan<- string
}

type renderedLine struct {
	speaker string
	role    agent.Role
	side    string
	text    string
}

var (
	hostStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("87"))
	affStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	negStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	judgeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	viewStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("177"))
	userStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("60")).Bold(true)
	statusBar  = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("231")).Padding(0, 1)
)

// NewModel constructs the TUI model. userOut is the channel used to push
// user input back to the orchestrator.
func NewModel(userOut chan<- string) *Model {
	ti := textinput.New()
	ti.Placeholder = "type a question, /end to wrap up, ↑/↓ or PgUp/PgDn to scroll..."
	ti.Focus()
	ti.CharLimit = 500
	vp := viewport.New(80, 20)
	return &Model{vp: vp, ti: ti, UserOut: userOut}
}

// Init satisfies tea.Model.
func (m *Model) Init() tea.Cmd { return textinput.Blink }

// Update routes events.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.layout()
		m.refreshViewport()
	case tea.KeyMsg:
		switch v.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			text := strings.TrimSpace(m.ti.Value())
			if text != "" {
				select {
				case m.UserOut <- text:
				default:
				}
				m.ti.Reset()
			}
		default:
			var cmd tea.Cmd
			m.ti, cmd = m.ti.Update(v)
			cmds = append(cmds, cmd)
		}
	case debate.TranscriptMsg:
		m.applyTranscript(v)
	case debate.TickMsg:
		m.elapsed = v.Elapsed
		m.remaining = v.Remaining
	case debate.PhaseMsg:
		m.phase = v.Phase
	case debate.StatusMsg:
		m.status = v.Text
	case debate.ErrorMsg:
		if v.Err != nil {
			m.status = "error: " + v.Err.Error()
		}
	case debate.EndedMsg:
		m.ended = true
		m.status = fmt.Sprintf("ended — transcript=%s audio=%s (press ctrl+c to quit)", v.TranscriptPath, v.AudioPath)
	}

	var vpCmd tea.Cmd
	m.vp, vpCmd = m.vp.Update(msg)
	cmds = append(cmds, vpCmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) applyTranscript(t debate.TranscriptMsg) {
	if t.Done {
		// A different speaker arriving complete (e.g. user typing "hi" while
		// Bob is mid-stream) must NOT be merged into Bob's line. Flush Bob's
		// in-progress line first, then add the new speaker as its own line.
		if t.Text != "" && t.Speaker != "" && m.current.speaker != "" && m.current.speaker != t.Speaker {
			m.lines = append(m.lines, m.current)
			m.lines = append(m.lines, renderedLine{
				speaker: t.Speaker, role: t.Role, side: t.Side, text: t.Text,
			})
			m.current = renderedLine{}
			m.refreshViewport()
			return
		}
		// Same-speaker (or no current) Done: promote current + any final text.
		if m.current.text != "" || t.Text != "" {
			line := m.current
			if t.Text != "" {
				if line.text != "" {
					line.text += " "
				}
				line.text += t.Text
			}
			if line.speaker == "" {
				line.speaker = t.Speaker
				line.role = t.Role
				line.side = t.Side
			}
			m.lines = append(m.lines, line)
		}
		m.current = renderedLine{}
		m.refreshViewport()
		return
	}
	if m.current.speaker != t.Speaker {
		// New speaker mid-stream: flush current first.
		if m.current.text != "" {
			m.lines = append(m.lines, m.current)
		}
		m.current = renderedLine{speaker: t.Speaker, role: t.Role, side: t.Side, text: t.Text}
	} else {
		if m.current.text != "" {
			m.current.text += " "
		}
		m.current.text += t.Text
	}
	m.refreshViewport()
}

func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	statusH := 1
	inputH := 1
	m.vp.Width = m.width
	m.vp.Height = m.height - statusH - inputH - 1
	m.ti.Width = m.width - 2
}

func (m *Model) refreshViewport() {
	// Only auto-scroll to the bottom if the user was already there. If they've
	// scrolled up to read history, leave their scroll position alone.
	wasAtBottom := m.vp.AtBottom()

	all := make([]renderedLine, 0, len(m.lines)+1)
	all = append(all, m.lines...)
	if m.current.text != "" || m.current.speaker != "" {
		all = append(all, m.current)
	}
	var b strings.Builder
	for _, l := range all {
		b.WriteString(formatLine(l, m.vp.Width))
		b.WriteByte('\n')
	}
	m.vp.SetContent(strings.TrimRight(b.String(), "\n"))
	if wasAtBottom {
		m.vp.GotoBottom()
	}
}

func formatLine(l renderedLine, width int) string {
	var prefix string
	var style lipgloss.Style
	switch l.role {
	case agent.RoleHost:
		prefix = "host"
		style = hostStyle
	case agent.RoleAffirmative:
		prefix = "affirmative side - " + l.speaker
		style = affStyle
	case agent.RoleNegative:
		prefix = "negative side - " + l.speaker
		style = negStyle
	case agent.RoleJudge:
		prefix = "judge"
		style = judgeStyle
	case agent.RoleViewer:
		prefix = "viewer - " + l.speaker
		style = viewStyle
	default:
		prefix = l.speaker
		style = userStyle
	}
	// User lines get the full line styled (bg + fg) so they're visually
	// distinct from agent turns where only the speaker tag is coloured.
	if l.role != agent.RoleHost && l.role != agent.RoleAffirmative &&
		l.role != agent.RoleNegative && l.role != agent.RoleJudge && l.role != agent.RoleViewer {
		s := style
		if width > 0 {
			s = s.Width(width)
		}
		return s.Render(prefix + ": " + l.text)
	}

	tag := style.Render(prefix + ":")
	line := tag + " " + l.text
	if width <= 0 {
		return line
	}
	// Wrap to viewport width. lipgloss.Width is ANSI-safe (doesn't count colour
	// escape codes) and uses go-runewidth so CJK double-width glyphs are
	// measured correctly.
	return lipgloss.NewStyle().Width(width).Render(line)
}

// View renders the model.
func (m *Model) View() string {
	if m.width == 0 {
		return "initializing..."
	}
	status := fmt.Sprintf("phase: %s   elapsed: %s   remaining: %s",
		m.phase.String(), fmtDur(m.elapsed), fmtDur(m.remaining))
	if m.status != "" {
		status += "   " + m.status
	}
	bar := statusBar.Width(m.width).Render(status)
	return lipgloss.JoinVertical(lipgloss.Left,
		m.vp.View(),
		bar,
		m.ti.View(),
	)
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
