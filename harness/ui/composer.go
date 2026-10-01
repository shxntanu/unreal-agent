package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	compactChatBoxInputHeight     = 3
	comfortableChatBoxInputHeight = 5
	composerStatusHeight          = 1
	composerPaddingWidth          = 2
)

// composer is used to compose messages, run slash commands, etc.
type composer struct {
	input textarea.Model
	width int
	style lipgloss.Style
}

func newComposer() composer {
	input := textarea.New()
	input.Placeholder = "Message the agent..."
	input.Prompt = "› "
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	// The viewport scrolls, but the text itself should not have a small line cap.
	input.MaxHeight = 0
	input.MaxWidth = 0
	input.SetHeight(comfortableChatBoxInputHeight)
	input.Focus()

	styles := textarea.DefaultDarkStyles()
	styles.Focused.Base = styles.Focused.Base.Background(tuiSurface2)
	styles.Focused.Text = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiInk)
	styles.Focused.Prompt = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiAccentTool).
		Bold(true)
	styles.Focused.CursorLine = lipgloss.NewStyle().Background(tuiSurface2)
	styles.Focused.Placeholder = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiMuted)
	styles.Focused.EndOfBuffer = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiSurface2)
	styles.Blurred.Base = styles.Blurred.Base.Background(tuiSurface2)
	styles.Blurred.Text = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiInk)
	styles.Blurred.Prompt = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiSubtle)
	styles.Blurred.CursorLine = lipgloss.NewStyle().Background(tuiSurface2)
	styles.Blurred.Placeholder = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiMuted)
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle().
		Background(tuiSurface2).
		Foreground(tuiSurface2)
	styles.Cursor.Color = tuiAccentTool
	input.SetStyles(styles)

	return composer{
		input: input,
		style: lipgloss.NewStyle().
			Background(tuiSurface2).
			Foreground(tuiInk).
			Padding(0, 1),
	}
}

func (c composer) Init() tea.Cmd {
	return c.input.Focus()
}

func (c *composer) SetFocused(focused bool) tea.Cmd {
	if focused {
		return c.input.Focus()
	}
	c.input.Blur()
	return nil
}

func (c composer) Update(msg tea.Msg) (composer, tea.Cmd) {
	if !c.input.Focused() {
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(msg)
		return c, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if composerNewlineKey(key) {
			var cmd tea.Cmd
			c.input, cmd = c.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return c, cmd
		}
		if c.handleCommandBackspace(key) {
			return c, nil
		}
		if c.handleCommandArrow(key) {
			return c, nil
		}
	}

	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}

func composerNewlineKey(key tea.KeyPressMsg) bool {
	switch key.Keystroke() {
	case "shift+enter", "alt+enter", "ctrl+j":
		return true
	}
	return key.Code == tea.KeyEnter && key.Mod.Contains(tea.ModShift)
}

func (c *composer) handleCommandArrow(key tea.KeyPressMsg) bool {
	switch {
	case key.Code == tea.KeyLeft && key.Mod&tea.ModMeta != 0,
		key.Keystroke() == "meta+left",
		key.Keystroke() == "cmd+left",
		key.Keystroke() == "super+left":
		c.input.ClearSelection()
		c.input.CursorStart()
		return true
	case key.Code == tea.KeyRight && key.Mod&tea.ModMeta != 0,
		key.Keystroke() == "meta+right",
		key.Keystroke() == "cmd+right",
		key.Keystroke() == "super+right":
		c.input.ClearSelection()
		c.input.CursorEnd()
		return true
	default:
		return false
	}
}

func (c *composer) handleCommandBackspace(key tea.KeyPressMsg) bool {
	switch {
	case key.Code == tea.KeyBackspace && key.Mod&tea.ModMeta != 0,
		key.Keystroke() == "meta+backspace",
		key.Keystroke() == "cmd+backspace",
		key.Keystroke() == "super+backspace":
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		_ = cmd
		return true
	default:
		return false
	}
}

func (c composer) View(slashMode bool, skillMode bool, fileMode bool, skillPrefix string,
	_ int, sessionLabel string) string {
	modeColor := tuiAccentAgent
	modeLabel := "message"
	modeHint := "enter to send · shift+enter for newline"
	if slashMode {
		modeColor = tuiAccentCommand
		modeLabel = "command"
		modeHint = "tab completes · enter runs"
	} else if skillMode {
		modeColor = tuiAccentTool
		modeLabel = "skill"
		modeHint = "tab completes · enter selects · esc closes"
	} else if fileMode {
		modeColor = tuiAccentInfo
		modeLabel = "file"
		modeHint = "arrows select · enter pastes · esc closes"
	}
	if !slashMode && strings.TrimSpace(skillPrefix) != "" {
		modeColor = tuiAccentTool
	}

	status := c.modeLine(modeLabel, modeHint, modeColor, sessionLabel)
	content := lipgloss.JoinVertical(lipgloss.Left, c.inputView(), status)
	return c.style.Width(max(0, c.width)).Render(content)
}

func (c composer) modeLine(modeLabel string, modeHint string, modeColor color.Color,
	sessionLabel string) string {
	contentWidth := max(1, c.width-composerPaddingWidth)
	sessionLabel = strings.TrimSpace(sessionLabel)
	if sessionLabel == "" {
		sessionLabel = "untitled"
	}
	mode := lipgloss.NewStyle().Foreground(modeColor).Bold(true).Render(modeLabel)
	hint := lipgloss.NewStyle().Foreground(tuiMuted).Render(modeHint)
	left := mode + "  " + hint
	leftWidth := ansi.StringWidth(left)
	sessionPrefix := lipgloss.NewStyle().Foreground(tuiSubtle).Render("session ")
	prefixWidth := ansi.StringWidth(sessionPrefix)
	if leftWidth+prefixWidth+2 > contentWidth {
		return fitHeaderText(left, contentWidth)
	}
	availableLabel := max(1, contentWidth-leftWidth-prefixWidth-1)
	session := sessionPrefix + lipgloss.NewStyle().Foreground(tuiAccentAgent).Bold(true).
		Render(fitHeaderText(sessionLabel, availableLabel))
	sessionWidth := ansi.StringWidth(session)
	if leftWidth+sessionWidth+1 > contentWidth {
		return fitHeaderText(left, contentWidth)
	}
	return left + strings.Repeat(" ", max(1, contentWidth-leftWidth-sessionWidth)) + session
}

func (c composer) inputView() string {
	return c.input.View()
}

func (c composer) Value() string {
	return c.input.Value()
}

func (c composer) SingleLine() bool {
	return c.input.LineCount() <= 1
}

func (c composer) CursorLine() int {
	return c.input.Line()
}

func (c composer) CursorColumn() int {
	return c.input.Column()
}

func (c composer) ValueBeforeCursor() string {
	value := c.input.Value()
	lines := strings.Split(value, "\n")
	row := c.input.Line()
	if row < 0 {
		row = 0
	}
	if row >= len(lines) {
		return value
	}

	lineRunes := []rune(lines[row])
	col := min(max(0, c.input.Column()), len(lineRunes))
	before := strings.Join(lines[:row], "\n")
	if before != "" {
		before += "\n"
	}
	return before + string(lineRunes[:col])
}

func (c composer) ValueAfterCursor() string {
	value := c.input.Value()
	lines := strings.Split(value, "\n")
	row := c.input.Line()
	if row < 0 || row >= len(lines) {
		return ""
	}

	lineRunes := []rune(lines[row])
	col := min(max(0, c.input.Column()), len(lineRunes))
	after := string(lineRunes[col:])
	if row+1 < len(lines) {
		after += "\n" + strings.Join(lines[row+1:], "\n")
	}
	return after
}

func (c *composer) ReplaceTokenBeforeCursor(token string, replacement string) bool {
	if token == "" {
		return false
	}
	if !strings.HasSuffix(c.ValueBeforeCursor(), token) {
		return false
	}

	for range []rune(token) {
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		_ = cmd
	}
	c.input.InsertString(replacement)
	return true
}

func (c *composer) SetValue(value string) {
	c.input.SetValue(value)
	c.input.CursorEnd()
}

func (c *composer) Reset() {
	c.input.Reset()
}

func (c *composer) SetWidth(width int) {
	c.width = max(width, 0)
	c.input.SetWidth(max(c.width-composerPaddingWidth, 1))
}

func (c *composer) SetDensity(density densityMode) {
	height := comfortableChatBoxInputHeight
	if density == densityCompact {
		height = compactChatBoxInputHeight
	}
	c.input.SetHeight(height)
}

// SetHeight sets the total composer height, including its status row.
func (c *composer) SetHeight(height int) {
	c.input.SetHeight(max(height-composerStatusHeight, 1))
}

func (c composer) Height() int {
	return c.input.Height() + composerStatusHeight
}

func (c *composer) Submit() (string, bool) {
	text := strings.TrimSpace(c.input.Value())
	if text == "" {
		return "", false
	}

	c.input.Reset()
	return text, true
}
