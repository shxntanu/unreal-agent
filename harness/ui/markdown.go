package ui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type markdownRenderer struct {
	width      int
	text       lipgloss.Style
	heading    lipgloss.Style
	emphasis   lipgloss.Style
	strong     lipgloss.Style
	inlineCode lipgloss.Style
	codeBlock  lipgloss.Style
	codeLabel  lipgloss.Style
	quote      lipgloss.Style
	bullet     lipgloss.Style
	rule       lipgloss.Style
}

func newMarkdownRenderer(width int) markdownRenderer {
	return markdownRenderer{
		width: max(1, width),
		text: lipgloss.NewStyle().
			Background(tuiBackground).
			Foreground(tuiInk),
		heading: lipgloss.NewStyle().
			Background(tuiBackground).
			Bold(true).
			Foreground(tuiAccentModel),
		emphasis: lipgloss.NewStyle().
			Background(tuiBackground).
			Italic(true).
			Foreground(tuiInk),
		strong: lipgloss.NewStyle().
			Background(tuiBackground).
			Bold(true).
			Foreground(tuiInkStrong),
		inlineCode: lipgloss.NewStyle().
			Foreground(tuiInkStrong).
			Background(tuiSelected),
		codeBlock: lipgloss.NewStyle().
			Foreground(tuiInk).
			Background(tuiSurface2).
			BorderStyle(lipgloss.NormalBorder()).
			BorderLeft(true).
			BorderForeground(tuiAccentInfo).
			Padding(0, 1),
		codeLabel: lipgloss.NewStyle().
			Background(tuiSurface2).
			Foreground(tuiAccentAgent).
			Bold(true),
		quote: lipgloss.NewStyle().
			Background(tuiBackground).
			Foreground(tuiMuted).
			BorderStyle(lipgloss.NormalBorder()).
			BorderLeft(true).
			BorderForeground(tuiSubtle).
			PaddingLeft(1),
		bullet: lipgloss.NewStyle().
			Background(tuiBackground).
			Foreground(tuiAccentAgent).
			Bold(true),
		rule: lipgloss.NewStyle().
			Background(tuiBackground).
			Foreground(tuiLine),
	}
}

func (r markdownRenderer) Render(markdown string) string {
	lines := strings.Split(strings.TrimSpace(markdown), "\n")
	rendered := make([]string, 0, len(lines))
	paragraph := make([]string, 0)
	inCode := false
	codeFence := ""
	codeLanguage := ""
	codeLines := make([]string, 0)

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		text := strings.Join(paragraph, " ")
		rendered = append(rendered, r.wrapStyled(r.renderInline(text), r.width))
		paragraph = paragraph[:0]
	}
	flushCode := func() {
		rendered = append(rendered, r.renderCodeBlock(codeLanguage, codeLines))
		codeLines = codeLines[:0]
		codeLanguage = ""
		codeFence = ""
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if fence, language, ok := parseCodeFence(line); ok {
			if inCode && strings.HasPrefix(strings.TrimSpace(line), codeFence) {
				flushCode()
				inCode = false
				continue
			}
			if !inCode {
				flushParagraph()
				inCode = true
				codeFence = fence
				codeLanguage = language
				continue
			}
		}
		if inCode {
			codeLines = append(codeLines, line)
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushParagraph()
			continue
		}
		if isMarkdownRule(trimmed) {
			flushParagraph()
			rendered = append(rendered, r.rule.Render(strings.Repeat("─", max(1, min(r.width, 80)))))
			continue
		}
		if level, heading, ok := parseHeading(trimmed); ok {
			flushParagraph()
			rendered = append(rendered, r.renderHeading(level, heading))
			continue
		}
		if quote, ok := parseQuote(trimmed); ok {
			flushParagraph()
			rendered = append(rendered, r.renderQuote(quote))
			continue
		}
		if prefix, body, ok := parseListItem(line); ok {
			flushParagraph()
			rendered = append(rendered, r.renderListItem(prefix, body))
			continue
		}

		paragraph = append(paragraph, trimmed)
	}

	if inCode {
		flushCode()
	}
	flushParagraph()

	return strings.Join(rendered, "\n\n")
}

func (r markdownRenderer) renderHeading(level int, text string) string {
	prefix := strings.Repeat("#", level) + " "
	return r.wrapStyled(r.heading.Render(prefix+strings.TrimSpace(stripInlineMarkers(text))), r.width)
}

func (r markdownRenderer) renderQuote(text string) string {
	body := r.wrapStyled(r.renderInline(text), max(1, r.width-r.quote.GetHorizontalFrameSize()))
	if r.width <= r.quote.GetHorizontalFrameSize() {
		return r.wrapStyled(r.renderInline(text), r.width)
	}
	return r.quote.Width(r.width).Render(body)
}

func (r markdownRenderer) renderListItem(prefix string, body string) string {
	prefix = r.bullet.Render(prefix)
	if r.width <= lipgloss.Width(prefix)+1 {
		return r.wrapStyled(r.renderInline(strings.TrimSpace(body)), r.width)
	}
	body = r.wrapStyled(r.renderInline(strings.TrimSpace(body)), max(1, r.width-lipgloss.Width(prefix)-1))
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.Repeat(" ", lipgloss.Width(prefix)+1) + lines[i]
	}
	return prefix + " " + strings.Join(lines, "\n")
}

func (r markdownRenderer) renderCodeBlock(language string, lines []string) string {
	innerWidth := max(1, r.width-r.codeBlock.GetHorizontalFrameSize())
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			wrapped = append(wrapped, " ")
			continue
		}
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(strings.ReplaceAll(line, "\t", "    "), innerWidth, true), "\n")...)
	}
	body := strings.Join(wrapped, "\n")
	if strings.TrimSpace(body) == "" {
		body = " "
	}
	if strings.TrimSpace(language) != "" {
		body = r.wrapStyled(r.codeLabel.Render(language), innerWidth) + "\n" + body
	}
	if r.width <= r.codeBlock.GetHorizontalFrameSize()+2 {
		return ansi.Hardwrap(body, r.width, true)
	}
	return r.codeBlock.Width(r.width).Render(body)
}

func (r markdownRenderer) renderInline(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "`"):
			if end := strings.Index(text[i+1:], "`"); end >= 0 {
				content := text[i+1 : i+1+end]
				b.WriteString(r.inlineCode.Render(content))
				i += end + 2
				continue
			}
		case strings.HasPrefix(text[i:], "**"):
			if end := strings.Index(text[i+2:], "**"); end >= 0 {
				content := text[i+2 : i+2+end]
				b.WriteString(r.strong.Render(content))
				i += end + 4
				continue
			}
		case strings.HasPrefix(text[i:], "__"):
			if end := strings.Index(text[i+2:], "__"); end >= 0 {
				content := text[i+2 : i+2+end]
				b.WriteString(r.strong.Render(content))
				i += end + 4
				continue
			}
		case text[i] == '*':
			if end := strings.Index(text[i+1:], "*"); end >= 0 {
				content := text[i+1 : i+1+end]
				b.WriteString(r.emphasis.Render(content))
				i += end + 2
				continue
			}
		case text[i] == '_':
			if end := strings.Index(text[i+1:], "_"); end >= 0 {
				content := text[i+1 : i+1+end]
				b.WriteString(r.emphasis.Render(content))
				i += end + 2
				continue
			}
		}

		b.WriteByte(text[i])
		i++
	}
	return r.text.Render(b.String())
}

func (r markdownRenderer) wrapStyled(text string, width int) string {
	return ansi.Wrap(text, max(1, width), " ")
}

func parseCodeFence(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, fence := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, fence) {
			return fence, strings.TrimSpace(strings.TrimPrefix(trimmed, fence)), true
		}
	}
	return "", "", false
}

func parseHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level:]), true
}

func parseQuote(line string) (string, bool) {
	if !strings.HasPrefix(line, ">") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, ">")), true
}

func parseListItem(line string) (string, string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) >= 2 {
		marker := trimmed[0]
		if (marker == '-' || marker == '*' || marker == '+') && trimmed[1] == ' ' {
			return "•", trimmed[2:], true
		}
	}

	dot := strings.Index(trimmed, ". ")
	if dot <= 0 {
		return "", "", false
	}
	number, err := strconv.Atoi(trimmed[:dot])
	if err != nil || number <= 0 {
		return "", "", false
	}
	return strconv.Itoa(number) + ".", trimmed[dot+2:], true
}

func isMarkdownRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	first := line[0]
	if first != '-' && first != '*' && first != '_' {
		return false
	}
	for _, ch := range line {
		if byte(ch) != first && ch != ' ' {
			return false
		}
	}
	return true
}

func stripInlineMarkers(text string) string {
	replacer := strings.NewReplacer("`", "", "**", "", "__", "", "*", "", "_", "")
	return replacer.Replace(text)
}
