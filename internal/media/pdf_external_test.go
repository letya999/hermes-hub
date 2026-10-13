package media

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestExtractCompressedPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("Poppler is provided by the runtime image")
	}
	body, err := os.ReadFile("testdata/compressed.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got, err := documentText("pdf", body)
	if err != nil || !strings.Contains(got, "Synthetic document extraction regression") {
		t.Fatalf("compressed PDF extraction failed: chars=%d err=%v", len(got), err)
	}
}

func TestPopplerInputAndOutputBounds(t *testing.T) {
	if _, err := popplerText(bytes.Repeat([]byte("x"), MaxDocumentBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized PDF accepted: %v", err)
	}
	var out cappedPDFOutput
	out.max = 3
	if _, err := out.Write([]byte("abcd")); err == nil {
		t.Fatal("unbounded extracted output")
	}
}

func TestPopplerRejectsExpandedTextBeyondLimit(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("Poppler is provided by the runtime image")
	}
	body, err := os.ReadFile("testdata/oversized-text.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if text, err := popplerText(body); err == nil {
		t.Fatalf("compressed PDF expanded beyond text limit: %d bytes", len(text))
	}
}

func TestPopplerRejectsTooManyPages(t *testing.T) {
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		t.Skip("Poppler is provided by the runtime image")
	}
	body, err := os.ReadFile("testdata/over-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := popplerText(body); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("oversized page count accepted: %v", err)
	}
}
