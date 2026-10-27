package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S1.1: structured resolution errors (#106).

func TestErrorCodeMapsResolutionSentinels(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{api.ErrAmbiguousEntrypoint, "ambiguous_entrypoint"},
		{fmt.Errorf("recipe: %w", api.ErrAmbiguousEntrypoint), "ambiguous_entrypoint"},
		{api.ErrUnsupportedCredentialContract, "unsupported_credential_contract"},
		{api.ErrUnsupportedAuth, "unsupported_auth"},
		{api.ErrInvalid, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := api.ErrorCode(tc.err); got != tc.want {
			t.Fatalf("ErrorCode(%v)=%q want %q", tc.err, got, tc.want)
		}
	}
	// New sentinels stay ErrInvalid-compatible so existing fail-closed callers hold.
	for _, sentinel := range []error{api.ErrAmbiguousEntrypoint, api.ErrUnsupportedCredentialContract, api.ErrUnsupportedAuth} {
		if !errors.Is(sentinel, api.ErrInvalid) {
			t.Fatalf("%v lost ErrInvalid compatibility", sentinel)
		}
	}
}

func artifactContext(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArtifactRecipeAmbiguousEntrypointCarriesCode(t *testing.T) {
	contextBytes := artifactContext(t, map[string]string{
		"package.json":      `{"name":"two-bins","bin":{"one":"one.js","two":"two.js"}}`,
		"package-lock.json": `{"lockfileVersion":3}`,
		"one.js":            "// one",
		"two.js":            "// two",
	})
	_, _, err := api.GenerateArtifactRecipe(contextBytes, "node", "", nil)
	if !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("ambiguous entrypoint error lacks sentinel: %v", err)
	}
	if !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("sentinel broke ErrInvalid compatibility: %v", err)
	}
	if got := api.ErrorCode(err); got != "ambiguous_entrypoint" {
		t.Fatalf("error_code=%q", got)
	}
}

func TestArtifactRecipeAmbiguousWorkspaceCarriesCode(t *testing.T) {
	contextBytes := artifactContext(t, map[string]string{
		"package.json":   `{}`,
		"pnpm-lock.yaml": "",
		"a/package.json": `{"name":"a-mcp","bin":"a.js"}`,
		"b/package.json": `{"name":"b-mcp","bin":"b.js"}`,
		"a/a.js":         "// a",
		"b/b.js":         "// b",
	})
	_, _, err := api.GenerateArtifactRecipe(contextBytes, "node", "", nil)
	if !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("ambiguous workspace error lacks sentinel: %v", err)
	}
	if got := api.ErrorCode(err); got != "ambiguous_entrypoint" {
		t.Fatalf("error_code=%q", got)
	}
}

func TestArtifactRecipeAmbiguousGoAndRustCarryCode(t *testing.T) {
	goContext := artifactContext(t, map[string]string{
		"go.mod":    "module example.org/mcp",
		"go.sum":    "",
		"a/main.go": "package main",
		"b/main.go": "package main",
	})
	if _, _, err := api.GenerateArtifactRecipe(goContext, "go", "", nil); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("ambiguous go mains error lacks sentinel: %v", err)
	}
	rustContext := artifactContext(t, map[string]string{
		"Cargo.lock":  "",
		"Cargo.toml":  "[package]\nname='mcp'\n[workspace]\n",
		"src/main.rs": "fn main() {}",
	})
	if _, _, err := api.GenerateArtifactRecipe(rustContext, "rust", "", nil); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("ambiguous rust workspace error lacks sentinel: %v", err)
	}
}

func TestResolveLocalRecipeComposeAmbiguityCarriesCode(t *testing.T) {
	compose := "services:\n  first-mcp:\n    image: ghcr.io/acme/first-mcp@sha256:" + strings.Repeat("1", 64) + "\n  second-mcp:\n    image: ghcr.io/acme/second-mcp@sha256:" + strings.Repeat("2", 64) + "\n"
	files := map[string][]byte{"compose.yaml": []byte(compose)}
	if _, _, _, err := api.ResolveLocalRecipe(files, api.ArtifactSource{}); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("compose ambiguity lacks sentinel: %v", err)
	}
	// A compose file with several non-MCP services abstains — it must not veto
	// the Dockerfile/language fallback paths that may still prove an entrypoint.
	files = map[string][]byte{"compose.yaml": []byte("services:\n  db:\n    image: postgres@sha256:" + strings.Repeat("3", 64) + "\n  cache:\n    image: redis@sha256:" + strings.Repeat("4", 64) + "\n")}
	launch, _, warnings, err := api.ResolveLocalRecipe(files, api.ArtifactSource{})
	if err != nil || api.HasLaunch(launch) {
		t.Fatalf("non-mcp compose vetoed resolution: launch=%+v err=%v", launch, err)
	}
	ambiguous := false
	for _, warning := range warnings {
		ambiguous = ambiguous || strings.Contains(warning, "ambiguous")
	}
	if !ambiguous {
		t.Fatalf("abstention not recorded: %v", warnings)
	}
}

// ---------- S2: lifecycle UX (#104) ----------

// A bare remove must never delete: it previews what goes away and asks for
// an explicit confirmation; only remove+confirm_nonce executes.
func TestRemoveRequiresExplicitConfirmation(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prepared["onboarding_id"].(string)

	preview, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": onboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if preview["phase"] == api.PhaseRemoved {
		t.Fatalf("bare remove deleted the connector: %v", preview)
	}
	if preview["requires_confirmation"] != true {
		t.Fatalf("bare remove did not request confirmation: %v", preview)
	}
	nonce := removeNonceOf(t, store, onboardingID)
	if nonce == "" {
		t.Fatalf("preview returned no confirmation token: %v", preview)
	}
	if mustOnboarding(t, store, onboardingID).Phase == api.PhaseRemoved {
		t.Fatal("bare remove mutated the record")
	}
	// A wrong token is a refusal, not a delete.
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": onboardingID, "remove_nonce": "wrong"}); err == nil {
		t.Fatal("forged remove nonce executed")
	}
	if mustOnboarding(t, store, onboardingID).Phase == api.PhaseRemoved {
		t.Fatal("forged nonce removed the connector")
	}
	// The previewed nonce executes and is single-use.
	removed, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": onboardingID, "remove_nonce": nonce})
	if err != nil {
		t.Fatal(err)
	}
	if removed["phase"] != api.PhaseRemoved {
		t.Fatalf("confirmed remove phase: %v", removed)
	}
	// Removing an already-removed connector is an idempotent status report,
	// not an error.
	again, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": onboardingID, "remove_nonce": nonce})
	if err != nil || again["phase"] != api.PhaseRemoved {
		t.Fatalf("idempotent remove=%v err=%v", again, err)
	}
}

// "выключи notion" must not need an onboarding_id: lifecycle ops resolve the
// connector by definition id, repository URL or repo name.
func TestLifecycleResolvesFriendlySelector(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	definition := catalogReadDefinition()
	definition.DefinitionID = "search-mcp"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prepared["onboarding_id"].(string)
	onboarding := mustOnboarding(t, store, onboardingID)
	onboarding.SourceURL = "https://github.com/acme/search-mcp"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	// Drive it to enabled so disable exercises a real binding.
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": onboarding.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID}); err != nil {
		t.Fatal(err)
	}

	// Selector by definition id already works through definition_id; the new
	// name/URL forms must resolve the same record.
	for _, selector := range []string{"search-mcp", "https://github.com/acme/search-mcp"} {
		body, err := control.Invoke(context.Background(), auth, "status", map[string]any{"selector": selector})
		if err != nil {
			t.Fatalf("status selector %q: %v", selector, err)
		}
		if body["onboarding_id"] != onboardingID {
			t.Fatalf("selector %q resolved %+v", selector, body["onboarding_id"])
		}
	}
	disabled, err := control.Invoke(context.Background(), auth, "disable", map[string]any{"selector": "search-mcp"})
	if err != nil {
		t.Fatalf("disable by name: %v", err)
	}
	if disabled["phase"] != api.PhaseDisabled {
		t.Fatalf("disable by name: %v", disabled)
	}
	// An unknown selector is not-found; it must not silently pick "latest".
	if _, err := control.Invoke(context.Background(), auth, "disable", map[string]any{"selector": "other-mcp"}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("unknown selector: %v", err)
	}
}

// "disable search-mcp" should work when the repo basename differs from the
// definition id, through any of the selector/name/source aliases. Two live
// onboardings with the same basename must fail ambiguous, never pick one.
func TestSelectorMatchesBasenameAndAliases(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	definition := catalogReadDefinition()
	definition.DefinitionID = "acme-search-v2"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prepared["onboarding_id"].(string)
	onboarding := mustOnboarding(t, store, onboardingID)
	onboarding.SourceURL = "https://github.com/acme/search-mcp.git"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	// Repo basename under each alias resolves the same record.
	for _, arg := range []string{"selector", "name", "source"} {
		body, err := control.Invoke(context.Background(), auth, "status", map[string]any{arg: "search-mcp"})
		if err != nil {
			t.Fatalf("status %s=%q: %v", arg, "search-mcp", err)
		}
		if body["onboarding_id"] != onboardingID {
			t.Fatalf("%s selector resolved %+v", arg, body["onboarding_id"])
		}
	}

	// A second live onboarding with the same basename makes the selector
	// ambiguous — refuse instead of picking the newest.
	definition2 := catalogReadDefinition()
	definition2.DefinitionID = "acme-search-fork"
	if err := store.RegisterDefinition(definition2); err != nil {
		t.Fatal(err)
	}
	prepared2, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition2.DefinitionID, "version": definition2.Version})
	if err != nil {
		t.Fatal(err)
	}
	onboarding2 := mustOnboarding(t, store, prepared2["onboarding_id"].(string))
	onboarding2.SourceURL = "https://github.com/other/search-mcp"
	if err := store.PutOnboarding(onboarding2); err != nil {
		t.Fatal(err)
	}
	_, err = control.Invoke(context.Background(), auth, "disable", map[string]any{"selector": "search-mcp"})
	if err == nil || errors.Is(err, api.ErrNotFound) {
		t.Fatalf("ambiguous selector must not resolve silently: %v", err)
	}
	// The exact source URL still picks one record under ambiguity.
	body, err := control.Invoke(context.Background(), auth, "status", map[string]any{"source": "https://github.com/other/search-mcp"})
	if err != nil || body["onboarding_id"] != prepared2["onboarding_id"] {
		t.Fatalf("exact source under ambiguity: %v / %v", body, err)
	}
}

// status(operation_id) lets the agent poll an in-flight install by the
// request_key it used, without storing onboarding_id.
func TestStatusResolvesOperationID(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "install-search-1"})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prepared["onboarding_id"].(string)

	body, err := control.Invoke(context.Background(), auth, "status", map[string]any{"operation_id": "install-search-1"})
	if err != nil {
		t.Fatalf("status by operation_id: %v", err)
	}
	if body["onboarding_id"] != onboardingID {
		t.Fatalf("operation_id resolved %+v", body["onboarding_id"])
	}
	// request_key is an accepted alias — it is the field prepare_source took.
	body, err = control.Invoke(context.Background(), auth, "status", map[string]any{"request_key": "install-search-1"})
	if err != nil || body["onboarding_id"] != onboardingID {
		t.Fatalf("request_key status=%v err=%v", body, err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"operation_id": "never-seen"}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("unknown operation_id: %v", err)
	}
}

// A shared workload may carry stored credentials only when the reviewed
// definition declares itself sharable; otherwise the Shared class stays
// per-request-only so one tenant's secret can never ride a common container.
func TestSharedDefinitionRequiresSharableForStaticCredentials(t *testing.T) {
	definition := remoteDefinition()
	definition.DefinitionID = "org-search"
	definition.Workload.Class = api.Shared
	definition.Workload.Rationale = "one stateless connector serves every member"
	if err := definition.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("shared workload with stored credentials admitted: %v", err)
	}
	definition.Workload.Sharable = true
	if err := definition.Validate(); err != nil {
		t.Fatalf("sharable shared definition rejected: %v", err)
	}
	// Sharable does not lift the other shared-workload limits.
	definition.Workload.Stateful = true
	if err := definition.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("stateful shared workload admitted: %v", err)
	}
}

// A shared connector covered by a SharedCredentialPolicy binds every member
// to one owner connection — all five members resolve a single workload id.
func TestSharedConnectorResolvesOneWorkload(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	definition := remoteDefinition()
	definition.DefinitionID = "org-search"
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	definition.Workload.Rationale = "one stateless connector serves every member"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	locator, err := secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(locator, "ops-owner", map[string]string{"GOOGLE_TOKEN": "org-token"}); err != nil {
		t.Fatal(err)
	}
	principals := []string{"u00", "u01", "u02", "u03", "u04"}
	policy := api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "policy-org", DefinitionID: "org-search", Locator: locator, StoreOwner: "ops-owner", Principals: principals, IssuedBy: "operator", Status: api.ActiveStatus}
	if err := store.PutSharedCredentialPolicy(policy); err != nil {
		t.Fatal(err)
	}
	var workloadID, connectionID string
	for _, principal := range principals {
		auth := identity.TelegramEnvelope(principal, 9, principal+"-runtime", "policy-1")
		if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, principal, "org-search", "1.0.0")); err != nil {
			t.Fatal(err)
		}
		grantTestControlOperations(t, store, principal, "prepare_source", "status", "confirm", "enable", "required_credentials")
		prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "org-search", "version": "1.0.0", "request_key": "shared-" + principal})
		if err != nil {
			t.Fatal(err)
		}
		onboardingID := prepared["onboarding_id"].(string)
		onboarding := mustOnboarding(t, store, onboardingID)
		if err := control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"GOOGLE_TOKEN": "ignored"}); err != nil {
			t.Fatalf("%s submit: %v", principal, err)
		}
		if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": confirmNonceOf(t, store, onboardingID)}); err != nil {
			t.Fatalf("%s confirm: %v", principal, err)
		}
		enabled, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID})
		if err != nil {
			t.Fatalf("%s enable: %v", principal, err)
		}
		effective, err := store.Resolve(auth, enabled["binding_id"].(string))
		if err != nil {
			t.Fatalf("%s resolve: %v", principal, err)
		}
		if effective.Connection == nil || effective.Connection.Owner.ID != "ops-owner" {
			t.Fatalf("%s bound a member-owned connection: %+v", principal, effective.Connection)
		}
		if workloadID == "" {
			workloadID, connectionID = effective.WorkloadID, effective.Connection.ConnectionID
		} else if effective.WorkloadID != workloadID || effective.Connection.ConnectionID != connectionID {
			t.Fatalf("%s got own workload %s/%s, want shared %s/%s", principal, effective.WorkloadID, effective.Connection.ConnectionID, workloadID, connectionID)
		}
		env, err := api.DecryptAuthorized(secrets, effective)
		if err != nil || env["GOOGLE_TOKEN"] != "org-token" {
			t.Fatalf("%s shared decrypt=%v err=%v", principal, env, err)
		}
	}
	// Exactly one connection and one credential reference for all members.
	store.mu.RLock()
	connections := len(store.connections)
	store.mu.RUnlock()
	if connections != 1 {
		t.Fatalf("shared connector created %d connections", connections)
	}
}

// Members hold their own binding only: rotate of the shared credential is
// refused, and a member revoke cuts that member's access without touching
// the shared connection, the shared ciphertext or other members' resolves.
func TestSharedConnectorMemberLifecycleIsScoped(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	definition := remoteDefinition()
	definition.DefinitionID = "org-search"
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	definition.Workload.Rationale = "one stateless connector serves every member"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	locator, err := secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(locator, "ops-owner", map[string]string{"GOOGLE_TOKEN": "org-token"}); err != nil {
		t.Fatal(err)
	}
	principals := []string{"u00", "u01"}
	policy := api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "policy-org", DefinitionID: "org-search", Locator: locator, StoreOwner: "ops-owner", Principals: principals, IssuedBy: "operator", Status: api.ActiveStatus}
	if err := store.PutSharedCredentialPolicy(policy); err != nil {
		t.Fatal(err)
	}
	enableMember := func(principal string) (api.Onboarding, string) {
		auth := identity.TelegramEnvelope(principal, 9, principal+"-runtime", "policy-1")
		if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, principal, "org-search", "1.0.0")); err != nil {
			t.Fatal(err)
		}
		grantTestControlOperations(t, store, principal, api.ControlOperations...)
		prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "org-search", "version": "1.0.0", "request_key": "shared-" + principal})
		if err != nil {
			t.Fatal(err)
		}
		onboardingID := prepared["onboarding_id"].(string)
		onboarding := mustOnboarding(t, store, onboardingID)
		if err := control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"GOOGLE_TOKEN": "ignored"}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": confirmNonceOf(t, store, onboardingID)}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		return mustOnboarding(t, store, onboardingID), onboardingID
	}
	onboarding0, _ := enableMember("u00")
	onboarding1, onboarding1ID := enableMember("u01")
	sharedConnectionID := onboarding0.ConnectionID
	if sharedConnectionID == "" || sharedConnectionID != onboarding1.ConnectionID {
		t.Fatalf("members did not share one connection: %q vs %q", sharedConnectionID, onboarding1.ConnectionID)
	}

	// A member cannot rotate the shared credential — only the store owner can.
	member0 := identity.TelegramEnvelope("u00", 9, "u00-runtime", "policy-1")
	if _, err := control.Invoke(context.Background(), member0, "rotate", map[string]any{"onboarding_id": onboarding0.OnboardingID}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("member rotated shared credential: %v", err)
	}
	// A member revoke cuts only that member's binding.
	if _, err := control.Invoke(context.Background(), member0, "revoke", map[string]any{"onboarding_id": onboarding0.OnboardingID}); err != nil {
		t.Fatalf("member revoke: %v", err)
	}
	store.mu.RLock()
	sharedConn := store.connections[sharedConnectionID]
	store.mu.RUnlock()
	if sharedConn.Status != api.ActiveStatus {
		t.Fatalf("member revoke touched the shared connection: %s", sharedConn.Status)
	}
	if _, err := secrets.Info(locator, "ops-owner"); err != nil {
		t.Fatalf("member revoke touched the shared ciphertext: %v", err)
	}
	// u01 still resolves and decrypts the shared workload.
	member1 := identity.TelegramEnvelope("u01", 9, "u01-runtime", "policy-1")
	binding1 := mustOnboarding(t, store, onboarding1ID).BindingID
	effective, err := store.Resolve(member1, binding1)
	if err != nil {
		t.Fatalf("surviving member resolve: %v", err)
	}
	if env, err := api.DecryptAuthorized(secrets, effective); err != nil || env["GOOGLE_TOKEN"] != "org-token" {
		t.Fatalf("surviving member decrypt=%v err=%v", env, err)
	}
}

// A member disable must not stop the shared workload while other members
// still hold active bindings on the same connection.
func TestSharedWorkloadSurvivesMemberDisable(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	stopped := &recordingStopper{}
	store := NewStore()
	store.Stopper = stopped
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	definition := remoteDefinition()
	definition.DefinitionID = "org-search"
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	definition.Workload.Rationale = "one stateless connector serves every member"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	locator, err := secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(locator, "ops-owner", map[string]string{"GOOGLE_TOKEN": "org-token"}); err != nil {
		t.Fatal(err)
	}
	principals := []string{"u00", "u01"}
	if err := store.PutSharedCredentialPolicy(api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "policy-org", DefinitionID: "org-search", Locator: locator, StoreOwner: "ops-owner", Principals: principals, IssuedBy: "operator", Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	enableMember := func(principal string) api.Onboarding {
		auth := identity.TelegramEnvelope(principal, 9, principal+"-runtime", "policy-1")
		if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, principal, "org-search", "1.0.0")); err != nil {
			t.Fatal(err)
		}
		grantTestControlOperations(t, store, principal, api.ControlOperations...)
		prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "org-search", "version": "1.0.0", "request_key": "shared-" + principal})
		if err != nil {
			t.Fatal(err)
		}
		onboardingID := prepared["onboarding_id"].(string)
		onboarding := mustOnboarding(t, store, onboardingID)
		if err := control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"GOOGLE_TOKEN": "ignored"}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": confirmNonceOf(t, store, onboardingID)}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		return mustOnboarding(t, store, onboardingID)
	}
	onboarding0 := enableMember("u00")
	onboarding1 := enableMember("u01")
	// Record the running shared workload under u00's binding — the shape the
	// supervisor writes once the container is up.
	member0 := identity.TelegramEnvelope("u00", 9, "u00-runtime", "policy-1")
	binding0, err := store.binding(onboarding0.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := api.NewWorkloadInstance(binding0, nil, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(instance); err != nil {
		t.Fatal(err)
	}

	if _, err := control.Invoke(context.Background(), member0, "disable", map[string]any{"onboarding_id": onboarding0.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if stopped.seen(instance.WorkloadID) {
		t.Fatalf("member disable stopped the shared workload %s", instance.WorkloadID)
	}
	// u01's member still resolves.
	member1 := identity.TelegramEnvelope("u01", 9, "u01-runtime", "policy-1")
	if _, err := store.Resolve(member1, onboarding1.BindingID); err != nil {
		t.Fatalf("surviving member resolve after sibling disable: %v", err)
	}
	// Once the last member disables, the shared workload can stop.
	if _, err := control.Invoke(context.Background(), member1, "disable", map[string]any{"onboarding_id": onboarding1.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if !stopped.seen(instance.WorkloadID) {
		t.Fatalf("last member disable did not stop %s", instance.WorkloadID)
	}
}

// A member's confirmed remove deletes only its own binding and onboarding —
// the shared connection, credential reference and ciphertext belong to the
// policy's store owner and survive.
func TestSharedMemberRemoveKeepsSharedRecords(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	definition := remoteDefinition()
	definition.DefinitionID = "org-search"
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	definition.Workload.Rationale = "one stateless connector serves every member"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	locator, err := secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(locator, "ops-owner", map[string]string{"GOOGLE_TOKEN": "org-token"}); err != nil {
		t.Fatal(err)
	}
	principals := []string{"u00", "u01"}
	if err := store.PutSharedCredentialPolicy(api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "policy-org", DefinitionID: "org-search", Locator: locator, StoreOwner: "ops-owner", Principals: principals, IssuedBy: "operator", Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	enableMember := func(principal string) api.Onboarding {
		auth := identity.TelegramEnvelope(principal, 9, principal+"-runtime", "policy-1")
		if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, principal, "org-search", "1.0.0")); err != nil {
			t.Fatal(err)
		}
		grantTestControlOperations(t, store, principal, api.ControlOperations...)
		prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "org-search", "version": "1.0.0", "request_key": "shared-" + principal})
		if err != nil {
			t.Fatal(err)
		}
		onboardingID := prepared["onboarding_id"].(string)
		onboarding := mustOnboarding(t, store, onboardingID)
		if err := control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"GOOGLE_TOKEN": "ignored"}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": confirmNonceOf(t, store, onboardingID)}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID}); err != nil {
			t.Fatal(err)
		}
		return mustOnboarding(t, store, onboardingID)
	}
	onboarding0 := enableMember("u00")
	onboarding1 := enableMember("u01")
	member0 := identity.TelegramEnvelope("u00", 9, "u00-runtime", "policy-1")

	if _, err := control.Invoke(context.Background(), member0, "remove", map[string]any{"onboarding_id": onboarding0.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), member0, "remove", map[string]any{"onboarding_id": onboarding0.OnboardingID, "remove_nonce": removeNonceOf(t, store, onboarding0.OnboardingID)}); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	_, bindingGone := store.bindings[onboarding0.BindingID]
	_, connectionGone := store.connections[onboarding0.ConnectionID]
	credRefs := len(store.credentials)
	store.mu.RUnlock()
	if bindingGone {
		t.Fatal("member binding row survived remove")
	}
	if !connectionGone || credRefs == 0 {
		t.Fatal("member remove deleted the shared connection records")
	}
	if _, err := secrets.Info(locator, "ops-owner"); err != nil {
		t.Fatalf("member remove deleted shared ciphertext: %v", err)
	}
	// u01's member still resolves the shared workload afterwards.
	member1 := identity.TelegramEnvelope("u01", 9, "u01-runtime", "policy-1")
	effective, err := store.Resolve(member1, onboarding1.BindingID)
	if err != nil {
		t.Fatalf("surviving member resolve: %v", err)
	}
	if env, err := api.DecryptAuthorized(secrets, effective); err != nil || env["GOOGLE_TOKEN"] != "org-token" {
		t.Fatalf("surviving member decrypt=%v err=%v", env, err)
	}
}

type recordingStopper struct {
	mu   sync.Mutex
	hits map[string]bool
}

func (r *recordingStopper) Stop(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hits == nil {
		r.hits = map[string]bool{}
	}
	r.hits[id] = true
	return nil
}

func (r *recordingStopper) seen(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[id]
}

// Confirmed remove deletes the encrypted credential, not just marks it
// revoked — ciphertext must not survive in the secrets store.
func TestRemoveDeletesCredentialCiphertext(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "TOKEN", Required: true}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	id := prepared["onboarding_id"].(string)
	onboarding := mustOnboarding(t, store, id)
	if err := control.SubmitCredentials(id, onboarding.FormNonce, map[string]string{"TOKEN": "fixture-value"}); err != nil {
		t.Fatal(err)
	}
	onboarding = mustOnboarding(t, store, id)
	locator := onboarding.Locator
	if locator == "" {
		t.Fatal("credential locator not recorded")
	}
	owner := onboarding.CredentialOwner
	if owner == "" {
		owner = auth.PrincipalID
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": id}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": id, "nonce": confirmNonceOf(t, store, id)}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": id}); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Info(locator, owner); err != nil {
		t.Fatalf("credential never stored: %v", err)
	}

	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": id}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": id, "remove_nonce": removeNonceOf(t, store, id)}); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Info(locator, owner); err == nil {
		t.Fatal("credential ciphertext survived remove")
	}
	// Store rows go too — bindings, the owner connection and its credential
	// reference. The onboarding tombstone stays for idempotent replay.
	store.mu.RLock()
	rows := len(store.bindings) + len(store.connections) + len(store.credentials)
	store.mu.RUnlock()
	if rows != 0 {
		t.Fatalf("remove left %d store rows", rows)
	}
}

// A reviewed file credential lands as a read-only mount at its declared
// container target; the env var carries the in-container path.
func TestFileCredentialMaterializesAtReviewedTarget(t *testing.T) {
	workspace := t.TempDir()
	definition := statefulContainerDefinition()
	definition.Credentials = []api.CredentialInput{
		{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/mcp-secrets/client.json"},
		{Name: "API_TOKEN", Required: true, Delivery: "env"},
	}
	// The workspace env file is what the admitted credential store writes.
	if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte("CLIENT_JSON={\"installed\":{\"client_id\":\"x\"}}\nAPI_TOKEN=tok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	envPath, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != "/run/mcp-secrets/client.json" || !mounts[0].ReadOnly {
		t.Fatalf("file credential not mounted at reviewed target: %+v", mounts)
	}
	if mounts[0].Source == "" || !strings.HasPrefix(mounts[0].Source, workspace) {
		t.Fatalf("file mount source outside workspace: %+v", mounts[0])
	}
	envBody, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envBody), "CLIENT_JSON=/run/mcp-secrets/client.json") {
		t.Fatalf("env does not carry container path: %q", envBody)
	}
	if !strings.Contains(string(envBody), "API_TOKEN=tok") {
		t.Fatalf("env credential lost: %q", envBody)
	}
	// The plaintext JSON must not ride in the env file.
	if strings.Contains(string(envBody), "client_id") {
		t.Fatalf("file credential leaked into env: %q", envBody)
	}
}

// Credential hints carry the reviewed delivery so the form renders a file
// picker, not a password box, for file credentials.
func TestCredentialHintsPropagateFileDelivery(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.Credentials = []api.CredentialInput{
		{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/mcp-secrets/client.json"},
	}
	hints := credentialHints(definition)
	if len(hints) != 1 {
		t.Fatalf("hints=%+v", hints)
	}
	if hints[0].Delivery != "file" || hints[0].Target != "/run/mcp-secrets/client.json" {
		t.Fatalf("file delivery dropped in hint: %+v", hints[0])
	}
}

// The loopback form must accept a file upload for a file-delivery field —
// the value stored is the uploaded content, not a pasted path.
func TestCredentialFormAcceptsFileField(t *testing.T) {
	markup := credentialFieldMarkup(api.CredentialHint{Name: "CLIENT_JSON", Type: "json", Secret: true, Delivery: "file", Target: "/run/mcp-secrets/client.json"}, api.ConnectionField{}, "", locale.LangRU)
	if !strings.Contains(markup, `type="file"`) {
		t.Fatalf("file delivery did not render a file input: %s", markup)
	}
}

// A file-delivery field submitted as a multipart upload stores the uploaded
// content, ready for the runtime to mount at the reviewed target.
func TestCredentialFormStoresUploadedFileContent(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "files.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := statefulContainerDefinition()
	definition.DefinitionID = "file-mcp"
	definition.Credentials = []api.CredentialInput{{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/mcp-secrets/client.json"}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now, FormOrigin: "http://127.0.0.1:8090"}
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	id := prepared["onboarding_id"].(string)
	onboarding := mustOnboarding(t, store, id)
	if onboarding.Phase != api.PhaseAwaitingCreds {
		t.Fatalf("phase=%s", onboarding.Phase)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("CLIENT_JSON", "client.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(`{"installed":{"client_id":"x"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/credentials/"+id+"?nonce="+onboarding.FormNonce, &body)
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	gateway.serveCredentials(rec, req)

	stored := mustOnboarding(t, store, id)
	if stored.Locator == "" {
		t.Fatalf("file upload stored no credential: status=%d body=%s", rec.Code, rec.Body.String())
	}
	values, err := secrets.Get(stored.Locator, stored.CredentialOwner)
	if err != nil {
		t.Fatal(err)
	}
	if values["CLIENT_JSON"] != `{"installed":{"client_id":"x"}}` {
		t.Fatalf("stored value is not file content: %q", values["CLIENT_JSON"])
	}
}

// ---------- S1.3: file credentials + unsupported contracts (#105) ----------

func TestConnectionRecipePreservesCredentialDelivery(t *testing.T) {
	recipe := api.ConnectionRecipe{Fields: []api.ConnectionField{
		{Name: "CLIENT_JSON", Required: true, Secret: true, Delivery: "file", Target: "/run/secrets/CLIENT_JSON"},
		{Name: "API_TOKEN", Required: true, Secret: true, Delivery: "env", Target: "API_TOKEN"},
	}}
	inputs := recipe.CredentialInputs()
	if len(inputs) != 2 {
		t.Fatalf("inputs=%+v", inputs)
	}
	byName := map[string]api.CredentialInput{}
	for _, input := range inputs {
		byName[input.Name] = input
	}
	if byName["CLIENT_JSON"].Delivery != "file" || byName["CLIENT_JSON"].Target != "/run/secrets/CLIENT_JSON" {
		t.Fatalf("file delivery dropped in projection: %+v", byName["CLIENT_JSON"])
	}
	// An env target that restates the variable name projects empty — the env
	// var is the destination already.
	if byName["API_TOKEN"].Delivery != "env" || byName["API_TOKEN"].Target != "" {
		t.Fatalf("env delivery mangled: %+v", byName["API_TOKEN"])
	}
}

func TestFileAndUnsupportedCredentialContracts(t *testing.T) {
	base := func() api.ToolDefinition {
		d := statefulContainerDefinition()
		d.Credentials = []api.CredentialInput{{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/secrets/client.json"}}
		return d
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("file credential on container transport rejected: %v", err)
	}

	// File delivery mounts inside the container; targets must be real
	// absolute paths, never host or relative ones.
	for _, target := range []string{"client.json", "../etc/passwd", "C:\\temp\\x", ""} {
		d := base()
		d.Credentials[0].Target = target
		if err := d.Validate(); err == nil {
			t.Fatalf("unsafe file target %q accepted", target)
		}
	}

	// A remote MCP cannot mount files; a bounded CLI cannot bind-mount
	// either — file delivery is container-mcp only.
	remote := remoteDefinition()
	remote.Credentials = []api.CredentialInput{{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/secrets/client.json"}}
	if err := remote.Validate(); !errors.Is(err, api.ErrUnsupportedCredentialContract) {
		t.Fatalf("file delivery on remote-mcp: %v", err)
	}

	// Delivery names the platform does not run fail with the contract code,
	// not a generic invalid.
	d := base()
	d.Credentials[0].Delivery = "grpc"
	if err := d.Validate(); !errors.Is(err, api.ErrUnsupportedCredentialContract) {
		t.Fatalf("unknown delivery code: %v", err)
	}
	// OAuth as a static credential field is an auth contract the form cannot
	// satisfy; it belongs to a reviewed flow, not the value store.
	d.Credentials[0].Delivery = "oauth"
	d.Credentials[0].Target = ""
	if err := d.Validate(); !errors.Is(err, api.ErrUnsupportedAuth) {
		t.Fatalf("oauth field code: %v", err)
	}
	// Same codes through the recipe contract validator.
	if err := (api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "X_TOKEN", Required: true, Delivery: "carrier-pigeon"}}}).Validate(); !errors.Is(err, api.ErrUnsupportedCredentialContract) {
		t.Fatalf("recipe unknown delivery: %v", err)
	}
}

func TestStatusSurfacesErrorCode(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "code-1"})
	if err != nil {
		t.Fatal(err)
	}
	onboardingID := prepared["onboarding_id"].(string)

	// A wrapped sentinel on the cause sets the persisted code.
	if err := control.persistFailure(mustOnboarding(t, store, onboardingID), "ambiguous MCP entrypoint; select literal argv", fmt.Errorf("review: %w", api.ErrAmbiguousEntrypoint)); err == nil {
		t.Fatal("persistFailure swallowed the cause")
	}
	body, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if body["error_code"] != "ambiguous_entrypoint" {
		t.Fatalf("status body missing error_code: %v", body)
	}
	if body["error"] == "" {
		t.Fatalf("status body lost human error: %v", body)
	}
	// A non-coded failure keeps the field absent, never fabricated.
	if err := control.persistFailure(mustOnboarding(t, store, onboardingID), "plain failure", fmt.Errorf("plain: %w", api.ErrStale)); err == nil {
		t.Fatal("persistFailure swallowed the cause")
	}
	body, err = control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := body["error_code"]; present {
		t.Fatalf("uncoded failure fabricated a code: %v", body["error_code"])
	}
}
