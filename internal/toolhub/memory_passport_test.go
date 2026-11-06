package toolhub

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func writeMemoryPassport(t *testing.T, root, user string, enabled bool) {
	t.Helper()
	revision := "r-0123456789abcdef"
	if enabled {
		revision = "r-0123456789abcdee"
	}
	writeMemoryPassportDecl(t, root, user, user, revision, "dev", enabled)
}

func TestMemoryCallChecksPublishedSwitchBeforeBackend(t *testing.T) {
	store, auth := memoryToolFixture(t)
	profile := store.capabilityProfiles[auth.CapabilityProfile]
	root := t.TempDir()
	writeMemoryPassport(t, root, "alice", false)
	profile.Revision++
	profile.PassportRevision = "r-0123456789abcdef"
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	calls := 0
	g := &Gateway{Store: store, PassportRoot: root, AuditWrite: func(string, map[string]string) error { return nil },
		Runs: runAuthorityFunc(func(_ context.Context, jobID string) (api.RunContext, error) {
			if jobID != "job-private" {
				return api.RunContext{}, context.DeadlineExceeded
			}
			return privateRunRecord(auth, "job-private", "run-private-1"), nil
		}),
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			calls++
			return api.BackendResult{Text: "found"}, nil
		})}
	if _, err := g.CallAuthorized(t.Context(), auth, "memory_search", nil); err == nil || calls != 0 {
		t.Fatalf("disabled memory reached backend: calls=%d err=%v", calls, err)
	}
	writeMemoryPassport(t, root, "alice", true)
	profile.Revision++
	profile.PassportRevision = "r-0123456789abcdee"
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	// Enabled and granted is still not enough: only the exact-run claim the
	// authority verifies as a live private run of this caller admits.
	if _, err := g.CallAuthorized(t.Context(), auth, "memory_search", nil); err == nil || calls != 0 {
		t.Fatalf("enabled memory without a run claim reached backend: calls=%d err=%v", calls, err)
	}
	liveCtx := api.WithCallCorrelation(t.Context(), api.HeaderCorrelation("job-private", "run-private-1"))
	if _, err := g.CallAuthorized(liveCtx, auth, "memory_search", nil); err != nil || calls != 1 {
		t.Fatalf("enabled memory with grant and a live run denied: calls=%d err=%v", calls, err)
	}
	otherEnv := auth
	otherEnv.Environment = "prod"
	if _, err := g.CallAuthorized(liveCtx, otherEnv, "memory_search", nil); err == nil || calls != 1 {
		t.Fatalf("memory call from a different environment reached backend: calls=%d err=%v", calls, err)
	}
}

func TestMemoryPassportGateUsesAuthenticatedPrincipal(t *testing.T) {
	root := t.TempDir()
	writeMemoryPassport(t, root, "alice", true)
	writeMemoryPassport(t, root, "bob", false)
	if err := memoryEnrollment(root, "alice", "dev"); err != nil {
		t.Fatalf("alice's enabled memory denied: %v", err)
	}
	if err := memoryEnrollment(root, "alice", "prod"); err == nil {
		t.Fatal("prod call inherited dev memory switch")
	}
	if err := memoryEnrollment(root, "bob", "dev"); err == nil {
		t.Fatal("bob inherited alice's enabled memory")
	}
	if err := memoryEnrollment(root, "carol", "dev"); err == nil {
		t.Fatal("missing published passport enabled memory")
	}
	if err := memoryEnrollment(root, "../alice", "dev"); err == nil {
		t.Fatal("invalid principal escaped passport root")
	}
	if err := memoryEnrollment("", "alice", "dev"); err == nil {
		t.Fatal("unmounted passport root enabled memory")
	}
}
