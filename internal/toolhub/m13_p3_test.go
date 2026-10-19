package toolhub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func m13P3Auth(t *testing.T) identity.Envelope {
	t.Helper()
	return identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
}

// S3.1: the operation journal persists, reloads and is owner-scoped.
func TestOperationStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	store := NewStore()
	auth := m13P3Auth(t)
	op := api.Operation{
		Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "alice", "install", "github"),
		Intent: api.OpIntentInstall, Target: "github", RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, "github"),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		State: api.OpStateRunning, CreatedAt: time.Now().UTC(),
		Steps: []api.OperationStep{{Name: "resolve", State: api.OpStepDone}},
	}
	if err := store.PutOperation(op); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.OperationFor(auth, op.OperationID)
	if err != nil {
		t.Fatalf("for: %v", err)
	}
	if got.Intent != api.OpIntentInstall || got.State != api.OpStateRunning || len(got.Steps) != 1 || got.Steps[0].Name != "resolve" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// A foreign principal cannot read it.
	foreign := identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1")
	if _, err := reloaded.OperationFor(foreign, op.OperationID); err == nil {
		t.Fatal("foreign principal read the operation")
	}
}

// S3.1: request key derives from (principal, intent, normalized target) — a
// duplicate install call resolves the same operation.
func TestOperationRequestKeyIdempotent(t *testing.T) {
	auth := m13P3Auth(t)
	keyA := api.OperationRequestKey(auth, api.OpIntentInstall, " https://github.com/Org/Repo ")
	keyB := api.OperationRequestKey(auth, api.OpIntentInstall, "https://github.com/org/repo")
	if keyA == "" || keyA != keyB {
		t.Fatalf("request key not normalized: %q vs %q", keyA, keyB)
	}
	if api.OperationRequestKey(auth, api.OpIntentRemove, "https://github.com/org/repo") == keyA {
		t.Fatal("intent did not change the key")
	}
	other := identity.TelegramEnvelope("bobuser", 8, "runtime", "policy-1")
	if api.OperationRequestKey(other, api.OpIntentInstall, "https://github.com/org/repo") == keyA {
		t.Fatal("principal did not change the key")
	}
}

// S3.1: OperationForRequestKey resolves the live operation for the same
// (principal, intent, target); superseded records are never candidates.
func TestOperationForRequestKey(t *testing.T) {
	store := NewStore()
	auth := m13P3Auth(t)
	target := "github"
	op := api.Operation{
		Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "a1"),
		Intent: api.OpIntentInstall, Target: target, RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, target),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		State: api.OpStateDone, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOperation(op); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := store.OperationForRequestKey(auth, api.OpIntentInstall, "GitHub")
	if !ok || got.OperationID != op.OperationID {
		t.Fatalf("request key lookup failed: %v %+v", ok, got)
	}
	if _, ok := store.OperationForRequestKey(auth, api.OpIntentRemove, target); ok {
		t.Fatal("different intent matched")
	}
	op.State = api.OpStateFailed
	op.SupersededBy = "op-next0000000000000000000"
	if err := store.PutOperation(op); err != nil {
		t.Fatalf("put2: %v", err)
	}
	if _, ok := store.OperationForRequestKey(auth, api.OpIntentInstall, target); ok {
		t.Fatal("superseded operation matched the request key")
	}
}

// S3.1: Validate rejects operations without identity, with unknown
// intent/state/code or malformed steps.
func TestOperationValidate(t *testing.T) {
	auth := m13P3Auth(t)
	base := func() api.Operation {
		return api.Operation{
			Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", "v"),
			Intent: api.OpIntentInstall, Target: "x", RequestKey: api.OperationRequestKey(auth, api.OpIntentInstall, "x"),
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
			State: api.OpStateQueued, CreatedAt: time.Now().UTC(),
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid op rejected: %v", err)
	}
	cases := map[string]func(*api.Operation){
		"bad state":      func(o *api.Operation) { o.State = "exploding" },
		"bad intent":     func(o *api.Operation) { o.Intent = "vibes" },
		"missing target": func(o *api.Operation) { o.Target = "" },
		"bad principal":  func(o *api.Operation) { o.PrincipalID = "!!" },
		"bad step state": func(o *api.Operation) { o.Steps = []api.OperationStep{{Name: "resolve", State: "lol"}} },
		"bad code":       func(o *api.Operation) { o.ErrorCode = "because reasons" },
		"dup step": func(o *api.Operation) {
			o.Steps = []api.OperationStep{{Name: "resolve", State: api.OpStepDone}, {Name: "resolve", State: api.OpStepPending}}
		},
	}
	for name, mutate := range cases {
		op := base()
		mutate(&op)
		if err := op.Validate(); err == nil {
			t.Fatalf("%s: invalid operation accepted", name)
		}
	}
}
