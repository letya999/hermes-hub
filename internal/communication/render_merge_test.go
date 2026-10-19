package communication

import (
	"strings"
	"testing"
)

// Inline spans cover the markdown shapes channels actually carry: task
// checkboxes, autolinks, images without alt text, unsafe link destinations
// (never linkified), inline raw HTML and line breaks.
func TestRenderInlineSpanShapes(t *testing.T) {
	src := "- [ ] open task\n- [x] done task\n\n![](https://img.example/x.png) ![alt](javascript:alert(1))\n\n[click](javascript:evil) <b>raw</b>\n\nfirst line  \nsecond line\n"
	parts, _ := renderTelegram(src)
	got := strings.Join(parts, "\n")
	for _, want := range []string{"☐", "☑", "🖼", "raw", "first line", "second line"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, `href="javascript:`) {
		t.Fatalf("unsafe destination linkified: %q", got)
	}
	slack := strings.Join(renderSlack(src), "\n")
	for _, want := range []string{"☐", "☑", "🖼"} {
		if !strings.Contains(slack, want) {
			t.Errorf("slack missing %q in %q", want, slack)
		}
	}
	if strings.Contains(slack, "<javascript:") {
		t.Fatalf("slack linkified unsafe destination: %q", slack)
	}

	// blockPlainText keeps every block kind readable for plain-text sinks.
	blocks := parseBlocks("# Head\n\npara\n\n- a\n- b\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n---\n\n> quoted\n\n```\ncode\n```\n")
	var lines []string
	for _, b := range blocks {
		lines = append(lines, blockPlainText(b)...)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Head", "para", "a", "b", "A", "———", "quoted", "code"} {
		if !strings.Contains(joined, want) {
			t.Errorf("plain text missing %q in %q", want, joined)
		}
	}
}
