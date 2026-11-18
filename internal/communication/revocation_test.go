package communication

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRevokePrincipalJobsLeavesSiblingScope(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	alice := queuedJob("job-alice", "alice", "alice", "conv-a")
	alice.IdempotencyKey = "key-alice"
	bob := queuedJob("job-bob", "bob", "bob", "conv-b")
	bob.IdempotencyKey = "key-bob"
	if ok, err := s.Enqueue(alice); err != nil || !ok {
		t.Fatalf("alice enqueue: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Enqueue(bob); err != nil || !ok {
		t.Fatalf("bob enqueue: ok=%v err=%v", ok, err)
	}
	if count, err := s.RevokePrincipalJobs("alice", "alice"); err != nil || count != 1 {
		t.Fatalf("revoke count=%d err=%v", count, err)
	}
	mapping, found, err := s.Mapping(alice.ID)
	if err != nil || !found || mapping.Status != "cancelled" || !mapping.CancelRequested || mapping.TerminalAt.IsZero() {
		t.Fatalf("alice mapping=%+v found=%v err=%v", mapping, found, err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "failed", alice.ID+".json")); err != nil {
		t.Fatalf("cancelled job evidence: %v", err)
	}
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil || claimed.ID != bob.ID {
		t.Fatalf("sibling claim=%+v err=%v", claimed, err)
	}
	if count, err := s.RevokePrincipalJobs("alice", "alice"); err != nil || count != 0 {
		t.Fatalf("second revoke count=%d err=%v", count, err)
	}
}

func TestRevokePrincipalJobsKeepsClaimedWorkUncertain(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := queuedJob("job-running", "alice", "alice", "conv-a")
	job.IdempotencyKey = "key-running"
	if ok, err := s.Enqueue(job); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	if claimed, err := s.ClaimJob(); err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if count, err := s.RevokePrincipalJobs("alice", "alice"); err != nil || count != 1 {
		t.Fatalf("revoke count=%d err=%v", count, err)
	}
	mapping, found, err := s.Mapping(job.ID)
	if err != nil || !found || mapping.Status != "uncertain" || !mapping.CancelRequested || mapping.LastKnownEvent != "run.unknown" {
		t.Fatalf("claimed mapping=%+v found=%v err=%v", mapping, found, err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "running", job.ID+".json")); err != nil {
		t.Fatalf("claimed evidence lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "failed", job.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("claimed job falsely terminalized: %v", err)
	}
}

func TestRevokePrincipalDeliveriesSupersedesOnlyTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(dir, id, owner, contextID string) {
		t.Helper()
		d := Delivery{ID: id, OwnerPrincipalID: owner, OwnerContextID: contextID, Channel: "telegram_bot", Text: "reply", CreatedAt: time.Now().UTC()}
		if err := atomicJSON(filepath.Join(root, "outbox", dir, id+".json"), d); err != nil {
			t.Fatal(err)
		}
	}
	write("pending", "del-alice", "alice", "alice")
	write("sending", "del-alice-live", "alice", "alice")
	write("pending", "del-bob", "bob", "bob")
	if count, err := s.RevokePrincipalDeliveries("alice", "alice"); err != nil || count != 2 {
		t.Fatalf("revoke count=%d err=%v", count, err)
	}
	for _, id := range []string{"del-alice", "del-alice-live"} {
		if _, err := os.Stat(filepath.Join(root, "outbox", "superseded", id+".json")); err != nil {
			t.Fatalf("%s evidence: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "pending", "del-bob.json")); err != nil {
		t.Fatalf("sibling delivery lost: %v", err)
	}
	if count, err := s.RevokePrincipalDeliveries("alice", "alice"); err != nil || count != 0 {
		t.Fatalf("second revoke count=%d err=%v", count, err)
	}
}

// A principal-only owner stamp is not a scope: shared contexts make the
// (principal, context) pair mandatory, and a binding through the durable job
// mapping outranks a stamped pair that disagrees with it.
func TestRevokePrincipalDeliveriesScopeMatching(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(d Delivery) {
		t.Helper()
		if err := atomicJSON(filepath.Join(root, "outbox", "pending", d.ID+".json"), d); err != nil {
			t.Fatal(err)
		}
	}
	aliceJob := queuedJob("job-alice-bound", "alice", "alice", "conv-a")
	aliceJob.IdempotencyKey = "key-alice-bound"
	if ok, err := s.Enqueue(aliceJob); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	// Standalone records: exact pair only — a principal match on the wrong
	// context, a context match on the wrong principal, and ownerless legacy
	// records all fail closed.
	write(Delivery{ID: "watch-alice", OccurrenceID: "occ-1", OwnerPrincipalID: "alice", OwnerContextID: "alice", Channel: "telegram_bot", Text: "watch"})
	write(Delivery{ID: "watch-shared", OccurrenceID: "occ-2", OwnerPrincipalID: "alice", OwnerContextID: "shared-ctx", Channel: "telegram_bot", Text: "other context"})
	write(Delivery{ID: "legacy-ownerless", OccurrenceID: "occ-3", Channel: "telegram_bot", Text: "legacy"})
	write(Delivery{ID: "wrong-principal", OwnerPrincipalID: "bob", OwnerContextID: "alice", Channel: "telegram_bot", Text: "cross"})
	// Job-bound records resolve through the durable mapping — a stamped pair
	// naming a different context does not exempt them, and a status-id
	// convention resolves its job without any JobID field. A job reference
	// that resolves nowhere falls back to the stamped pair.
	write(Delivery{ID: "del-job-bound", JobID: aliceJob.ID, OwnerPrincipalID: "alice", OwnerContextID: "shared-ctx", Channel: "telegram_bot", Text: "job notice"})
	write(Delivery{ID: "job-" + aliceJob.ID + "-status", Channel: "telegram_bot", Text: "status"})
	write(Delivery{ID: "appr-bound", ApprovalJobID: aliceJob.ID, Channel: "telegram_bot", Text: "approval"})
	write(Delivery{ID: "ghost-bound", JobID: "job-never-admitted", OwnerPrincipalID: "alice", OwnerContextID: "alice", Channel: "telegram_bot", Text: "orphan"})
	write(Delivery{ID: "ghost-shared", JobID: "job-never-admitted", OwnerPrincipalID: "alice", OwnerContextID: "shared-ctx", Channel: "telegram_bot", Text: "orphan ctx"})
	if count, err := s.RevokePrincipalDeliveries("alice", "alice"); err != nil || count != 5 {
		t.Fatalf("revoke count=%d err=%v", count, err)
	}
	for _, id := range []string{"watch-alice", "del-job-bound", "job-" + aliceJob.ID + "-status", "appr-bound", "ghost-bound"} {
		if _, err := os.Stat(filepath.Join(root, "outbox", "superseded", id+".json")); err != nil {
			t.Fatalf("%s evidence: %v", id, err)
		}
	}
	for _, id := range []string{"watch-shared", "legacy-ownerless", "wrong-principal", "ghost-shared"} {
		if _, err := os.Stat(filepath.Join(root, "outbox", "pending", id+".json")); err != nil {
			t.Fatalf("%s survived wrongly: %v", id, err)
		}
	}
	// The surviving shared-context records die only under their own pair.
	if count, err := s.RevokePrincipalDeliveries("alice", "shared-ctx"); err != nil || count != 2 {
		t.Fatalf("shared-context revoke count=%d err=%v", count, err)
	}
}

func TestPrincipalRevocationHTTPContract(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := queuedJob("job-http", "alice", "alice", "conv-a")
	job.IdempotencyKey = "key-http"
	if ok, err := s.Enqueue(job); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	g := &Gateway{config: Config{AutomationAuth: "service"}, spool: s}
	valid := `{"principal_id":"alice","context_id":"alice","stage":"jobs"}`
	for _, tc := range []struct {
		name, method, token, body, origin string
		want                              int
	}{
		{"valid", http.MethodPost, "service", valid, "", http.StatusOK},
		{"method", http.MethodGet, "service", valid, "", http.StatusMethodNotAllowed},
		{"browser origin", http.MethodPost, "service", valid, "https://example.test", http.StatusForbidden},
		{"wrong bearer", http.MethodPost, "wrong", valid, "", http.StatusUnauthorized},
		{"trailing json", http.MethodPost, "service", valid + ` {"x":1}`, "", http.StatusBadRequest},
		{"unknown stage", http.MethodPost, "service", `{"principal_id":"alice","context_id":"alice","stage":"toolhub"}`, "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/v1/admin/revocations", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			g.servePrincipalRevocation(w, req)
			if w.Code != tc.want {
				t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body)
			}
		})
	}
	if mapping, found, err := s.Mapping(job.ID); err != nil || !found || mapping.Status != "cancelled" {
		t.Fatalf("mapping after HTTP revoke=%+v found=%v err=%v", mapping, found, err)
	}
}
