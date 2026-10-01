package list

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestHighlightContentWrappedLines(t *testing.T) {
	t.Parallel()
	content, width := "This is a long line that should wrap around", 20
	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("wrapped content produced %d logical lines: %q", len(lines), result)
	}
}

func TestHighlightContentRealNewlinesPreserved(t *testing.T) {
	t.Parallel()
	content, width := "first\nsecond", 40
	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "first") || !strings.Contains(lines[1], "second") {
		t.Fatalf("real newlines were not preserved: %q", result)
	}
}

func TestHighlightContentParagraphBreak(t *testing.T) {
	t.Parallel()
	content := "first paragraph\n\nsecond paragraph"
	result := HighlightContent(content, uv.Rect(0, 0, 40, lipgloss.Height(content)), 0, 0, -1, -1)
	if lines := strings.Split(strings.TrimRight(result, "\n"), "\n"); len(lines) < 3 {
		t.Fatalf("paragraph break was lost: %q", result)
	}
}

func TestHighlightContentHardWrap(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("a", 79) + "b"
	result := HighlightContent(content, uv.Rect(0, 0, 80, lipgloss.Height(content)), 0, 0, -1, -1)
	if want := strings.Repeat("a", 79) + "b\n"; result != want {
		t.Fatalf("hard wrap = %q, want %q", result, want)
	}
}

func TestHighlightContentMarkdownList(t *testing.T) {
	t.Parallel()
	width := 120
	first := "• If the current row's content extends past sixty percent of the buffer width emit a space (space, wrap"
	continuation := "continuation)"
	next := "• Otherwise emit a newline (real newline, short lines like headings, list items, code)"
	content := "\x1b[36m" + first + "\x1b[0m\n\x1b[3m" + continuation + "\x1b[0m\n\x1b[1m" + next + "\x1b[0m"
	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)
	if !strings.Contains(result, "(space, wrap continuation)\n") {
		t.Fatalf("wrapped continuation did not join with a space: %q", result)
	}
	if !strings.Contains(result, "continuation)\n• Otherwise") {
		t.Fatalf("next list item did not start on its own line: %q", result)
	}
}

func TestHighlightContentRestoresCodespanBackticks(t *testing.T) {
	t.Parallel()
	code := "\x1b[48;5;24m" + codespanPadding + "code" + codespanPadding + "\x1b[0m"
	rendered := "\x1b[36mmessage \"this is " + "\x1b[0m" + code + "\" ok"
	if strings.Contains(ansi.Strip(rendered), "`") {
		t.Fatal("display fixture unexpectedly contains backticks")
	}
	result := HighlightContent(rendered, uv.Rect(0, 0, 80, lipgloss.Height(rendered)), 0, 0, -1, -1)
	if !strings.Contains(result, `"this is `+"`code`"+`" ok`) {
		t.Fatalf("copy did not restore codespan backticks: %q", result)
	}
}

func TestHighlightContentPreservesRealNonBreakingSpaces(t *testing.T) {
	t.Parallel()
	rendered := "question\u00a0: is " + codespanPadding + "code" + codespanPadding + " ok"
	result := HighlightContent(rendered, uv.Rect(0, 0, 80, lipgloss.Height(rendered)), 0, 0, -1, -1)
	if !strings.Contains(result, "question\u00a0: is `code` ok") {
		t.Fatalf("copy lost the real no-break space or codespan backticks: %q", result)
	}
	if result = HighlightContent("foo\u00a0bar", uv.Rect(0, 0, 40, 1), 0, 0, -1, -1); result != "foo\u00a0bar\n" {
		t.Fatalf("plain no-break space = %q", result)
	}
}

func TestHighlightContentRestoresWrappedCodespanBackticks(t *testing.T) {
	t.Parallel()
	width := 16
	rendered := "some text\n" + codespanPadding + "codespan" + codespanPadding + "\nand more words to force wrapping across rows"
	result := HighlightContent(rendered, uv.Rect(0, 0, width, lipgloss.Height(rendered)), 0, 0, -1, -1)
	if !strings.Contains(result, "`codespan`") {
		t.Fatalf("wrapped codespan did not reassemble in copy: %q", result)
	}
}
