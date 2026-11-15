package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditCorruptTailReportedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(NewEvent("job", "alice", "allow")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"schema":1`); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(NewEvent("job", "alice", "allow")); err == nil {
		t.Error("append acknowledged after partial tail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("corrupt evidence mutated")
	}
}

func TestAuditRecordHasFiniteLimit(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	event := NewEvent("job", "alice", "allow")
	event.JobID = strings.Repeat("j", 32*1024)
	if err := ledger.Append(event); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded record: %v", err)
	}
}

func TestAuditUnknownVersionIsNotEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":999,"principal_id":"bob"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.List("alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown foreign schema hidden by scope: %v", err)
	}
}

func TestAuditBoundedReadAndScope(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		event := NewEvent("job", "alice", "allow")
		event.JobID = fmt.Sprint(i)
		event.ContextID = "selected"
		event.Environment = "dev"
		if i%2 == 1 {
			event.ContextID = "foreign"
		}
		if err := ledger.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	scope := Scope{PrincipalID: "alice", ContextID: "selected", Environment: "dev"}
	first, err := ledger.ReadPage(scope, 0, 2)
	if err != nil || !first.More || len(first.Events) != 2 || first.Events[0].JobID != "0" || first.Events[1].JobID != "2" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := ledger.ReadPage(scope, first.Next, 2)
	if err != nil || second.More || len(second.Events) != 2 || second.Events[0].JobID != "4" || second.Events[1].JobID != "6" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	empty, err := ledger.ReadPage(Scope{PrincipalID: "bob"}, 0, 1)
	if err != nil || len(empty.Events) != 0 {
		t.Fatalf("foreign scope leaked: %+v %v", empty, err)
	}
	for _, limit := range []int{0, MaxPageRecords + 1} {
		if _, err := ledger.ReadPage(scope, 0, limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if _, err := ledger.ReadPage(scope, 100, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future cursor: %v", err)
	}
}

func TestAuditCapacityStopsWithoutDeletingEvidence(t *testing.T) {
	for _, cap := range []string{"bytes", "records", "segments"} {
		t.Run(cap, func(t *testing.T) {
			ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			ledger.bounds = ledgerBounds{1024, 16 * 1024, 16, 100}
			switch cap {
			case "bytes":
				ledger.bounds.total = 600
			case "records":
				ledger.bounds.records = 2
			case "segments":
				ledger.bounds.segment = 250
				ledger.bounds.segments = 2
			}
			var accepted []Event
			for i := 0; i < 100; i++ {
				event := NewEvent("job", "alice", "allow")
				err := ledger.Append(event)
				if errors.Is(err, ErrFull) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				accepted = append(accepted, event)
			}
			if len(accepted) == 0 || len(accepted) == 100 {
				t.Fatalf("capacity not enforced: %d", len(accepted))
			}
			events, err := ledger.List("alice")
			if err != nil || len(events) != len(accepted) {
				t.Fatalf("history lost: %d/%d %v", len(events), len(accepted), err)
			}
			for i := range events {
				if events[i].EventID != accepted[i].EventID {
					t.Fatal("history changed")
				}
			}
			reopened, err := Open(ledger.path)
			if err != nil {
				t.Fatal(err)
			}
			reopened.bounds = ledger.bounds
			if err := reopened.Append(NewEvent("job", "alice", "allow")); !errors.Is(err, ErrFull) {
				t.Fatalf("reopen bypassed cap: %v", err)
			}
		})
	}
}

func TestConcurrentAppendRotationAcrossWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuditProcessWorker$") // #nosec G204 -- current test executable and fixed worker.
			cmd.Env = append(os.Environ(), "HUB_AUDIT_TEST_PATH="+path)
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("worker: %w: %s", err, output)
			}
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	ledger.bounds = ledgerBounds{1024, 16 * 1024, 16, 100}
	events, err := ledger.List("alice")
	if err != nil || len(events) != 32 {
		t.Fatalf("lost records: %d %v", len(events), err)
	}
	ids := map[string]bool{}
	for _, event := range events {
		if ids[event.EventID] {
			t.Fatal("duplicate event")
		}
		ids[event.EventID] = true
	}
	if _, err := os.Stat(path + ".v1-000001"); err != nil {
		t.Fatalf("no rotation: %v", err)
	}
}

func TestAuditProcessWorker(t *testing.T) {
	path := os.Getenv("HUB_AUDIT_TEST_PATH")
	if path == "" {
		t.Skip("subprocess worker")
	}
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ledger.bounds = ledgerBounds{1024, 16 * 1024, 16, 100}
	for i := 0; i < 16; i++ {
		if err := ledger.Append(NewEvent("job", "alice", "allow")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditSegmentGapAndLostInitialRefuseMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".v1-000002", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(NewEvent("job", "alice", "allow")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("gap accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lost initial recreated: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing initial file mutated")
	}
}

func TestAuditListRefusesSilentPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture bytes.Buffer
	for i := 0; i <= MaxPageRecords; i++ {
		event := NewEvent("job", "alice", "allow")
		event.JobID = fmt.Sprint(i)
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Write(data)
		fixture.WriteByte('\n')
	}
	if err := os.WriteFile(path, fixture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.List("alice"); !errors.Is(err, ErrPage) {
		t.Fatalf("silent truncation: %v", err)
	}
	first, err := ledger.ReadPage(Scope{PrincipalID: "alice"}, 0, MaxPageRecords)
	if err != nil || !first.More || len(first.Events) != MaxPageRecords {
		t.Fatalf("first page: %d %v", len(first.Events), err)
	}
	final, err := ledger.ReadPage(Scope{PrincipalID: "alice"}, first.Next, MaxPageRecords)
	if err != nil || final.More || len(final.Events) != 1 || final.Events[0].JobID != "1000" {
		t.Fatalf("final page: %+v %v", final, err)
	}
}

func TestAuditRejectsFileAndLockAliases(t *testing.T) {
	for _, suffix := range []string{"", ".lock"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "private.json")
			original := []byte("private fixture")
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "audit.jsonl")
			if err := os.Symlink(target, path+suffix); err != nil {
				t.Skip("symlink unavailable")
			}
			if _, err := Open(path); !errors.Is(err, ErrInvalid) {
				t.Fatalf("alias accepted: %v", err)
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("alias target changed")
			}
		})
	}
}
