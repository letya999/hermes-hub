package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

// admit is the atomic gate before a provider attempt: concurrent admits
// cannot both pass a one-request window, the charge lands before dispatch so
// a lost response still spends capacity, and a restart keeps the counter.
func TestRelayMeterAdmit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meter.json")
	meter := &relayMeter{path: path}
	limits := []stack.ModelLimit{{Of: "requests", Per: "1d", Amount: 1}}
	body := []byte(`{"model":"m","messages":[]}`)

	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes <- meter.admit("svc", limits, "alice", body)
		}()
	}
	wg.Wait()
	close(outcomes)
	admitted, refused := 0, 0
	for err := range outcomes {
		if err == nil {
			admitted++
		} else if errors.Is(err, errQuota) {
			refused++
		} else {
			t.Fatalf("unexpected admit error: %v", err)
		}
	}
	if admitted != 1 || refused != 1 {
		t.Fatalf("requests=1 admitted=%d refused=%d, want 1/1", admitted, refused)
	}
	// The admitted attempt already spent the window even if its response is
	// later lost: a fresh attempt refuses without any settlement step.
	if err := meter.admit("svc", limits, "alice", body); !errors.Is(err, errQuota) {
		t.Fatalf("post-attempt admit=%v, want quota refusal", err)
	}
	// Restart: the durable counter survives; a fresh meter still refuses.
	restarted := &relayMeter{path: path}
	if err := restarted.admit("svc", limits, "alice", body); !errors.Is(err, errQuota) {
		t.Fatalf("restarted admit=%v, want quota refusal", err)
	}
	if !restarted.exhausted("svc", limits, "alice") {
		t.Fatal("restarted meter lost the spent window")
	}
}

// Every declared window gates independently: token limits reserve the
// conservative estimate per attempt, so two attempts can exhaust a window a
// single request check would have missed.
func TestRelayMeterAdmitChargesPerAttempt(t *testing.T) {
	meter := &relayMeter{path: filepath.Join(t.TempDir(), "meter.json")}
	limits := []stack.ModelLimit{{Of: "tokens", Per: "1d", Amount: 8300}}
	body := []byte(`{"model":"m","messages":[],"max_tokens":9000}`)
	if err := meter.admit("svc", limits, "alice", body); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	// The first attempt reserved ~9000 against the 8300 window (single-attempt
	// overshoot is documented); the next attempt sees a spent window and
	// refuses — fallback hops get their own gate, not a shared reservation.
	if err := meter.admit("svc", limits, "alice", body); !errors.Is(err, errQuota) {
		t.Fatalf("second admit=%v, want quota refusal", err)
	}
}

// Durable failures fail closed: corrupt state refuses admission, and a save
// that cannot persist refuses the attempt while keeping the in-memory charge
// so a retry stays conservative rather than free.
func TestRelayMeterAdmitFailsClosed(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := &relayMeter{path: bad}
	if err := corrupt.admit("svc", []stack.ModelLimit{{Of: "requests", Per: "1d", Amount: 5}}, "alice", nil); !errors.Is(err, errQuota) {
		t.Fatalf("corrupt admit=%v, want quota refusal", err)
	}
	if !corrupt.exhausted("svc", []stack.ModelLimit{{Of: "requests", Per: "1d", Amount: 5}}, "alice") {
		t.Fatal("corrupt state should read as exhausted")
	}
	// Unwritable path: the reservation cannot persist, so nothing dispatches;
	// the in-memory charge keeps the next attempt conservative.
	unwritable := &relayMeter{path: filepath.Join(dir, "missing", "meter.json")}
	limits := []stack.ModelLimit{{Of: "requests", Per: "1d", Amount: 2}}
	if err := unwritable.admit("svc", limits, "alice", nil); !errors.Is(err, errQuota) {
		t.Fatalf("unsaved admit=%v, want quota refusal", err)
	}
	if err := unwritable.admit("svc", limits, "alice", nil); !errors.Is(err, errQuota) {
		t.Fatalf("post-failure admit=%v, want quota refusal", err)
	}
	if !unwritable.exhausted("svc", limits, "alice") {
		t.Fatal("in-memory charge lost after save failure")
	}
}
