package media

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Regression: generated PDFs rendered every line on one baseline because the
// content stream used T* without ever setting text leading (TL), which
// defaults to 0. Multi-line documents must carry an explicit leading.
func TestPDFGeneratedSetsLeading(t *testing.T) {
	doc, err := pdfBytes("line one\nline two\nline three")
	if err != nil {
		t.Fatal(err)
	}
	streams, err := pdfStreams(doc)
	if err != nil {
		t.Fatal(err)
	}
	var content []byte
	for _, s := range streams {
		if bytes.Contains(s, []byte(" Tj")) {
			content = append(content, s...)
		}
	}
	if len(content) == 0 {
		t.Fatal("no text content stream found")
	}
	if !bytes.Contains(content, []byte(" TL")) {
		t.Fatalf("content stream sets no leading; T* draws every line at the same baseline:\n%s", content)
	}
	if strings.Count(string(content), " Tj") < 3 {
		t.Fatalf("expected three text ops, got:\n%s", content)
	}
}

func TestPDFGeneratedUsesGlyphAdvances(t *testing.T) {
	doc, err := pdfBytes("Germi Живая")
	if err != nil {
		t.Fatal(err)
	}
	_, cmap, err := pdfFont()
	if err != nil {
		t.Fatal(err)
	}
	width := func(r rune) int {
		t.Helper()
		pattern := regexp.MustCompile(fmt.Sprintf(`\b%d \[(\d+)\]`, cmap[r]))
		match := pattern.FindSubmatch(doc)
		if len(match) != 2 {
			t.Fatalf("missing width for %q", r)
		}
		n, err := strconv.Atoi(string(match[1]))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if narrow, wide := width('i'), width('m'); narrow >= 500 || wide <= 500 {
		t.Fatalf("font advances ignored: i=%d m=%d", narrow, wide)
	}
	if got, err := pdfText(doc); err != nil || !strings.Contains(got, "Germi Живая") {
		t.Fatalf("generated text lost: %q %v", got, err)
	}
}
