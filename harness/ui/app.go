// Package ui contains the harness-native terminal interface.
package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	uilist "github.com/unreallabsai/unreal-agent/harness/ui/list"
)

// Event is one output event from a harness run. Item carries a persisted
// session item; Line accepts its JSON encoding. Done marks the end of the run.
type Event struct {
	Item *sessionstore.Item
	Line string
	Done bool
	Err  error
}

// Submit starts one harness turn and returns its event stream. The session ID
// is stable for the lifetime of a TUI, so each submitted prompt resumes the
// same durable session.
type Submit func(context.Context, string, string) <-chan Event

// Config controls the TUI.
type Config struct {
	Context       context.Context
	Input         io.Reader
	Output        io.Writer
	SessionID     string
	SessionName   string
	InitialPrompt string
	Workspace     string
	Submit        Submit
	Store         sessionstore.Store
	History       []sessionstore.Item
}

// Run starts the interactive harness UI.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Submit == nil {
		return errors.New("ui submit function is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, cfg)
	options := []tea.ProgramOption{tea.WithContext(ctx)}
	if cfg.Input != nil {
		options = append(options, tea.WithInput(cfg.Input))
	}
	if cfg.Output != nil {
		options = append(options, tea.WithOutput(cfg.Output))
	}
	_, err := tea.NewProgram(m, options...).Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		return context.Canceled
	}
	return err
}

type model struct {
	ctx context.Context
	cfg Config

	input            composer
	transcript       *uilist.List
	spinner          spinner.Model
	run              <-chan Event
	toolCalls        map[string]*transcriptItem
	running          bool
	cancelRun        context.CancelFunc
	mainFocus        bool
	optimisticInputs map[string]int
	sessions         sessionPicker
	status           string
	sessionName      string
	tokenCount       int64
	width            int
	height           int
}

type initialPromptMsg struct{}

type eventMsg struct {
	event Event
}

type runStartedMsg struct {
	events <-chan Event
}

type sessionRenamedMsg struct {
	name string
	err  error
}

var (
	tuiBackground    = lipgloss.Color("#101017")
	tuiSurface       = lipgloss.Color("#171722")
	tuiSurface2      = lipgloss.Color("#211F31")
	tuiSelected      = lipgloss.Color("#29263B")
	tuiInk           = lipgloss.Color("#E6E6EB")
	tuiInkStrong     = lipgloss.Color("#F5F3FA")
	tuiMuted         = lipgloss.Color("#A39BB8")
	tuiSubtle        = lipgloss.Color("#8B879A")
	tuiFaint         = lipgloss.Color("#5C5968")
	tuiLine          = lipgloss.Color("#343142")
	tuiLineStrong    = lipgloss.Color("#514B68")
	tuiAccentAgent   = lipgloss.Color("#A78BFA")
	tuiAccentModel   = lipgloss.Color("#BBA7FF")
	tuiAccentTool    = lipgloss.Color("#F2C96D")
	tuiAccentInfo    = lipgloss.Color("#72C8F4")
	tuiAccentCommand = lipgloss.Color("#F38BA8")

	brandStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A78BFA"))
	mutedStyle      = lipgloss.NewStyle().Foreground(tuiSubtle)
	bodyStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#E6E6EB"))
	userStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#53D39A"))
	toolStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F2C96D"))
	errorStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF7A86"))
	reasoningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#A39BB8"))
	codeStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#D5C8FF")).Background(lipgloss.Color("#211F31")).Padding(0, 1)
	inlineCode      = lipgloss.NewStyle().Foreground(lipgloss.Color("#D5C8FF")).Background(lipgloss.Color("#29263B")).Padding(0, 1)
	statusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#72D6B0"))
	spinnerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	successStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#72D6B0"))
	toolStatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#B4ADBF"))
)

type densityMode uint8

const (
	densityComfortable densityMode = iota
	densityCompact
)

func fitHeaderText(value string, width int) string {
	value = strings.TrimSpace(value)
	if value == "" || ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, max(width, 1), "…")
}

func activeSelectorMarker(frame int) string {
	if frame%2 == 0 {
		return "✦"
	}
	return "✧"
}

func newModel(ctx context.Context, cfg Config) model {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.SessionID) == "" {
		cfg.SessionID = newSessionID()
	}
	input := newComposer()
	input.SetDensity(densityComfortable)
	transcript := uilist.NewList()
	transcript.SetGap(1)
	m := model{
		ctx:         ctx,
		cfg:         cfg,
		input:       input,
		transcript:  transcript,
		spinner:     spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(spinnerStyle)),
		toolCalls:   make(map[string]*transcriptItem),
		status:      "ready",
		sessionName: cfg.SessionName,
		width:       80,
		height:      24,
	}
	m.resize()
	for _, item := range cfg.History {
		m.consumeItem(item, true)
	}
	m.status = "ready"
	m.transcript.ScrollToBottom()
	return m
}

func (m model) Init() tea.Cmd {
	if strings.TrimSpace(m.cfg.InitialPrompt) != "" {
		return func() tea.Msg { return initialPromptMsg{} }
	}
	return m.input.Init()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width <= 0 || msg.Height <= 0 {
			return m, nil
		}
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case initialPromptMsg:
		m.input.SetValue(m.cfg.InitialPrompt)
		next, cmd := m.submit()
		return next, tea.Batch(m.input.Init(), cmd)
	case runStartedMsg:
		m.run = msg.events
		return m, waitForEvent(m.run)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.running {
			return m, cmd
		}
		return m, nil
	case eventMsg:
		if msg.event.Item != nil {
			m.consumeItem(*msg.event.Item, false)
		} else if msg.event.Line != "" {
			m.consumeLine(msg.event.Line)
		}
		if msg.event.Done {
			stopped := m.status == "stopping" || errors.Is(msg.event.Err, context.Canceled)
			m.running = false
			m.run = nil
			if m.cancelRun != nil {
				m.cancelRun()
				m.cancelRun = nil
			}
			m.optimisticInputs = nil
			m.status = "ready"
			if stopped {
				for _, item := range m.toolCalls {
					if item.value.status == "running" {
						m.updateToolItem(item, "canceled", item.value.detail)
					}
				}
				m.appendEntry(entry{role: "status", text: "Turn stopped. Send a message to continue."})
			}
			if msg.event.Err != nil && !errors.Is(msg.event.Err, context.Canceled) {
				m.status = "run failed"
				m.appendEntry(entry{role: "error", text: msg.event.Err.Error()})
			}
			return m, nil
		}
		if m.run != nil {
			return m, waitForEvent(m.run)
		}
		return m, nil
	case sessionRenamedMsg:
		if msg.err != nil {
			m.status = "rename failed"
			m.appendEntry(entry{role: "error", text: msg.err.Error()})
			return m, nil
		}
		m.sessionName = msg.name
		m.status = "renamed"
		m.appendEntry(entry{role: "status", text: "Session renamed to " + msg.name + "."})
		return m, nil
	case sessionsListedMsg:
		if msg.requestID != m.sessions.requestID || !m.sessions.open {
			return m, nil
		}
		m.sessions.loading = false
		m.sessions.err = msg.err
		m.sessions.items = msg.items
		m.sessions.selected = max(0, min(m.sessions.selected, len(msg.items)-1))
		return m, nil
	case sessionLoadedMsg:
		if msg.requestID != m.sessions.requestID || !m.sessions.open {
			return m, nil
		}
		m.sessions.loading = false
		if msg.err != nil {
			m.sessions.err = msg.err
			return m, nil
		}
		m.replaceSession(msg.id, msg.name, msg.items)
		return m, m.input.SetFocused(true)
	case tea.MouseClickMsg:
		if m.sessions.open {
			return m, nil
		}
		m.mainFocus = msg.Y >= 2 && msg.Y < 2+m.transcript.Height()
		return m, m.input.SetFocused(!m.mainFocus)
	case tea.MouseWheelMsg:
		if m.sessions.open {
			m.sessions.move(mouseScroll(msg.Button))
			return m, nil
		}
		if msg.Y >= 2 && msg.Y < 2+m.transcript.Height() {
			m.transcript.ScrollBy(mouseScroll(msg.Button) * 3)
			return m, nil
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			if m.running {
				m.cancelRun()
				m.status = "stopping"
				return m, nil
			}
			return m, tea.Quit
		}
		if m.sessions.open {
			return m.handleSessionKey(key)
		}
		if key == "ctrl+r" && !m.running {
			return m.openSessions()
		}
		if key == "tab" {
			m.mainFocus = !m.mainFocus
			return m, m.input.SetFocused(!m.mainFocus)
		}
		if m.mainFocus {
			if key == "esc" {
				m.mainFocus = false
				return m, m.input.SetFocused(true)
			}
			if key == "q" {
				return m, tea.Quit
			}
			m.scroll(key)
			return m, nil
		}
		if msg.Key().Code == tea.KeyEnter && msg.Key().Mod == 0 {
			return m.submit()
		}
		if m.scroll(key) {
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func mouseScroll(button tea.MouseButton) int {
	switch button {
	case tea.MouseWheelUp:
		return -1
	case tea.MouseWheelDown:
		return 1
	}
	return 0
}

const composerSectionGapRows = 1

func (m *model) resize() {
	width := max(m.width, 1)
	if m.height < 32 {
		m.input.SetDensity(densityCompact)
	} else {
		m.input.SetDensity(densityComfortable)
	}
	m.input.SetWidth(m.width)
	m.input.SetHeight(max(4, min(m.input.Height(), m.height-5)))
	follow := m.transcript.Len() == 0 || m.transcript.Height() <= 0 || m.transcript.AtBottom()
	reservedRows := 3 + 2*composerSectionGapRows // header, header gap, footer, and composer gaps
	m.transcript.SetSize(width, max(m.height-m.input.Height()-reservedRows, 1))
	if follow {
		m.transcript.ScrollToBottom()
	}
}

func (m model) submit() (tea.Model, tea.Cmd) {
	if m.running {
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	switch text {
	case "/resume", "/sessions":
		m.input.Reset()
		return m.openSessions()
	case "/new":
		m.replaceSession(newSessionID(), "", nil)
		return m, m.input.SetFocused(true)
	case "/quit", "/exit":
		return m, tea.Quit
	}
	if text == "/rename" || strings.HasPrefix(text, "/rename ") {
		name := strings.TrimSpace(strings.TrimPrefix(text, "/rename"))
		if name == "" {
			m.input.Reset()
			m.appendEntry(entry{role: "error", text: "Usage: /rename <session name>"})
			return m, nil
		}
		m.input.Reset()
		return m, m.renameSession(name)
	}
	prompt, ok := m.input.Submit()
	if !ok {
		return m, nil
	}
	m.appendEntry(entry{role: "user", text: prompt})
	if m.optimisticInputs == nil {
		m.optimisticInputs = make(map[string]int)
	}
	m.optimisticInputs[prompt]++
	m.transcript.ScrollToBottom()
	m.running = true
	m.status = "thinking"
	runContext, cancel := context.WithCancel(m.ctx)
	m.cancelRun = cancel
	submit, id := m.cfg.Submit, m.cfg.SessionID
	return m, tea.Batch(func() tea.Msg {
		return runStartedMsg{events: submit(runContext, id, prompt)}
	}, func() tea.Msg { return m.spinner.Tick() })
}

func (m model) renameSession(name string) tea.Cmd {
	store, ok := m.cfg.Store.(sessionstore.Renamer)
	if !ok {
		return func() tea.Msg { return sessionRenamedMsg{err: errors.New("session storage does not support renaming")} }
	}
	ctx, id := m.ctx, session.ID(m.cfg.SessionID)
	return func() tea.Msg {
		_, err := store.Rename(ctx, id, name)
		if errors.Is(err, fs.ErrNotExist) {
			// A fresh TUI session is created lazily on its first prompt. Make
			// /rename useful before that first prompt as well.
			if _, createErr := m.cfg.Store.Create(ctx, id); createErr != nil {
				return sessionRenamedMsg{name: name, err: fmt.Errorf("create session for rename: %w", createErr)}
			}
			_, err = store.Rename(ctx, id, name)
		}
		if err != nil {
			return sessionRenamedMsg{name: name, err: fmt.Errorf("rename session: %w", err)}
		}
		return sessionRenamedMsg{name: name}
	}
}

func waitForEvent(events <-chan Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return eventMsg{event: Event{Done: true}}
		}
		return eventMsg{event: event}
	}
}

func (m model) View() tea.View {
	width, height := max(m.width, 1), max(m.height, 1)
	if width < 8 || height < 10 {
		view := tea.NewView(fitRows("Enlarge terminal (8×10 minimum).", width, height))
		view.AltScreen = true
		return view
	}

	headerLeft := lipgloss.JoinHorizontal(lipgloss.Left,
		brandStyle.Render("UNREAL"),
		mutedStyle.Render(" / "),
		brandStyle.Render("AGENT"),
		mutedStyle.Render("  "+strings.TrimSpace(m.cfg.Workspace)),
	)
	headerRight := m.statusView()
	headerLeft = fitHeaderText(headerLeft, max(width-lipgloss.Width(headerRight)-1, 1))
	headerGap := max(width-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight), 1)
	header := headerLeft + strings.Repeat(" ", headerGap) + headerRight
	transcript := m.transcript.Render()
	if transcript == "" {
		transcript = mutedStyle.Width(width).Render("No messages yet. Type a prompt below.\nCtrl+R resumes a saved session.")
	}
	if m.sessions.open {
		transcript = m.sessions.render(width, m.transcript.Height())
	}
	transcript = fitRows(transcript, width, m.transcript.Height())
	sessionLabel := m.sessionName
	if strings.TrimSpace(sessionLabel) == "" {
		sessionLabel = shortID(m.cfg.SessionID)
	}
	input := m.input.View(false, false, false, "", 0, sessionLabel)
	footer := "pgup/pgdn scroll · tab focus · ctrl+r resume · /new · /rename · ctrl+c quit"
	if m.running {
		footer = "pgup/pgdn scroll · tab focus · ctrl+c stop · draft your next message"
	} else if m.mainFocus {
		footer = "↑/↓ scroll · home/end · tab or esc compose · ctrl+r resume · q quit"
	}
	content := lipgloss.JoinVertical(lipgloss.Left, header, "", transcript, "", input, "", mutedStyle.Render(footer))
	content = fitRows(content, width, height)
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "Unreal Agent"
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// fitRows reserves exact terminal cells so the composer stays fixed while
// transcript content grows or the window is resized.
func fitRows(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func formatTokenCount(value int64) string {
	if value < 0 {
		value = 0
	}
	digits := strconv.FormatInt(value, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}

func (m model) statusView() string {
	var status string
	if m.running {
		status = m.spinner.View() + " " + mutedStyle.Render(m.status)
	} else if m.status == "run failed" {
		status = errorStyle.Render("× failed")
	} else {
		status = statusStyle.Render("● ready")
	}
	tokens := mutedStyle.Render(formatTokenCount(m.tokenCount) + " tokens")
	return lipgloss.JoinHorizontal(lipgloss.Left, tokens, mutedStyle.Render("  "), status)
}

func (m *model) scroll(key string) bool {
	step := max(m.transcript.Height()/2, 1)
	switch key {
	case "up", "k", "ctrl+u":
		if !m.mainFocus {
			return false
		}
		if key == "up" || key == "k" {
			step = 1
		}
		m.transcript.ScrollBy(-step)
	case "down", "j", "ctrl+d":
		if !m.mainFocus {
			return false
		}
		if key == "down" || key == "j" {
			step = 1
		}
		m.transcript.ScrollBy(step)
	case "pgup":
		m.transcript.ScrollBy(-max(m.transcript.Height(), 1))
	case "pgdown":
		m.transcript.ScrollBy(max(m.transcript.Height(), 1))
	case "home":
		if !m.mainFocus {
			return false
		}
		m.transcript.ScrollToTop()
	case "end":
		if !m.mainFocus {
			return false
		}
		m.transcript.ScrollToBottom()
	default:
		return false
	}
	return true
}

func (m *model) appendEntry(value entry) {
	m.appendTranscriptItem(newTranscriptItem(value))
}

func (m *model) appendTranscriptItem(item *transcriptItem) {
	follow := m.transcript.Len() == 0 || m.transcript.Height() <= 0 || m.transcript.AtBottom()
	m.transcript.AppendItems(item)
	if follow {
		m.transcript.ScrollToBottom()
	}
}

func (m *model) updateToolItem(item *transcriptItem, status, detail string) {
	follow := m.transcript.Len() == 0 || m.transcript.Height() <= 0 || m.transcript.AtBottom()
	item.value.status = status
	item.value.detail = detail
	item.Bump()
	if follow {
		m.transcript.ScrollToBottom()
	}
}
