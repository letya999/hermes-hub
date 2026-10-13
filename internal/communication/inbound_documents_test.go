package communication

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDocumentJobPinsTrustedInboundID(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	update := Update{UpdateID: 90, Message: &Message{MessageID: 5, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Document: &TGMedia{FileID: "doc1", FileName: "report.zip", MimeType: "application/zip", FileSize: 12}}}
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.InboundID != "in-90" {
		t.Fatalf("trusted inbound reference missing: job=%+v err=%v", job, err)
	}
}

func TestDocumentDownloadFailureDoesNotStartFilelessRun(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	if err := os.Remove(filepath.Join(g.spool.root, "tmp")); err != nil {
		t.Fatal(err)
	}
	update := Update{UpdateID: 91, Message: &Message{MessageID: 5, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Document: &TGMedia{FileID: "missing", FileName: "report.pdf", MimeType: "application/pdf", FileSize: 12}}}
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job != nil {
		t.Fatalf("fileless job admitted: job=%v err=%v", job, err)
	}
}
