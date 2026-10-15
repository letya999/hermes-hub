package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log/slog"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

type countingSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *countingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingSlogHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *countingSlogHandler) WithGroup(string) slog.Handler            { return h }
func (h *countingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *countingSlogHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// SPEC-0053 R1.3: a binding that drops out of the projection must be reported
// with an exact reason, scoped to its own principal — no silent drops.
func TestProjectionExclusionReportedWithReason(t *testing.T) {
	store, auth, binding := seededStore(t)
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	tools, err := store.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("disabled binding still projected: %+v", tools)
	}
	excluded, err := store.ProjectionExclusions(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 1 {
		t.Fatalf("expected one exclusion, got %+v", excluded)
	}
	if excluded[0].BindingID != binding.ToolBindingID || excluded[0].Reason != api.ExcludeDisabled {
		t.Fatalf("wrong exclusion: %+v", excluded[0])
	}
}

// Every SPEC-0053 R1.3 failure mode lands on its exact reason word.
func TestProjectionExclusionReasons(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, store *Store, auth *identity.Envelope, binding api.ToolBinding)
		reason  string
	}{
		{"disabled", func(t *testing.T, s *Store, _ *identity.Envelope, b api.ToolBinding) {
			if err := s.SetBindingStatus(b.ToolBindingID, api.DisabledStatus); err != nil {
				t.Fatal(err)
			}
		}, api.ExcludeDisabled},
		{"revoked", func(t *testing.T, s *Store, _ *identity.Envelope, b api.ToolBinding) {
			if err := s.SetBindingStatus(b.ToolBindingID, api.RevokedStatus); err != nil {
				t.Fatal(err)
			}
		}, api.ExcludeRevoked},
		{"degraded", func(t *testing.T, s *Store, _ *identity.Envelope, _ api.ToolBinding) {
			if err := s.MarkDegraded("google-work"); err != nil {
				t.Fatal(err)
			}
		}, api.ExcludeDegraded},
		{"stale", func(t *testing.T, s *Store, _ *identity.Envelope, _ api.ToolBinding) {
			if err := s.SetConnectionStatus("google-work", api.DisabledStatus); err != nil {
				t.Fatal(err)
			}
		}, api.ExcludeStale},
		{"policy_mismatch", func(t *testing.T, _ *Store, auth *identity.Envelope, _ api.ToolBinding) {
			auth.PolicyVersion = "policy-9"
		}, api.ExcludePolicyMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, auth, binding := seededStore(t)
			tc.prepare(t, store, &auth, binding)
			if _, err := store.ListProjectedTools(auth); err != nil {
				t.Fatal(err)
			}
			excluded, err := store.ProjectionExclusions(auth)
			if err != nil {
				t.Fatal(err)
			}
			if len(excluded) != 1 || excluded[0].Reason != tc.reason {
				t.Fatalf("expected one %q exclusion, got %+v", tc.reason, excluded)
			}
		})
	}
}

// A binding that keeps failing logs once per (principal, binding, projection
// revision, reason) — repeated tools/list calls must not flood the log.
func TestProjectionExclusionLoggedOnce(t *testing.T) {
	handler := &countingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	store, auth, binding := seededStore(t)
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.ListProjectedTools(auth); err != nil {
			t.Fatal(err)
		}
	}
	if got := handler.count("toolhub projection excluded"); got != 1 {
		t.Fatalf("expected one exclusion log line, got %d", got)
	}
	// A projection bump is a new revision: the same failing binding logs again.
	if err := store.SetBindingStatus(binding.ToolBindingID, api.ActiveStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectedTools(auth); err != nil {
		t.Fatal(err)
	}
	if got := handler.count("toolhub projection excluded"); got != 2 {
		t.Fatalf("expected a second log line after a new revision, got %d", got)
	}
}

// The exclusion set is bounded in memory: a flood of distinct failing
// bindings evicts the oldest entries instead of growing without limit.
func TestProjectionExclusionBoundedSet(t *testing.T) {
	store := NewStore()
	for i := 0; i < api.ExclusionLogLimit+50; i++ {
		binding := api.ToolBinding{
			PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
			ToolBindingID: fmt.Sprintf("bind-%04d", i), DefinitionID: "def",
		}
		store.noteExclusion(binding, api.ExcludeDisabled)
	}
	if len(store.exclusions) != api.ExclusionLogLimit {
		t.Fatalf("exclusion set is not bounded: %d entries", len(store.exclusions))
	}
}

// Red team: a principal's status view must never reveal another principal's
// excluded bindings — missing tools are authorization metadata.
func TestProjectionExclusionsScopeIsolated(t *testing.T) {
	store, auth, binding := seededStore(t)
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectedTools(auth); err != nil {
		t.Fatal(err)
	}
	other := identity.TelegramEnvelope("bob", 9, "runtime", "policy-1")
	excluded, err := store.ProjectionExclusions(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 0 {
		t.Fatalf("cross-principal exclusion leak: %+v", excluded)
	}
}

// The existing ToolHub diagnostics surface reports the caller's own excluded
// bindings even when log collection is off — missing tools are always
// operator-visible (SPEC-0053 R1.3).
func TestDiagnosticsReportsExclusions(t *testing.T) {
	store, auth, binding := seededStore(t)
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store}
	body, err := control.diagnostics(context.Background(), auth, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	excluded, ok := body["excluded"].([]api.ProjectionExclusion)
	if !ok || len(excluded) != 1 || excluded[0].Reason != api.ExcludeDisabled || excluded[0].BindingID != binding.ToolBindingID {
		t.Fatalf("diagnostics surface lacks the excluded binding: %+v", body["excluded"])
	}
}
