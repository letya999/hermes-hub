package toolhub

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S4.x wiring: the reconciler the process schedules is built from the live
// control plane — every seam the ScanOnce ladder calls must be bound, or the
// production loop silently skips the checks the issue requires.
func TestNewReconcilerWiresProductionSeams(t *testing.T) {
	store, _, _, _ := managedStore(t)
	c := &ControlPlane{Store: store}
	c.SurfaceProbe = func(context.Context, api.Connection, api.ToolDefinition) ([]api.ToolSpec, error) { return nil, nil }
	r := c.NewReconciler()
	if r == nil || r.Store != store {
		t.Fatalf("reconciler missing store: %+v", r)
	}
	seams := map[string]bool{
		"CredAlive":   r.CredAlive != nil,
		"CredResolve": r.CredResolve != nil,
		"CredPurge":   r.CredPurge != nil,
		"Resolve":     r.Resolve != nil,
		"Surface":     r.Surface != nil,
		"Notify":      r.Notify != nil,
	}
	for name, bound := range seams {
		if !bound {
			t.Fatalf("reconciler seam %s not wired", name)
		}
	}
	// Docker label scans are opt-in: without the deployment seam the orphan
	// pass must stay unbound rather than pointing at a no-op.
	if r.Scan != nil || r.Purge != nil {
		t.Fatal("label seams bound without a scan seam")
	}
	c.LabelScan = func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, nil }
	c.LabelPurge = func(context.Context, string, string, map[string]string) error { return nil }
	r = c.NewReconciler()
	if r.Scan == nil || r.Purge == nil {
		t.Fatal("label seams not bound when configured")
	}
}

// The scheduled pass must run cleanly against a store with no Docker seam and
// no recipe pins: every check degrades to a skip instead of failing the loop.
func TestReconcileOnceRunsWithoutOptionalSeams(t *testing.T) {
	store, _, _, _ := managedStore(t)
	c := &ControlPlane{Store: store}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ScanOnce panicked without optional seams: %v", r)
		}
	}()
	if err := c.NewReconciler().ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The bound CredAlive seam must consult the credential backend, not just the
// store: a reference whose ciphertext vanished from the local backend is
// dead; with no backend configured the check cannot accuse anyone and stays
// alive.
func TestNewReconcilerCredAliveChecksBackend(t *testing.T) {
	store, _, _ := seededStore(t)
	c := &ControlPlane{Store: store}
	connection, _ := store.connectionFor("google-work")
	ref, _ := store.credentialFor(connection.CredentialRefID)
	alive, err := c.NewReconciler().CredAlive(context.Background(), ref.CredentialRefID)
	if err != nil || !alive {
		t.Fatalf("unconfigured backend must not degrade: alive=%v err=%v", alive, err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "secrets.json"), Key: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	c.Secrets = secrets
	alive, err = c.NewReconciler().CredAlive(context.Background(), ref.CredentialRefID)
	if err != nil || alive {
		t.Fatalf("missing ciphertext reported alive: alive=%v err=%v", alive, err)
	}
	locator, err := c.Secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Secrets.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": "x"}); err != nil {
		t.Fatal(err)
	}
	live := ref
	live.CredentialRefID = api.CredentialReferenceID("google-work", 2)
	live.Revision = 2
	live.Locator = locator
	if err := store.PutCredentialReference(live); err != nil {
		t.Fatal(err)
	}
	alive, err = c.NewReconciler().CredAlive(context.Background(), live.CredentialRefID)
	if err != nil || !alive {
		t.Fatalf("live ciphertext reported dead: alive=%v err=%v", alive, err)
	}
}

// The digest seams re-derive from the content-addressed recipe the pin names:
// a recipe whose credential claims differ from the pin drifts the digest the
// caller compares, and a missing recipe answers empty rather than accusing.
func TestNewReconcilerResolvesStoredRecipeDigests(t *testing.T) {
	store, _, _ := seededStore(t)
	resolution := api.RecipeResolution{
		Source: api.ArtifactSource{Repository: "https://github.com/acme/work", CommitSHA: strings.Repeat("7", 40)},
		Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.acme.example/x"},
	}
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := store.connectionFor("google-work")
	connection.Metadata[api.LabelRecipe] = digest
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	r := (&ControlPlane{Store: store}).NewReconciler()
	if got, err := r.Resolve(context.Background(), connection); err != nil || got != digest {
		t.Fatalf("recipe re-resolution=%q err=%v want %q", got, err, digest)
	}
	credDigest, err := api.CredentialRecipeDigest(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.CredResolve(context.Background(), connection); err != nil || got != credDigest {
		t.Fatalf("credential recipe re-resolution=%q err=%v want %q", got, err, credDigest)
	}
}

// The bound CredPurge must really remove the stored ciphertext: after purge
// the backend no longer serves the locator under the credential owner.
func TestNewReconcilerCredPurgeDeletesBackend(t *testing.T) {
	store, _, _ := seededStore(t)
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "secrets.json"), Key: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Secrets: secrets}
	connection, _ := store.connectionFor("google-work")
	locator, err := c.Secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Secrets.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": "x"}); err != nil {
		t.Fatal(err)
	}
	live := api.CredentialReference{
		Schema:          api.SchemaVersion,
		CredentialRefID: api.CredentialReferenceID("google-work", 2),
		ConnectionID:    "google-work",
		Revision:        2,
		Backend:         "local",
		Locator:         locator,
		Keys:            []string{"GOOGLE_TOKEN"},
		Status:          api.ActiveStatus,
	}
	if err := store.PutCredentialReference(live); err != nil {
		t.Fatal(err)
	}
	r := c.NewReconciler()
	if err := r.CredPurge(context.Background(), connection, live); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Secrets.Info(locator, "alice"); err == nil {
		t.Fatal("purge left ciphertext in the backend")
	}
}

// The bound Surface seam is nil without a probe backend (the check skips
// rather than fabricating an empty surface that would falsely drift every
// pin); with a probe it re-lists through the connection's definition.
func TestNewReconcilerSurfaceUsesProbe(t *testing.T) {
	store, _, _ := seededStore(t)
	if r := (&ControlPlane{Store: store}).NewReconciler(); r.Surface != nil {
		t.Fatal("surface seam bound without a probe backend")
	}
	var got api.Connection
	c := &ControlPlane{
		Store: store,
		SurfaceProbe: func(_ context.Context, connection api.Connection, _ api.ToolDefinition) ([]api.ToolSpec, error) {
			got = connection
			return []api.ToolSpec{{Name: "search"}}, nil
		},
	}
	connection, _ := store.connectionFor("google-work")
	tools, err := c.NewReconciler().Surface(context.Background(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConnectionID != "google-work" || len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("probe passthrough wrong: conn=%q tools=%v", got.ConnectionID, tools)
	}
}

// A definition id with several registered versions must resolve to the
// version the connection was materialized under — a map-order pick compares
// the binding's pinned digest against the wrong surface and falsely degrades
// the connection.
func TestReconcilerSurfaceResolvesConnectionVersion(t *testing.T) {
	store, _, binding := seededStore(t)
	older := remoteDefinition()
	older.Version = "0.9.0"
	older.Tools = []api.ToolSpec{{Name: "legacy", Effect: api.ReadEffect}}
	if err := store.RegisterDefinition(older); err != nil {
		t.Fatal(err)
	}
	newer := remoteDefinition()
	newer.Version = "9.9.9"
	newer.Tools = []api.ToolSpec{{Name: "renamed", Effect: api.ReadEffect}}
	if err := store.RegisterDefinition(newer); err != nil {
		t.Fatal(err)
	}
	pinned := store.bindings[binding.ToolBindingID]
	pinned.ToolDigest = "sha256:pinned"
	store.bindings[binding.ToolBindingID] = pinned
	var probed []string
	c := &ControlPlane{Store: store, SurfaceProbe: func(_ context.Context, _ api.Connection, definition api.ToolDefinition) ([]api.ToolSpec, error) {
		probed = append(probed, definition.Version)
		return definition.Tools, nil
	}}
	connection, _ := store.connectionFor("google-work")
	for i := 0; i < 20; i++ {
		tools, err := c.NewReconciler().Surface(context.Background(), connection)
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 2 || tools[0].Name != "search" || tools[1].Name != "send" {
			t.Fatalf("probe resolved a different definition version: %v", tools)
		}
	}
	for _, version := range probed {
		if version != "1.0.0" {
			t.Fatalf("probe ran against definition version %s, want pinned 1.0.0", version)
		}
	}
}

// With no binding pinning a version the resolution still must be
// deterministic — the newest registered version wins, never a random one.
func TestDefinitionForConnectionFallsBackToNewestVersion(t *testing.T) {
	store, _, binding := seededStore(t)
	newer := remoteDefinition()
	newer.Version = "0.9.9"
	newer.Tools = []api.ToolSpec{{Name: "legacy", Effect: api.ReadEffect}}
	if err := store.RegisterDefinition(newer); err != nil {
		t.Fatal(err)
	}
	connection, _ := store.connectionFor("google-work")
	delete(store.bindings, binding.ToolBindingID)
	for i := 0; i < 20; i++ {
		definition, err := store.definitionForConnection(connection)
		if err != nil {
			t.Fatal(err)
		}
		if definition.Version != "1.0.0" {
			t.Fatalf("unversioned connection resolved %s, want newest 1.0.0", definition.Version)
		}
	}
}

// The Notify seam is the owner-visible half of a reconcile event: a reauth
// notice re-arms the credential step on the connection's onboarding and a
// drift notice re-arms confirmation — the channel link the gateway delivers
// then points at a live step instead of a done page.
func TestNewReconcilerNotifyRearmsOnboarding(t *testing.T) {
	store, _, _ := seededStore(t)
	onboarding := api.Onboarding{
		Schema:        api.SchemaVersion,
		OnboardingID:  "onb-google-work",
		PrincipalID:   "alice",
		ContextID:     "alice",
		RuntimeID:     "runtime",
		PolicyVersion: "policy-1",
		Mode:          api.OnboardingCatalog,
		Phase:         api.PhaseEnabled,
		ConnectionID:  "google-work",
		Revision:      1,
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	r := (&ControlPlane{Store: store}).NewReconciler()
	if err := r.Notify(context.Background(), "alice", "reauth", map[string]any{"connection": "google-work"}); err != nil {
		t.Fatal(err)
	}
	rearmed, err := store.onboarding("onb-google-work")
	if err != nil {
		t.Fatal(err)
	}
	if rearmed.Phase != api.PhaseAwaitingCreds || rearmed.FormNonce == "" || rearmed.WizardNonce == "" {
		t.Fatalf("reauth notice left phase=%s form=%q wizard=%q", rearmed.Phase, rearmed.FormNonce, rearmed.WizardNonce)
	}
	if err := r.Notify(context.Background(), "alice", "drift", map[string]any{"connection": "google-work"}); err != nil {
		t.Fatal(err)
	}
	rearmed, _ = store.onboarding("onb-google-work")
	if rearmed.Phase != api.PhaseAwaitingConfirm || rearmed.ConfirmationNonce == "" || rearmed.ConfirmationUsed {
		t.Fatalf("drift notice left phase=%s nonce=%q used=%v", rearmed.Phase, rearmed.ConfirmationNonce, rearmed.ConfirmationUsed)
	}
}

// A credential rotation is the reauth heal: a connection the reconciler
// parked in reauth must accept a fresh reference and come back active.
func TestRotateCredentialHealsReauthConnection(t *testing.T) {
	store, _, _ := seededStore(t)
	if err := store.MarkReauth("google-work"); err != nil {
		t.Fatal(err)
	}
	reference, err := store.RotateCredential("google-work", "local", "local://alice/google/2", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatalf("reauth connection refused rotation: %v", err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ActiveStatus || connection.CredentialRefID != reference.CredentialRefID {
		t.Fatalf("connection not healed: %+v", connection)
	}
}

// The dead-credential variant: a degraded connection without pending drift
// pins heals the same way when the owner resubmits the form.
func TestRotateCredentialHealsDeadCredentialDegrade(t *testing.T) {
	store, _, _ := seededStore(t)
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/2", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatalf("degraded dead-credential connection refused rotation: %v", err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ActiveStatus {
		t.Fatalf("connection not healed: %+v", connection)
	}
}

// A degraded connection carrying a pending surface/recipe accept pin must
// NOT heal through credential rotation alone — the human confirm is owed.
func TestRotateCredentialDoesNotBypassDriftAccept(t *testing.T) {
	store, _, _ := seededStore(t)
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveDrift("google-work", api.LabelAcceptSurface, "sha256:new-surface"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/2", []string{"GOOGLE_TOKEN"}); err == nil {
		t.Fatal("drift-pinned degraded connection rotated past the human confirm")
	}
}

// The observed credential-recipe drift the reconciler recorded must become
// the connection's new pin when fresh credentials heal it — otherwise the
// next pass re-detects the same drift and re-parks the connection forever.
func TestRotateCredentialRepinsCredentialRecipeDrift(t *testing.T) {
	store, _, _ := seededStore(t)
	connection, _ := store.connectionFor("google-work")
	connection.Metadata[api.LabelCredentialRecipe] = "sha256:cred-old"
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReauth("google-work"); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveDrift("google-work", api.LabelAcceptCredential, "sha256:cred-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/2", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatalf("reauth rotation: %v", err)
	}
	after, _ := store.connectionFor("google-work")
	if after.Metadata[api.LabelCredentialRecipe] != "sha256:cred-new" {
		t.Fatalf("credential recipe not re-pinned: %v", after.Metadata)
	}
	if after.Metadata[api.LabelAcceptCredential] != "" {
		t.Fatal("accept pin not consumed")
	}
}

// materializeBinding must reach the healable connection even though the
// active-only matcher cannot see it: resubmitted credentials rotate the same
// record instead of minting a parallel connection.
func TestMaterializeRotatesHealableConnection(t *testing.T) {
	store, auth, _ := seededStore(t)
	if err := store.MarkReauth("google-work"); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-reauth", PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: "google-work", DefinitionVersion: "1.0.0",
		Locator: "local://alice/google/2", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}},
		Phase: api.PhaseAwaitingConfirm, Revision: 1, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	binding, err := c.materializeBinding(context.Background(), auth, onboarding, remoteDefinition())
	if err != nil {
		t.Fatalf("reauth materialize: %v", err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ActiveStatus || binding.ConnectionID != "google-work" {
		t.Fatalf("heal bound wrong state: conn=%+v binding=%+v", connection, binding)
	}
}

// A degraded connection whose credential died heals the same way — the
// resubmitted locator rotates it; no parallel connection may appear.
func TestMaterializeRotatesDeadCredentialDegraded(t *testing.T) {
	store, auth, _ := seededStore(t)
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-degraded", PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: "google-work", DefinitionVersion: "1.0.0",
		Locator: "local://alice/google/2", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}},
		Phase: api.PhaseAwaitingConfirm, Revision: 1, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := c.materializeBinding(context.Background(), auth, onboarding, remoteDefinition()); err != nil {
		t.Fatalf("degraded materialize: %v", err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ActiveStatus {
		t.Fatalf("degraded connection not healed: %s", connection.Status)
	}
}

// The drift accept path: the human's /confirm POST must re-pin the observed
// surface and return the connection to service — not re-materialize.
func TestConfirmAcceptsDriftedConnection(t *testing.T) {
	store, auth, binding := seededStore(t)
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveDrift("google-work", api.LabelAcceptSurface, "sha256:drifted-surface"); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-drift", PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: "google-work", DefinitionVersion: "1.0.0",
		Phase: api.PhaseAwaitingConfirm, ConnectionID: "google-work", BindingID: binding.ToolBindingID,
		ConfirmationNonce: "nonce-drift", ConfirmationExpires: time.Now().Add(time.Hour).UTC(),
		Revision: 1, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	if err := c.finishAuthorization(context.Background(), "onboard-drift"); err != nil {
		t.Fatalf("drift confirm: %v", err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ActiveStatus {
		t.Fatalf("connection not restored: %+v", connection)
	}
	if got := connection.Metadata[api.LabelAcceptSurface]; got != "" {
		t.Fatalf("accept pin not consumed: %q", got)
	}
	after, err := store.binding(binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ToolDigest != "sha256:drifted-surface" {
		t.Fatalf("binding surface not re-pinned: %q", after.ToolDigest)
	}
}

// A recipe pin accepts through the same confirm: the observed digest becomes
// the connection's new pin.
func TestConfirmAcceptsRecipeDrift(t *testing.T) {
	store, auth, binding := seededStore(t)
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveDrift("google-work", api.LabelAcceptRecipe, "sha256:new-recipe"); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-rdrift", PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: "google-work", DefinitionVersion: "1.0.0",
		Phase: api.PhaseAwaitingConfirm, ConnectionID: "google-work", BindingID: binding.ToolBindingID,
		ConfirmationNonce: "nonce-rdrift", ConfirmationExpires: time.Now().Add(time.Hour).UTC(),
		Revision: 1, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	if err := c.finishAuthorization(context.Background(), "onboard-rdrift"); err != nil {
		t.Fatalf("recipe drift confirm: %v", err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ActiveStatus || connection.Metadata[api.LabelRecipe] != "sha256:new-recipe" {
		t.Fatalf("recipe pin not re-pinned: %+v", connection)
	}
}

// Drift must leave an accept pin the human confirm can consume: the pass
// records the observed surface digest under the surface key — otherwise
// /confirm has nothing to re-pin and the connection parks forever.
func TestScanOnceRecordsSurfaceDriftAcceptPin(t *testing.T) {
	store, _, binding := seededStore(t)
	pinned := store.bindings[binding.ToolBindingID]
	pinned.ToolDigest = "sha256:pinned-surface"
	store.bindings[binding.ToolBindingID] = pinned
	surface := []api.ToolSpec{{Name: "renamed"}}
	r := &Reconciler{
		Store:     store,
		Surface:   func(context.Context, api.Connection) ([]api.ToolSpec, error) { return surface, nil },
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Notify:    func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.connectionFor("google-work")
	if after.Metadata[api.LabelAcceptSurface] != api.ToolSurfaceDigest(surface) {
		t.Fatalf("surface drift not recorded: %v", after.Metadata)
	}
}

// A recipe drift records the resolved digest under the recipe accept key so
// the same /confirm step re-pins it.
func TestScanOnceRecordsRecipeDriftAcceptPin(t *testing.T) {
	store, _, _ := seededStore(t)
	connection, _ := store.connectionFor("google-work")
	connection.Metadata[api.LabelRecipe] = "sha256:old-recipe"
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Store:     store,
		Resolve:   func(context.Context, api.Connection) (string, error) { return "sha256:new-recipe", nil },
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Notify:    func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.connectionFor("google-work")
	if after.Metadata[api.LabelAcceptRecipe] != "sha256:new-recipe" {
		t.Fatalf("recipe drift not recorded: %v", after.Metadata)
	}
}

// A credential-recipe drift records the resolved digest under the credential
// accept key so the credential-rotation heal re-pins it.
func TestScanOnceRecordsCredentialDriftAcceptPin(t *testing.T) {
	store, _, _ := seededStore(t)
	connection, _ := store.connectionFor("google-work")
	connection.Metadata[api.LabelCredentialRecipe] = "sha256:old-cred"
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Store:       store,
		CredResolve: func(context.Context, api.Connection) (string, error) { return "sha256:new-cred", nil },
		CredAlive:   func(context.Context, string) (bool, error) { return true, nil },
		Notify:      func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.connectionFor("google-work")
	if after.Metadata[api.LabelAcceptCredential] != "sha256:new-cred" {
		t.Fatalf("credential drift not recorded: %v", after.Metadata)
	}
}

// The install facade routes every target form the issue names: a cli spec in
// args, github-release pins, repository URLs, remote MCP URLs and bare CLI
// names all reach a real pipeline — never a bare not-found.
func TestConnectorInstallRoutesAllTargetForms(t *testing.T) {
	store, auth, _ := seededStore(t)
	c := &ControlPlane{Store: store}
	ctx := context.Background()
	targets := []string{
		"github-release:cli/cli@v2.78.0",
		"https://github.com/owner/repo/tree/0123456789abcdef0123456789abcdef01234567",
		"https://mcp.example.com/server",
		"jq",
	}
	for _, target := range targets {
		_, err := c.prepareInstallTarget(ctx, auth, target, "", "req-1", map[string]any{"target": target})
		if err == nil {
			t.Fatalf("target %q unexpectedly succeeded", target)
		}
		if errors.Is(err, api.ErrNotFound) {
			t.Fatalf("target %q unrouted: %v", target, err)
		}
	}
	// An explicit cli spec object reaches the same pipeline.
	if _, err := c.prepareInstallTarget(ctx, auth, "gh", "", "req-2", map[string]any{"cli": map[string]any{"name": "gh", "command": "gh"}}); err == nil || errors.Is(err, api.ErrNotFound) {
		t.Fatalf("cli spec unrouted: %v", err)
	}
}

// A connection already parked in reauth must not be demoted to degraded by
// the dead-credential check on a later pass — the parked state outranks it.
func TestReconcilerSkipsCredCheckOnNonActive(t *testing.T) {
	store, _, _ := seededStore(t)
	if err := store.MarkReauth("google-work"); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Store:     store,
		Scan:      func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, nil },
		Purge:     func(context.Context, string, string, map[string]string) error { return nil },
		CredAlive: func(context.Context, string) (bool, error) { return false, nil },
		Notify: func(context.Context, string, string, map[string]any) error {
			t.Fatal("re-notified a parked connection")
			return nil
		},
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.ReauthStatus {
		t.Fatalf("parked connection demoted: %s", connection.Status)
	}
}
