package agentrunner

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
	"uuid"

	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	harnessui "github.com/unreallabsai/unreal-agent/harness/ui"
)

func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == name {
			set = true
		}
	})
	return set
}

func runTUI(
	ctx context.Context,
	getenv func(string) string,
	environ func() []string,
	input io.Reader,
	output io.Writer,
	initialPrompt string,
	sessionDirectory string,
	workspaceDirectory string,
	logDirectory string,
	toolHeartbeatInterval time.Duration,
	resume string,
	config Config,
) error {
	uiContext, cancel := context.WithCancel(ctx)
	defer cancel()

	uiConfig, err := newTUIConfig(
		uiContext, getenv, environ, input, output, initialPrompt,
		sessionDirectory, workspaceDirectory, logDirectory,
		toolHeartbeatInterval, resume, config,
	)
	if err != nil {
		return err
	}
	return harnessui.Run(uiContext, uiConfig)
}

func newTUIConfig(
	ctx context.Context,
	getenv func(string) string,
	environ func() []string,
	input io.Reader,
	output io.Writer,
	initialPrompt string,
	sessionDirectory string,
	workspaceDirectory string,
	logDirectory string,
	toolHeartbeatInterval time.Duration,
	resume string,
	config Config,
) (harnessui.Config, error) {
	storeDirectory, err := resolveSessionDirectory(sessionDirectory, getenv)
	if err != nil {
		return harnessui.Config{}, fmt.Errorf("resolve session directory: %w", err)
	}
	store, err := localfile.New(storeDirectory)
	if err != nil {
		return harnessui.Config{}, fmt.Errorf("open session store: %w", err)
	}

	sessionID := session.ID(uuid.New().String())
	var history []sessionstore.Item
	sessionName := ""
	if resume != "" {
		id, err := resolveTUISession(ctx, store, resume)
		if err != nil {
			return harnessui.Config{}, err
		}
		sessionID = id
		history, err = readSessionHistory(ctx, store, id)
		if err != nil {
			return harnessui.Config{}, err
		}
		snapshot, err := store.Inspect(ctx, id)
		if err != nil {
			return harnessui.Config{}, err
		}
		sessionName = snapshot.Session.Name
	}

	submit := func(runContext context.Context, id, prompt string) <-chan harnessui.Event {
		events := make(chan harnessui.Event, 128)
		go func() {
			defer close(events)
			request := Request{Messages: []RequestMessage{{Content: prompt}}, SessionID: &id}
			encoded, err := json.Marshal(request)
			if err == nil {
				args := []string{"-session-directory", storeDirectory}
				if workspaceDirectory != "" {
					args = append(args, "-workspace", workspaceDirectory)
				}
				if logDirectory != "" {
					args = append(args, "-log-directory", logDirectory)
				}
				args = append(args, "-tool-heartbeat-interval", toolHeartbeatInterval.String())
				writer := &tuiEventWriter{ctx: runContext, events: events}
				err = Run(runContext, args, getenv, environ, bytes.NewReader(encoded), writer, io.Discard, config)
				if flushErr := writer.Flush(); flushErr != nil {
					err = errors.Join(err, flushErr)
				}
			}
			_ = sendTUIEvent(runContext, events, harnessui.Event{Done: true, Err: err})
		}()
		return events
	}

	return harnessui.Config{
		Context:       ctx,
		Input:         input,
		Output:        output,
		SessionID:     string(sessionID),
		SessionName:   sessionName,
		InitialPrompt: initialPrompt,
		Workspace:     workspaceDirectory,
		Store:         store,
		History:       history,
		Submit:        submit,
	}, nil
}

func resolveTUISession(
	ctx context.Context,
	store sessionstore.Store,
	requested string,
) (session.ID, error) {
	requested = strings.TrimSpace(requested)
	if requested == "latest" {
		sessions, err := store.ListSessions(ctx)
		if err != nil {
			return "", fmt.Errorf("list sessions: %w", err)
		}
		if len(sessions) == 0 {
			return "", errors.New("there are no sessions to resume")
		}
		latest := sessions[0]
		for _, candidate := range sessions[1:] {
			if candidate.LastUpdatedAt.After(latest.LastUpdatedAt) {
				latest = candidate
			}
		}
		return latest.ID, nil
	}
	if requested == "" {
		return "", errors.New("-resume requires a session ID or latest")
	}
	id := session.ID(requested)
	if _, err := store.Inspect(ctx, id); err != nil {
		return "", fmt.Errorf("resume session %q: %w", id, err)
	}
	return id, nil
}

func readSessionHistory(
	ctx context.Context,
	store sessionstore.Store,
	id session.ID,
) ([]sessionstore.Item, error) {
	var items []sessionstore.Item
	after := sessionstore.BeforeFirst
	for {
		page, err := store.Items(ctx, id, after, 128)
		if err != nil {
			return nil, fmt.Errorf("read session %q history: %w", id, err)
		}
		items = append(items, page.Items...)
		if !page.More {
			return items, nil
		}
		if page.NextAfter == after {
			return nil, fmt.Errorf("read session %q history: store returned an empty page with more items", id)
		}
		after = page.NextAfter
	}
}

type tuiEventWriter struct {
	ctx     context.Context
	events  chan<- harnessui.Event
	pending string
}

func (writer *tuiEventWriter) Write(value []byte) (int, error) {
	writer.pending += string(value)
	for {
		line, rest, found := strings.Cut(writer.pending, "\n")
		if !found {
			break
		}
		writer.pending = rest
		if err := writer.writeLine(strings.TrimSuffix(line, "\r")); err != nil {
			return 0, err
		}
	}
	return len(value), nil
}

func (writer *tuiEventWriter) Flush() error {
	line := strings.TrimSuffix(writer.pending, "\r")
	writer.pending = ""
	return writer.writeLine(line)
}

func (writer *tuiEventWriter) writeLine(line string) error {
	if line == "" {
		return nil
	}
	var item sessionstore.Item
	if err := json.Unmarshal([]byte(line), &item); err != nil {
		return fmt.Errorf("decode session event: %w", err)
	}
	event := harnessui.Event{Line: line, Item: &item}
	return sendTUIEvent(writer.ctx, writer.events, event)
}

func sendTUIEvent(ctx context.Context, events chan<- harnessui.Event, event harnessui.Event) error {
	select {
	case events <- event:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
