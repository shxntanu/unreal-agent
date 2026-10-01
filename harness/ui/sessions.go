package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"uuid"

	tea "charm.land/bubbletea/v2"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

type sessionPicker struct {
	open, loading bool
	items         []sessionstore.SessionInfo
	selected      int
	requestID     uint64
	err           error
}

type sessionsListedMsg struct {
	items     []sessionstore.SessionInfo
	requestID uint64
	err       error
}

type sessionLoadedMsg struct {
	id        string
	name      string
	items     []sessionstore.Item
	requestID uint64
	err       error
}

func newSessionID() string { return uuid.New().String() }

func (m model) openSessions() (tea.Model, tea.Cmd) {
	if m.cfg.Store == nil {
		m.appendEntry(entry{role: "error", text: "Session storage is unavailable."})
		return m, nil
	}
	m.sessions.open, m.sessions.loading = true, true
	m.sessions.err = nil
	m.sessions.selected = 0
	m.sessions.requestID++
	requestID, store, ctx := m.sessions.requestID, m.cfg.Store, m.ctx
	return m, tea.Batch(m.input.SetFocused(false), func() tea.Msg {
		items, err := store.ListSessions(ctx)
		slices.SortFunc(items, func(a, b sessionstore.SessionInfo) int {
			if order := b.LastUpdatedAt.Compare(a.LastUpdatedAt); order != 0 {
				return order
			}
			return strings.Compare(string(a.ID), string(b.ID))
		})
		return sessionsListedMsg{items: items, requestID: requestID, err: err}
	})
}

func (m model) handleSessionKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+r":
		m.sessions.open = false
		m.sessions.requestID++
		return m, m.input.SetFocused(!m.mainFocus)
	case "up", "k":
		m.sessions.move(-1)
	case "down", "j":
		m.sessions.move(1)
	case "home":
		m.sessions.selected = 0
	case "end":
		m.sessions.selected = max(len(m.sessions.items)-1, 0)
	case "enter":
		if m.sessions.loading || len(m.sessions.items) == 0 {
			return m, nil
		}
		id := string(m.sessions.items[m.sessions.selected].ID)
		m.sessions.loading = true
		m.sessions.err = nil
		requestID, ctx, store := m.sessions.requestID, m.ctx, m.cfg.Store
		return m, func() tea.Msg {
			items, err := readSessionHistory(ctx, store, id)
			return sessionLoadedMsg{id: id, name: m.sessions.items[m.sessions.selected].Name, items: items, requestID: requestID, err: err}
		}
	}
	return m, nil
}

func readSessionHistory(ctx context.Context, store sessionstore.Store, id string) ([]sessionstore.Item, error) {
	var items []sessionstore.Item
	after := sessionstore.BeforeFirst
	for {
		page, err := store.Items(ctx, session.ID(id), after, 1000)
		if err != nil {
			return nil, fmt.Errorf("load session %q: %w", id, err)
		}
		items = append(items, page.Items...)
		if !page.More {
			return items, nil
		}
		if page.NextAfter <= after {
			return nil, fmt.Errorf("load session %q: history cursor did not advance", id)
		}
		after = page.NextAfter
	}
}

func (m *model) replaceSession(id, name string, items []sessionstore.Item) {
	m.cfg.SessionID = id
	m.sessionName = name
	m.cfg.History = nil
	m.transcript.SetItems()
	m.transcript.ScrollToTop()
	m.toolCalls = make(map[string]*transcriptItem)
	m.optimisticInputs = nil
	m.tokenCount = 0
	for _, item := range items {
		m.consumeItem(item, true)
	}
	m.transcript.ScrollToBottom()
	m.sessions.open = false
	m.sessions.requestID++
	m.mainFocus = false
	m.input.Reset()
	m.status = "ready"
}

func (s *sessionPicker) move(delta int) {
	s.selected = max(0, min(s.selected+delta, len(s.items)-1))
}

func (s sessionPicker) render(width, height int) string {
	lines := []string{brandStyle.Render("Resume a session"), mutedStyle.Render("↑/↓ choose · enter resume · esc cancel")}
	if s.loading {
		return strings.Join(append(lines, "", "Loading sessions…"), "\n")
	}
	if s.err != nil {
		return strings.Join(append(lines, "", errorStyle.Render(s.err.Error())), "\n")
	}
	if len(s.items) == 0 {
		return strings.Join(append(lines, "", "No saved sessions yet. Send a message to save one."), "\n")
	}
	visible := max(height-3, 1)
	start := max(0, min(s.selected-visible/2, len(s.items)-visible))
	lines = append(lines, "")
	for i := start; i < min(start+visible, len(s.items)); i++ {
		item := s.items[i]
		label := item.Name
		if strings.TrimSpace(label) == "" {
			label = string(item.ID)
		}
		text := fmt.Sprintf("  %s  %s", item.LastUpdatedAt.Local().Format("Jan 02 15:04"), label)
		if i == s.selected {
			text = "›" + strings.TrimPrefix(text, " ")
			text = brandStyle.Render(text)
		}
		lines = append(lines, fitHeaderText(text, width))
	}
	return strings.Join(lines, "\n")
}
