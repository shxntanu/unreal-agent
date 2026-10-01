package agentrunner

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	harnessui "github.com/unreallabsai/unreal-agent/harness/ui"
)

func TestTUISubmitsDurableTurnsAndLoadsResumeHistory(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	var requests []llm.Request
	client := &fakeClient{respond: func(_ context.Context, request llm.Request) (llm.Response, error) {
		requests = append(requests, request)
		return llm.Response{Output: []llm.Item{{
			Type: llm.ItemMessage,
			Data: llm.Message{Role: llm.RoleAssistant, Text: "answer " + string(rune('0'+len(requests)))},
		}}}, nil
	}}
	getenv := func(name string) string {
		switch name {
		case llmAPIKeyEnvironment:
			return "secret"
		case "SHELL":
			return "/bin/sh"
		default:
			return ""
		}
	}
	uiConfig, err := newTUIConfig(
		t.Context(), getenv, func() []string { return []string{"PATH=/usr/bin:/bin"} }, nil, io.Discard,
		"", sessions, workspace, "", time.Second, "", testConfig(client),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(uiConfig.History) != 0 {
		t.Fatalf("new session history = %d items, want empty", len(uiConfig.History))
	}
	for _, prompt := range []string{"first prompt", "second prompt"} {
		runTUISubmit(t, uiConfig.Submit(t.Context(), uiConfig.SessionID, prompt))
	}
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(requests))
	}
	var secondMessages []string
	for _, item := range requests[1].Input {
		if item.Type == llm.ItemMessage {
			secondMessages = append(secondMessages, item.Data.(llm.Message).Text)
		}
	}
	for _, want := range []string{"first prompt", "answer 1", "second prompt"} {
		if !containsString(secondMessages, want) {
			t.Fatalf("second request messages %q do not contain %q", secondMessages, want)
		}
	}

	resumed, err := newTUIConfig(
		t.Context(), getenv, func() []string { return []string{"PATH=/usr/bin:/bin"} }, nil, io.Discard,
		"", sessions, workspace, "", time.Second, uiConfig.SessionID, testConfig(client),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.History) == 0 {
		t.Fatal("resumed TUI did not receive persisted history")
	}
	var prompts []string
	for _, item := range resumed.History {
		if item.Kind != sessionstore.ItemInput {
			continue
		}
		if input, ok := item.Data.(inbox.Input); ok && input.Kind == inbox.InputExternal {
			var prompt string
			if err := json.Unmarshal(input.Payload, &prompt); err != nil {
				t.Fatal(err)
			}
			prompts = append(prompts, prompt)
		}
	}
	if strings.Join(prompts, ",") != "first prompt,second prompt" {
		t.Fatalf("persisted prompts = %q", prompts)
	}
	if id, err := resolveTUISession(t.Context(), resumed.Store, "latest"); err != nil || string(id) != uiConfig.SessionID {
		t.Fatalf("latest session = %q, err = %v", id, err)
	}
}

func TestTUIResumeRejectsMissingSession(t *testing.T) {
	store, err := localfile.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTUISession(t.Context(), store, "missing-session"); err == nil {
		t.Fatal("missing resume ID unexpectedly succeeded")
	}
	if _, err := resolveTUISession(t.Context(), store, "latest"); err == nil {
		t.Fatal("latest unexpectedly succeeded without sessions")
	}
	if _, err := store.Inspect(t.Context(), session.ID("missing-session")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing session inspect error = %v", err)
	}
}

func TestResumeFlagRequiresTUI(t *testing.T) {
	err := Run(t.Context(), []string{"-resume", "some-session"}, func(string) string { return "" },
		func() []string { return nil }, strings.NewReader("{}"), io.Discard, io.Discard, Config{
			Name: "runner", ParseRequest: func(io.Reader) (Request, ToolFactory, error) {
				return Request{}, func(context.Context, ToolConfig) (Tools, error) { return Tools{}, nil }, nil
			},
		})
	if err == nil || !strings.Contains(err.Error(), "-resume requires -tui") {
		t.Fatalf("Run error = %v, want non-TUI resume rejection", err)
	}
}

func runTUISubmit(t *testing.T, events <-chan harnessui.Event) {
	t.Helper()
	for event := range events {
		if event.Done && event.Err != nil {
			t.Fatalf("TUI submission failed: %v", event.Err)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
