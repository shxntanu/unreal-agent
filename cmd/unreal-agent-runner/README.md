# unreal-agent-runner

Run an AI agent from a prompt or JSON request. It writes events to stdout as
JSONL and exits when the task finishes.

Install with Go 1.27+:

```sh
go install github.com/unreallabsai/unreal-agent/cmd/unreal-agent-runner@latest
```

Set an OpenAI API key and run a prompt in the current directory:

```sh
export OPENAI_API_KEY="..."
unreal-agent-runner -p 'Inspect this project and explain how to run its tests.'
```

For an interactive terminal session, use the TUI mode. It keeps one durable
session while you submit multiple prompts:

```sh
unreal-agent-runner -tui
```

Use Enter to send, Shift+Enter for a newline, Tab to focus the transcript, and
Page Up/Page Down or the mouse wheel to scroll. Ctrl+C cancels an active turn
and quits when idle. Ctrl+R or `/resume` opens session selection, `/new` starts
a fresh session, `/rename <name>` renames the current session, and `/quit` exits. Sessions persist across prompts and runs.

Alt+Enter and Ctrl+J also insert newlines in terminals that cannot distinguish
Shift+Enter. Home/End, word movement, deletion, selection, and multiline paste
work in the composer. With transcript focus, arrows and Home/End navigate the
conversation; Tab or Esc returns to the composer.

An initial prompt can be supplied with `-p`:

```sh
unreal-agent-runner -tui -p 'Give me a quick tour of this project.'
```

Resume a specific session by ID, or start from the most recently updated
session:

```sh
unreal-agent-runner -tui -resume 123e4567-e89b-12d3-a456-426614174000
unreal-agent-runner -tui -resume latest
```

### Codex subscription

The runner can use the ChatGPT subscription credentials written by the Codex
CLI. Log in once, then select the `openai-codex` provider:

```sh
codex login
# alternatively: unreal-agent-runner -codex-login
export UNREAL_HARNESS_LLM_PROVIDER=openai-codex
export UNREAL_HARNESS_LLM_MODEL='your-codex-model'
unreal-agent-runner -p 'Inspect this project and explain how to run its tests.'
```

Without the explicit environment variables, credentials are read from
`$CODEX_HOME/auth.json`, or `$HOME/.codex/auth.json`. `OPENAI_CODEX_AUTH_FILE`
can point to another existing Codex auth file. Normal runs read the file only
and keep using the stored token; they do not copy or modify subscription
credentials. `-codex-login` delegates the interactive login to the installed
`codex` CLI and exits.

Or run from source at the repository root:

```sh
go run ./cmd/unreal-agent-runner -p 'Inspect this project and explain how to run its tests.'
```

Choose a workspace and save the output:

```sh
unreal-agent-runner -workspace ./my-project -p 'Summarize this project.' > run.jsonl
```

Sessions: `${XDG_STATE_HOME:-$HOME/.local/state}/unreal-agent/sessions`
(override with `-session-directory`).

You can also pass a JSON request as an argument or through stdin:

```sh
unreal-agent-runner '{"prompt":"Summarize this project."}'
unreal-agent-runner < request.json
```

OpenAI is the default provider. Set `UNREAL_HARNESS_LLM_PROVIDER` to `openai`,
`openai-codex`, `litellm`, `openrouter`, `fireworks`, or `ollama`, and
`UNREAL_HARNESS_LLM_MODEL` to choose a model.

For a local LiteLLM proxy:

```sh
export UNREAL_HARNESS_LLM_PROVIDER=litellm
export UNREAL_HARNESS_LLM_BASE_URL=http://localhost:4000/v1
export UNREAL_HARNESS_LLM_MODEL='provider/model'
export LITELLM_API_KEY='sk-...'
unreal-agent-runner -p 'Summarize this project.'
```

`LITELLM_API_KEY` is optional for unauthenticated proxies. The generic
`UNREAL_HARNESS_LLM_API_KEY` is also accepted.

Run `unreal-agent-runner -h` for options and the JSON request fields.

## Docker

The `unrea1labs/unreal-agent` image supports Linux on AMD64 and ARM64. Run it
with a project mounted as the workspace:

```sh
docker run --rm -i --user "$(id -u):$(id -g)" \
  -e OPENAI_API_KEY -v "$PWD:/workspace" \
  -v unreal-agent-state:/state \
  unrea1labs/unreal-agent:latest -p 'Summarize this project.'
```

Each release also publishes its Git tag (for example, `v0.1.0`) for version pinning.

To use an existing host subscription in Docker, mount the Codex auth directory
read-only and point `CODEX_HOME` at it:

```sh
docker run --rm -i --user "$(id -u):$(id -g)" \
  -e UNREAL_HARNESS_LLM_PROVIDER=openai-codex \
  -e UNREAL_HARNESS_LLM_MODEL='your-codex-model' \
  -e CODEX_HOME=/codex -v "$HOME/.codex:/codex:ro" \
  -v "$PWD:/workspace" -v unreal-agent-state:/state \
  unrea1labs/unreal-agent:latest -p 'Summarize this project.'
```
