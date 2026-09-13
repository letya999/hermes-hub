package audit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendListOwnerScopeAndRedaction(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "live-secret-value-not-for-ledger"
	alice := NewEvent("tool-call", "alice", "allow")
	alice.RuntimeID = "runtime"
	alice.ConnectionID = "google-work"
	alice.PolicyRevision = "policy-1"
	alice.CredentialRevision = 3
	alice.JobID = "job-1"
	alice.HermesRunID = "run-1"
	alice.ToolCallID = "call-1"
	alice.CorrelationID = "corr-1"
	if err := ledger.Append(alice); err != nil {
		t.Fatal(err)
	}
	bob := NewEvent("credential-change", "bob", "set")
	bob.Name = "GOOGLE_TOKEN"
	bob.ConnectionID = "google-work"
	if err := ledger.Append(bob); err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("alice")
	if err != nil || len(events) != 1 || events[0].JobID != "job-1" || events[0].HermesRunID != "run-1" || events[0].ToolCallID != "call-1" || events[0].CredentialRevision != 3 {
		t.Fatalf("alice events=%+v err=%v", events, err)
	}
	if events[0].PrincipalID != "alice" {
		t.Fatal("owner scope leaked")
	}
	body, err := os.ReadFile(ledger.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) || strings.Contains(string(body), `"prompt"`) || strings.Contains(string(body), `"result"`) {
		t.Fatal("audit ledger contained forbidden material")
	}
	bad := alice
	bad.Kind = "tool-call"
	payload := Event{Schema: Schema, EventID: "evt-x", At: time.Now().UTC(), Kind: "tool-call", PrincipalID: "alice", Outcome: "allow"}
	if err := ledger.Append(payload); err != nil {
		t.Fatal(err)
	}
	_ = bad
}

func TestAppendRejectsIncompleteAndSensitiveShapes(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(Event{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty event: %v", err)
	}
	event := NewEvent("unknown", "alice", "allow")
	if err := ledger.Append(event); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	event = NewEvent("tool-call", "alice", "allow\nsecret")
	if err := ledger.Append(event); !errors.Is(err, ErrInvalid) {
		t.Fatalf("newline outcome: %v", err)
	}
	if _, err := Open("relative.jsonl"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path: %v", err)
	}
	if err := (*Ledger)(nil).Append(NewEvent("job", "alice", "allow")); !errors.Is(err, ErrWrite) {
		t.Fatalf("nil ledger: %v", err)
	}
}

func TestContainsSensitiveAndOwnerFilter(t *testing.T) {
	if !containsSensitive([]byte(`{"prompt":"hi"}`)) || containsSensitive([]byte(`{"kind":"job"}`)) {
		t.Fatal("sensitive detection")
	}
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	alice := NewEvent("job", "alice", "allow")
	alice.JobID = "job-1"
	alice.HermesRunID = "run-1"
	alice.ToolCallID = "call-1"
	if err := ledger.Append(alice); err != nil {
		t.Fatal(err)
	}
	listed, err := ledger.List("carol")
	if err != nil || len(listed) != 0 {
		t.Fatalf("other principal listed=%+v err=%v", listed, err)
	}
}

func TestFailClosedWhenLedgerFileRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(NewEvent("job", "alice", "allow")); !errors.Is(err, ErrWrite) {
		t.Fatalf("write to directory: %v", err)
	}
}

func TestOpenExistingCorruptListAndSensitiveNeedles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(NewEvent("oauth", "alice", "allow")); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := again.List("alice")
	if err != nil || len(events) != 1 || events[0].Kind != "oauth" {
		t.Fatalf("reopen list=%+v err=%v", events, err)
	}
	if _, err := again.List(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty principal: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := again.List("alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt list: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := again.Append(NewEvent("job", "alice", "allow")); !errors.Is(err, ErrWrite) {
		t.Fatalf("missing file write: %v", err)
	}
	if !containsSensitive([]byte(`{"ciphertext":"x"}`)) || !containsSensitive([]byte(`{"refresh_token":"x"}`)) || !containsSensitive([]byte(`{"access_token":"x"}`)) || !containsSensitive([]byte(`{"body":"x"}`)) || !containsSensitive([]byte(`{"text":"x"}`)) {
		t.Fatal("sensitive needles missed")
	}
	if _, err := Open(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty path: %v", err)
	}
}
