package communication

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type photoInputAPI struct {
	*fakeAPI
	body []byte
}

func (a photoInputAPI) DownloadFile(_ context.Context, _ string, w io.Writer) error {
	_, err := w.Write(a.body)
	return err
}

func TestTelegramPhotoStagesTypedSafeNameAndExactBytes(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 3, 4))); err != nil {
		t.Fatal(err)
	}
	g.api = photoInputAPI{fakeAPI: &fakeAPI{}, body: imageBytes.Bytes()}
	message := &Message{MessageID: 6, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Photo: []TGMedia{{FileID: "not-a-safe-name/opaque", FileSize: int64(imageBytes.Len())}}}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 92, Message: message}); err != nil {
		t.Fatal(err)
	}
	record, body, err := g.spool.ReadInbound("in-92")
	if err != nil || record.Name != "in-92.png" || !bytes.Equal(body, imageBytes.Bytes()) {
		t.Fatalf("photo staging: name=%q bytes=%d err=%v", record.Name, len(body), err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.InboundID != "in-92" {
		t.Fatalf("photo reference: %v %v", job, err)
	}
}

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
