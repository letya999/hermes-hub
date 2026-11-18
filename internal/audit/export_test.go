package audit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExportPageCursorAndFilter(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	first := NewEvent("tool-call", "alice", "succeeded")
	second := NewEvent("audit-export", "alice", "degraded")
	third := NewEvent("introspection", "alice", "admitted")
	for _, event := range []Event{first, second, third} {
		if err := ledger.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	page, err := ledger.ExportPage(0, MaxPageRecords, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 2 || page.Records[0].Index != 1 || page.Records[1].Index != 3 || page.Next != 3 || page.More {
		t.Fatalf("bad export page: %+v", page)
	}
	if page.Records[0].Event.EventID != first.EventID || page.Records[1].Event.EventID != third.EventID {
		t.Fatalf("export order or filter broken: %+v", page.Records)
	}
	if got := page.Records[0].Event.Kind; got == "audit-export" {
		t.Fatalf("control event leaked: %s", got)
	}

	filtered, err := ledger.ExportPage(0, MaxPageRecords, map[string]bool{"introspection": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Records) != 1 || filtered.Records[0].Event.EventID != third.EventID {
		t.Fatalf("kind filter returned %+v", filtered.Records)
	}
	if _, err := ledger.ExportPage(4, MaxPageRecords, nil); err == nil {
		t.Fatal("future cursor accepted")
	}

	cursorPath := ExportCursorPath(ledgerPath)
	if err := StoreExportCursor(cursorPath, "https://sink.invalid/ingest", nil, 3); err != nil {
		t.Fatal(err)
	}
	after, err := LoadExportCursor(cursorPath, "https://sink.invalid/ingest", nil)
	if err != nil || after != 3 {
		t.Fatalf("cursor load = %d, %v", after, err)
	}
	if _, err := LoadExportCursor(cursorPath, "https://other.invalid/ingest", nil); err == nil {
		t.Fatal("cursor crossed destinations")
	}
	if err := os.WriteFile(cursorPath, []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExportCursor(cursorPath, "https://sink.invalid/ingest", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt cursor accepted: %v", err)
	}
}

func TestExportPageBatchBoundaries(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		event := NewEvent("tool-call", "alice", "succeeded")
		event.At = time.Unix(int64(i), 0).UTC()
		if err := ledger.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	page, err := ledger.ExportPage(0, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 2 || !page.More || page.Next != 2 {
		t.Fatalf("bad bounded page: %+v", page)
	}
	page, err = ledger.ExportPage(page.Next, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.More || page.Next != 3 {
		t.Fatalf("bad final page: %+v", page)
	}
}
