package ui

import (
	"context"
	"encoding/json/v2"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type inspectUI struct{ reply chan uiSnapshot }
type uiSnapshot struct {
	view, draft, sessionID             string
	running, atBottom, picker, loading bool
	offset, line, entries, sessions    int
}

// observedUI runs the production model in a real Bubble Tea event loop. The
// inspection message captures immutable values on that loop for the test.
type observedUI struct{ model }

func (m observedUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if query, ok := msg.(inspectUI); ok {
		offset, line := m.transcript.ScrollPosition()
		query.reply <- uiSnapshot{
			view: ansi.Strip(m.View().Content), draft: m.input.Value(),
			sessionID: m.cfg.SessionID, running: m.running,
			atBottom: m.transcript.AtBottom(), picker: m.sessions.open,
			loading: m.sessions.loading, offset: offset, line: line,
			entries: m.transcript.Len(), sessions: len(m.sessions.items),
		}
		return m, nil
	}
	next, cmd := m.model.Update(msg)
	m.model = next.(model)
	return m, cmd
}

func TestUIIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := localfile.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, "saved"); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal("original prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendInput(ctx, "saved", inbox.Input{ID: "input", Kind: inbox.InputExternal, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(ctx, "saved", session.Turn{ID: "turn", Type: session.TurnRegular}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendModelResponse(ctx, "saved", sessionstore.ModelResponse{TurnID: "turn", Response: llm.Response{Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "original answer"}}}}}); err != nil {
		t.Fatal(err)
	}

	type turn struct {
		ctx        context.Context
		id, prompt string
		events     chan Event
	}
	started := make(chan turn, 1)
	cfg := Config{SessionID: "fresh", Store: store, Submit: func(ctx context.Context, id, prompt string) <-chan Event {
		events := make(chan Event, 8)
		started <- turn{ctx: ctx, id: id, prompt: prompt, events: events}
		return events
	}}
	program := tea.NewProgram(observedUI{newModel(ctx, cfg)}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard))
	finished := make(chan error, 1)
	go func() { _, err := program.Run(); finished <- err }()
	defer program.Kill()
	inspect := func() uiSnapshot {
		t.Helper()
		reply := make(chan uiSnapshot, 1)
		program.Send(inspectUI{reply})
		select {
		case state := <-reply:
			return state
		case <-ctx.Done():
			t.Fatal("UI event loop did not respond")
			return uiSnapshot{}
		}
	}
	await := func(predicate func(uiSnapshot) bool) uiSnapshot {
		t.Helper()
		for {
			state := inspect()
			if predicate(state) {
				return state
			}
			select {
			case <-ctx.Done():
				t.Fatalf("UI did not reach expected state: %+v", state)
			case <-time.After(time.Millisecond):
			}
		}
	}
	key := func(code rune, mod tea.KeyMod) {
		msg := tea.KeyPressMsg{Code: code, Mod: mod}
		if mod == 0 && code >= ' ' && code <= '~' {
			msg.Text = string(code)
		}
		program.Send(msg)
	}
	replaceDraft := func(text string) { key('g', tea.ModCtrl); program.Send(tea.PasteMsg{Content: text}) }
	program.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	key('q', 0)
	if state := inspect(); state.draft != "q" {
		t.Fatalf("q quit or failed to type: %q", state.draft)
	}
	key('u', tea.ModCtrl)
	longDraft := strings.Repeat("multiline 東京🙂\n", 120)
	program.Send(tea.PasteMsg{Content: longDraft})
	if state := inspect(); state.draft != longDraft || state.running {
		t.Fatal("multiline paste was truncated or sent")
	}
	replaceDraft("hello")
	key(tea.KeyHome, 0)
	key('d', tea.ModCtrl)
	key(tea.KeyEnd, 0)
	key(tea.KeyEnter, tea.ModShift)
	if state := inspect(); state.draft != "ello\n" {
		t.Fatalf("editing/newline keys = %q", state.draft)
	}
	key(tea.KeyEnter, 0)
	var active turn
	select {
	case active = <-started:
	case <-ctx.Done():
		t.Fatal("submit did not start")
	}
	if active.id != "fresh" || active.prompt != "ello" {
		t.Fatalf("turn = %q, %q", active.id, active.prompt)
	}
	payload, err = json.Marshal("ello")
	if err != nil {
		t.Fatal(err)
	}
	echo := sessionstore.Item{Kind: sessionstore.ItemInput, Data: inbox.Input{ID: "echo", Kind: inbox.InputExternal, Payload: payload}}
	active.events <- Event{Item: &echo}
	output := "```go\n" + strings.Repeat("fmt.Println(\"東京🙂 output\")\n", 80) + "```"
	response := sessionstore.Item{Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{Response: llm.Response{Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: output}}}}}}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	active.events <- Event{Line: string(encoded)}
	state := await(func(s uiSnapshot) bool { return s.entries == 2 })
	if !state.atBottom {
		t.Fatal("output did not follow the bottom")
	}
	key(tea.KeyPgUp, 0)
	before := inspect()
	response.Data = sessionstore.ModelResponse{Response: llm.Response{Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "New output"}}}}}
	active.events <- Event{Item: &response}
	state = await(func(s uiSnapshot) bool { return s.entries == 3 })
	if state.offset != before.offset || state.line != before.line {
		t.Fatal("incoming output pulled away from earlier messages")
	}
	program.Send(tea.MouseWheelMsg{X: 1, Y: 3, Button: tea.MouseWheelUp})
	state = inspect()
	if state.offset == before.offset && state.line == before.line {
		t.Fatal("mouse wheel did not scroll")
	}
	key(tea.KeyTab, 0)
	key(tea.KeyEnd, 0)
	if !inspect().atBottom {
		t.Fatal("End with transcript focus did not return to latest")
	}
	key(tea.KeyTab, 0)
	program.Send(tea.PasteMsg{Content: "next draft"})
	key(tea.KeyEnter, 0)
	if inspect().draft != "next draft" {
		t.Fatal("active turn discarded the next draft")
	}
	key('c', tea.ModCtrl)
	inspect()
	if active.ctx.Err() != context.Canceled {
		t.Fatal("Ctrl+C did not cancel the turn")
	}
	close(active.events)
	state = await(func(s uiSnapshot) bool { return !s.running })
	if state.draft != "next draft" {
		t.Fatal("canceling lost the draft")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 32, Height: 12}, {Width: 8, Height: 10}, {Width: 120, Height: 40}} {
		program.Send(size)
		state = inspect()
		lines := strings.Split(state.view, "\n")
		if len(lines) != size.Height {
			t.Fatalf("rendered %d rows in %d-row terminal", len(lines), size.Height)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("row exceeds %d columns: %q", size.Width, line)
			}
		}
	}
	program.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	key('r', tea.ModCtrl)
	state = await(func(s uiSnapshot) bool { return s.picker && !s.loading })
	if state.sessions != 1 {
		t.Fatal("saved session missing from picker")
	}
	key(tea.KeyEscape, 0)
	if inspect().draft != "next draft" {
		t.Fatal("closing the picker lost the draft")
	}
	key('r', tea.ModCtrl)
	await(func(s uiSnapshot) bool { return s.picker && !s.loading })
	key(tea.KeyEnter, 0)
	state = await(func(s uiSnapshot) bool { return s.sessionID == "saved" })
	if !strings.Contains(state.view, "original prompt") || !strings.Contains(state.view, "original answer") {
		t.Fatalf("saved transcript was not replayed: %s", state.view)
	}
	replaceDraft("/new")
	key(tea.KeyEnter, 0)
	state = inspect()
	if state.sessionID == "saved" || state.entries != 0 {
		t.Fatal("/new did not create a fresh session")
	}
	if items, err := readSessionHistory(ctx, store, "saved"); err != nil || len(items) != 3 {
		t.Fatalf("old history changed: %d items, %v", len(items), err)
	}
	key('c', tea.ModCtrl)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("UI did not quit")
	}
}
