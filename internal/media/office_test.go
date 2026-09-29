package media

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMarkdownPDFSpreadsheetAndSlidesRoundTrip(t *testing.T) {
	s, _ := openSession(t)
	ctx := context.Background()

	note, err := s.Create(ctx, "note", "md", "# Title\n\nHello")
	if err != nil || note["path"] != "artifacts/documents/note.md" {
		t.Fatal(note, err)
	}
	got, err := s.Extract(ctx, note["path"].(string))
	if err != nil || got["text"] != "# Title\n\nHello" || got["format"] != "md" {
		t.Fatal(got, err)
	}

	brief, err := s.Create(ctx, "brief", "pdf", "Привет\nHello")
	if err != nil || brief["path"] != "artifacts/documents/brief.pdf" {
		t.Fatal(brief, err)
	}
	got, err = s.Extract(ctx, brief["path"].(string))
	if err != nil || !strings.Contains(got["text"].(string), "Привет") || !strings.Contains(got["text"].(string), "Hello") {
		t.Fatal(got, err)
	}
	edited, err := s.EditDocument(ctx, "artifacts/documents/brief.pdf", "Updated")
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Extract(ctx, edited["path"].(string))
	if err != nil || got["text"] != "Updated" {
		t.Fatal(got, err)
	}
	asText, err := s.ConvertDocument(ctx, "artifacts/documents/brief.pdf", "brief", "txt")
	if err != nil || asText["path"] != "artifacts/documents/brief.txt" {
		t.Fatal(asText, err)
	}

	book, err := s.Create(ctx, "book", "xlsx", "a,b\n1,2")
	if err != nil || book["path"] != "artifacts/documents/book.xlsx" {
		t.Fatal(book, err)
	}
	got, err = s.Extract(ctx, book["path"].(string))
	if err != nil || !strings.Contains(got["text"].(string), "a,b") || !strings.Contains(got["text"].(string), "1,2") {
		t.Fatal(got, err)
	}
	asCSV, err := s.ConvertDocument(ctx, "artifacts/documents/book.xlsx", "book", "csv")
	if err != nil || asCSV["path"] != "artifacts/documents/book.csv" {
		t.Fatal(asCSV, err)
	}
	if _, err := s.Create(ctx, "bad", "xslx", "a,b"); err == nil {
		t.Fatal("xslx accepted")
	}

	deck, err := s.Create(ctx, "deck", "pptx", "Slide one\n\nSlide two")
	if err != nil || deck["path"] != "artifacts/documents/deck.pptx" {
		t.Fatal(deck, err)
	}
	got, err = s.Extract(ctx, deck["path"].(string))
	if err != nil || got["text"] != "Slide one\n\nSlide two" {
		t.Fatal(got, err)
	}
	asMD, err := s.ConvertDocument(ctx, "artifacts/documents/deck.pptx", "deck", "md")
	if err != nil || asMD["format"] != "md" {
		t.Fatal(asMD, err)
	}
}

func TestSimpleAndEncryptedPDF(t *testing.T) {
	plain := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R >>\nendobj\n4 0 obj\n<< /Length 5 0 R >>\nstream\nBT /F1 12 Tf 10 100 Td (Hello) Tj ET\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
	text, err := pdfText(plain)
	if err != nil || !strings.Contains(text, "Hello") {
		t.Fatal(text, err)
	}
	locked := []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R /Encrypt 2 0 R >>\n%%EOF\n")
	if _, err := pdfText(locked); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatal(err)
	}
}

func TestOfficeZipRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	entry, err := w.Create("../evil.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte("<x/>")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = xlsxText(buf.Bytes()); err == nil {
		t.Fatal("zip slip accepted")
	}
	if _, err = pptxText(buf.Bytes()); err == nil {
		t.Fatal("zip slip accepted")
	}
}
