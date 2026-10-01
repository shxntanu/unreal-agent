package ui

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	uilist "github.com/unreallabsai/unreal-agent/harness/ui/list"
)

type entry struct {
	role   string
	id     string
	title  string
	text   string
	detail string
	status string
}

type transcriptItem struct {
	*uilist.Versioned
	value entry
}

func newTranscriptItem(value entry) *transcriptItem {
	return &transcriptItem{Versioned: uilist.NewVersioned(), value: value}
}

func (item *transcriptItem) Render(width int) string { return renderEntry(item.value, width) }
func (item *transcriptItem) Finished() bool          { return true }

// consumeLine accepts one serialized session item from the event stream.
// Session history itself is typed; this adapter only exists for line-based
// callers and never exposes malformed JSON in the transcript.
func (m *model) consumeLine(line string) {
	var item sessionstore.Item
	if err := json.Unmarshal([]byte(line), &item); err != nil {
		m.appendEntry(entry{role: "error", text: "Unable to read a session event."})
		return
	}
	m.consumeItem(item, false)
}

// consumeItem is shared by live events and replayed session history. Live input
// echoes are matched against prompts already drawn by submit; replayed inputs
// are rendered as normal user messages.
func (m *model) consumeItem(item sessionstore.Item, replay bool) {
	switch item.Kind {
	case sessionstore.ItemInput:
		input, ok := item.Data.(inbox.Input)
		if !ok || input.Kind != inbox.InputExternal {
			return // Hide control and crash inputs: they are harness lifecycle data.
		}
		text := externalInputText(input.Payload)
		if text == "" {
			return
		}
		if !replay {
			if m.optimisticInputs != nil && m.optimisticInputs[text] > 0 {
				m.optimisticInputs[text]--
				if m.optimisticInputs[text] == 0 {
					delete(m.optimisticInputs, text)
				}
				return
			}
		}
		m.appendEntry(entry{role: "user", text: text})
	case sessionstore.ItemTurn:
		if !replay {
			m.status = "thinking"
		}
	case sessionstore.ItemModelResponse:
		response, ok := item.Data.(sessionstore.ModelResponse)
		if ok {
			m.consumeResponse(response.Response)
		}
	case sessionstore.ItemToolCallStatus:
		status, ok := item.Data.(sessionstore.ToolCallStatus)
		if ok {
			m.consumeToolStatus(status)
		}
	case sessionstore.ItemFork:
		// Fork records describe history lineage, not user-facing content.
	default:
		m.appendEntry(entry{role: "error", text: "Unsupported session event."})
	}
}

func externalInputText(payload jsontext.Value) string {
	if len(payload) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(payload, &text) == nil {
		return strings.TrimSpace(text)
	}
	var value struct {
		Message string `json:"message"`
		Text    string `json:"text"`
		Prompt  string `json:"prompt"`
		Content string `json:"content"`
	}
	if json.Unmarshal(payload, &value) == nil {
		for _, candidate := range []string{value.Message, value.Text, value.Prompt, value.Content} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func (m *model) consumeResponse(response llm.Response) {
	m.tokenCount += response.Usage.TotalTokens()
	if response.Failure != nil {
		failure := strings.TrimSpace(response.Failure.Message)
		code := strings.TrimSpace(response.Failure.Code)
		if code != "" && failure != "" {
			failure = code + ": " + failure
		} else if code != "" {
			failure = code
		}
		if failure == "" {
			failure = "Model response failed."
		}
		m.appendEntry(entry{role: "error", text: failure})
	}
	for _, output := range response.Output {
		switch output.Type {
		case llm.ItemMessage:
			message, ok := output.Data.(llm.Message)
			if ok && strings.TrimSpace(message.Text) != "" {
				role := "assistant"
				if message.Role == llm.RoleUser {
					role = "user"
				} else if message.Role == llm.RoleSystem {
					role = "status"
				}
				if role != "status" {
					m.appendEntry(entry{role: role, text: message.Text})
				}
			}
		case llm.ItemReasoning:
			reasoning, ok := output.Data.(llm.Reasoning)
			if ok && len(reasoning.Summary) != 0 {
				m.appendEntry(entry{role: "reasoning", text: strings.Join(reasoning.Summary, " ")})
			}
		case llm.ItemToolCall:
			call, ok := output.Data.(llm.ToolCall)
			if !ok {
				continue
			}
			title, body := formatToolCallParts(call.Name, call.Arguments)
			item := newTranscriptItem(entry{
				role: "tool", id: strings.TrimSpace(call.CallID), title: title,
				text: body, status: "running",
			})
			m.appendTranscriptItem(item)
			if item.value.id != "" {
				if m.toolCalls == nil {
					m.toolCalls = make(map[string]*transcriptItem)
				}
				m.toolCalls[item.value.id] = item
			}
		case llm.ItemToolResult:
			// Operation state snapshots on tool-call status items are the durable
			// source of tool output. Keep this fallback for older histories.
			result, ok := output.Data.(llm.ToolResult)
			if ok {
				m.consumeToolResult(result)
			}
		}
	}
}

func (m *model) consumeToolStatus(value sessionstore.ToolCallStatus) {
	status, detail := formatToolStatus(value.Status, value.Operations)
	callID := strings.TrimSpace(value.CallID)
	if item := m.toolCalls[callID]; item != nil {
		m.updateToolItem(item, status, detail)
		return
	}
	// A status may be the first retained item when replay begins mid-session or
	// a tool call was rejected before its model response was stored.
	title := "Tool"
	if callID != "" {
		title += " " + shortID(callID)
	}
	item := newTranscriptItem(entry{role: "tool", id: callID, title: title, detail: detail, status: status})
	m.appendTranscriptItem(item)
	if callID != "" {
		m.toolCalls[callID] = item
	}
}

func (m *model) consumeToolResult(value llm.ToolResult) {
	detail := formatToolResult(value.Output)
	if item := m.toolCalls[strings.TrimSpace(value.CallID)]; item != nil {
		m.updateToolItem(item, "completed", detail)
		return
	}
	m.appendEntry(entry{role: "tool", title: "Tool result", detail: detail, status: "completed"})
}

func renderEntry(item entry, width int) string {
	width = max(width, 1)
	switch item.role {
	case "user":
		return renderLabeledBlock(width, "You  ", userStyle, item.text, bodyStyle)
	case "assistant":
		return renderLabeledBlock(width, "Agent  ", brandStyle, item.text, bodyStyle)
	case "reasoning":
		return renderLabeledBlock(width, "Thinking  ", reasoningStyle, item.text, reasoningStyle.Italic(true))
	case "tool":
		return renderToolEntry(item, width)
	case "error":
		return renderLabeledBlock(width, "Error  ", errorStyle, item.text, bodyStyle)
	default:
		text := strings.TrimSpace(item.text)
		if text == "" {
			return ""
		}
		return renderLabeledBlock(width, "Info  ", mutedStyle, text, mutedStyle)
	}
}

func renderLabeledBlock(width int, label string, labelStyle lipgloss.Style, text string, textStyle lipgloss.Style) string {
	labelWidth := ansi.StringWidth(label)
	if width <= labelWidth+1 {
		label = ansi.Truncate(label, width, "")
		lines := renderRichLines(text, width, textStyle)
		return labelStyle.Render(label) + "\n" + strings.Join(lines, "\n")
	}
	lines := renderRichLines(text, width-labelWidth, textStyle)
	if len(lines) == 0 {
		lines = []string{""}
	}
	for i := range lines {
		if i == 0 {
			lines[i] = labelStyle.Render(label) + lines[i]
		} else {
			lines[i] = strings.Repeat(" ", min(labelWidth, width-1)) + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func renderRichText(text string, width int, textStyle lipgloss.Style) string {
	renderer := newMarkdownRenderer(max(width, 1))
	renderer.text = textStyle
	renderer.emphasis = textStyle.Italic(true)
	renderer.strong = textStyle.Bold(true)
	renderer.inlineCode = inlineCode
	renderer.codeBlock = codeStyle
	renderer.codeLabel = mutedStyle
	renderer.quote = reasoningStyle
	renderer.bullet = brandStyle
	renderer.rule = mutedStyle
	return renderer.Render(text)
}

func renderRichLines(text string, width int, textStyle lipgloss.Style) []string {
	rendered := renderRichText(text, width, textStyle)
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

const maxToolOutputLines = 10

func renderToolEntry(item entry, width int) string {
	title := strings.TrimSpace(item.title)
	if title == "" {
		title = "Tool"
	}
	status := item.status
	if status == "" {
		status = "running"
	}
	statusText := renderToolStatus(status)
	header := toolStyle.Render(title) + " " + statusText
	lines := []string{header}
	if ansi.StringWidth(header) > width {
		lines = strings.Split(ansi.Hardwrap(toolStyle.Render(title), width, false), "\n")
		lines = append(lines, strings.Split(ansi.Hardwrap(statusText, width, false), "\n")...)
	}
	indent := min(2, max(width-1, 0))
	bodyWidth := max(width-indent, 1)
	if text := strings.TrimSpace(item.text); text != "" {
		lines = append(lines, renderToolCode(text, bodyWidth, indent)...)
	}
	if detail := strings.TrimSpace(item.detail); detail != "" {
		detailLines := make([]string, 0)
		if status == "failed" {
			for _, line := range renderRichLines(detail, bodyWidth, errorStyle) {
				detailLines = append(detailLines, strings.Repeat(" ", indent)+line)
			}
		} else if status == "running" {
			for _, line := range renderRichLines(detail, bodyWidth, toolStatusStyle) {
				detailLines = append(detailLines, strings.Repeat(" ", indent)+line)
			}
		} else {
			detailLines = renderToolCode(detail, bodyWidth, indent)
		}
		lines = append(lines, capToolOutputLines(detailLines, indent)...)
	}
	return strings.Join(lines, "\n")
}

func capToolOutputLines(lines []string, indent int) []string {
	if len(lines) <= maxToolOutputLines {
		return lines
	}

	// Keep both ends: command output commonly puts the useful summary or an
	// error at the end, while the beginning usually contains the main result.
	keepHead := (maxToolOutputLines - 1) / 2
	keepTail := maxToolOutputLines - 1 - keepHead
	omitted := len(lines) - keepHead - keepTail
	result := make([]string, 0, maxToolOutputLines)
	result = append(result, lines[:keepHead]...)
	result = append(result, strings.Repeat(" ", indent)+toolStatusStyle.Render(fmt.Sprintf("… %d output lines omitted …", omitted)))
	result = append(result, lines[len(lines)-keepTail:]...)
	return result
}

func renderToolCode(text string, width, indent int) []string {
	frame := codeStyle.GetHorizontalFrameSize()
	contentWidth := max(width-frame, 1)
	wrapped := ansi.Hardwrap(strings.ReplaceAll(text, "\t", "    "), contentWidth, true)
	lines := strings.Split(wrapped, "\n")
	for i := range lines {
		if width <= frame {
			lines[i] = strings.Repeat(" ", indent) + lines[i]
		} else {
			lines[i] = strings.Repeat(" ", indent) + codeStyle.Width(width).Render(lines[i])
		}
	}
	return lines
}

func renderToolStatus(status string) string {
	switch status {
	case "completed":
		return successStyle.Render("✓ done")
	case "failed":
		return errorStyle.Render("× failed")
	case "canceled":
		return mutedStyle.Render("× canceled")
	default:
		return toolStatusStyle.Render("… working")
	}
}

func formatToolCallParts(name, arguments string) (string, string) {
	title := prettyToolName(name)
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || arguments == "{}" {
		return title, ""
	}
	var values map[string]any
	if json.Unmarshal([]byte(arguments), &values) == nil {
		for _, key := range []string{"command", "cmd", "path", "query", "pattern", "url", "skill", "name"} {
			if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
				if strings.EqualFold(title, "Bash") || strings.EqualFold(title, "Shell") {
					return title, "$ " + strings.TrimSpace(value)
				}
				return title, strings.TrimSpace(value)
			}
		}
		if formatted, err := json.Marshal(values, jsontext.WithIndent("  ")); err == nil {
			return title, string(formatted)
		}
	}
	return title, arguments
}

func prettyToolName(name string) string {
	compact := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(name)))
	switch compact {
	case "viewimage":
		return "View image"
	case "skilluse":
		return "Use skill"
	case "joboutput":
		return "Job output"
	case "jobkill":
		return "Kill job"
	}
	parts := strings.FieldsFunc(strings.TrimSpace(name), func(r rune) bool { return r == '_' || r == '-' })
	if len(parts) == 0 {
		return "Tool"
	}
	for i, part := range parts {
		runes := []rune(part)
		if len(runes) != 0 {
			runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
			parts[i] = string(runes)
		}
	}
	return strings.Join(parts, " ")
}

func formatToolStatus(status tool.CallStatus, operations []operation.Operation) (string, string) {
	if errorText := strings.TrimSpace(status.Error); errorText != "" {
		return "failed", errorText
	}
	allTerminal, hasFailed, hasCanceled := len(operations) != 0, false, false
	details := make([]string, 0, len(operations))
	for _, current := range operations {
		switch current.Status {
		case operation.StatusFailed:
			hasFailed = true
		case operation.StatusCanceled:
			hasCanceled = true
		case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling, "":
			allTerminal = false
		}
		if detail := formatOperationOutput(current); detail != "" {
			details = append(details, detail)
		}
	}
	if hasFailed {
		return "failed", strings.Join(details, "\n")
	}
	if hasCanceled {
		return "canceled", strings.Join(details, "\n")
	}
	if allTerminal {
		return "completed", strings.Join(details, "\n")
	}
	if len(status.WaitingFor) != 0 {
		ids := make([]string, len(status.WaitingFor))
		for i, id := range status.WaitingFor {
			ids[i] = string(id)
		}
		return "running", "waiting for " + strings.Join(ids, ", ")
	}
	return "running", "working…"
}

func formatOperationOutput(current operation.Operation) string {
	if len(current.State) == 0 {
		return ""
	}
	var state struct {
		Result *struct {
			Out      string
			Err      string
			ExitCode int
			Error    string
		}
		TerminalResult string
		TerminalError  string
		Error          string
		Value          jsontext.Value
	}
	if json.Unmarshal(current.State, &state) != nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if state.Result != nil {
		if state.Result.Out != "" {
			parts = append(parts, state.Result.Out)
		}
		if state.Result.Err != "" {
			parts = append(parts, "stderr:\n"+state.Result.Err)
		}
		if state.Result.Error != "" {
			parts = append(parts, "error: "+state.Result.Error)
		}
		if state.Result.ExitCode != 0 {
			parts = append(parts, fmt.Sprintf("exit code: %d", state.Result.ExitCode))
		}
	}
	if state.TerminalResult != "" {
		parts = append(parts, state.TerminalResult)
	}
	if state.TerminalError != "" {
		parts = append(parts, "error: "+state.TerminalError)
	}
	if state.Error != "" {
		parts = append(parts, "error: "+state.Error)
	}
	if len(parts) == 0 && len(state.Value) != 0 && string(state.Value) != "null" {
		var value any
		if json.Unmarshal(state.Value, &value) == nil {
			if encoded, err := json.Marshal(value, jsontext.WithIndent("  ")); err == nil {
				parts = append(parts, string(encoded))
			}
		}
	}
	return limitToolOutput(strings.Join(parts, "\n"))
}

func formatToolResult(output []llm.ToolResultOutput) string {
	parts := make([]string, 0, len(output))
	for _, item := range output {
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		if item.Kind == llm.ToolResultImage {
			parts = append(parts, "[image output]")
		} else {
			parts = append(parts, value)
		}
	}
	return limitToolOutput(strings.Join(parts, "\n"))
}

func limitToolOutput(value string) string {
	const maxRunes = 8000
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "\n… output truncated …"
}

func shortID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:8] + "…"
}
