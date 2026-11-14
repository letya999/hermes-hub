package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func aliceAuth() identity.Envelope {
	return identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "connector", DeliveryTargetID: "connector", PolicyVersion: "policy-1"}
}

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		defer reader.Close()
		body, _ := io.ReadAll(reader)
		done <- string(body)
	}()
	runErr := run()
	os.Stdout = old
	writer.Close()
	return strings.TrimSpace(<-done), runErr
}

// CHG-0129 S2.7: hubctl surfaces the human action link for scripts, and
// --confirm lets a script finish an awaiting-confirmation onboarding without
// a browser round-trip.
func TestConnectorLinkAndConfirm(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "registry.json")
	keyFile := filepath.Join(t.TempDir(), "key")
	secretFile := filepath.Join(t.TempDir(), "store.enc")
	definition := api.CLICatalogDefinitions()[0]
	registry := toolhub.NewStore()
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutOnboarding(api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-1", Mode: api.OnboardingCatalog,
		PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", PolicyVersion: "policy-1",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Stage: api.StagePending, Revision: 1,
		Gate: &api.Gate{Kind: api.GateConfirmSetup, Nonce: "nonce-1", Expires: time.Now().Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	grant := api.Grant{
		Schema: api.SchemaVersion, GrantID: "grant-confirm-1", Kind: api.GrantControlOperation,
		PrincipalID: "alice", Operation: "confirm", IssuedBy: "operator", Status: api.ActiveStatus, Revision: 1,
	}
	grant.Confirmation = &api.Confirmation{Digest: api.ConfirmationDigest(grant), IssuedBy: "operator", ConfirmedAt: time.Now()}
	if err := registry.PutGrant(grant); err != nil {
		t.Fatal(err)
	}
	statusGrant := grant
	statusGrant.GrantID, statusGrant.Operation = "grant-status-1", "status"
	statusGrant.Confirmation = &api.Confirmation{Digest: api.ConfirmationDigest(statusGrant), IssuedBy: "operator", ConfirmedAt: time.Now()}
	if err := registry.PutGrant(statusGrant); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	base := []string{"--user", "alice", "--toolhub-store", storePath, "--store", secretFile, "--key-file", keyFile, "--onboarding", "onb-1"}
	// link prints the one-time human action URL for the pending onboarding.
	out, err := captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"link"}, append(base, "--origin", "http://127.0.0.1:8090")...))
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "http://127.0.0.1:8090/confirm/onb-1?t=nonce-1" {
		t.Fatalf("link=%q", out)
	}
	onboarding, err := registry.OnboardingFor(aliceAuth(), "onb-1")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.ConfirmationUsed = true
	onboarding.Revision++
	if err := registry.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return runConnector(ctx, append([]string{"link"}, base...)) }); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("spent link: %v", err)
	}
	registry, err = toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err = registry.OnboardingFor(aliceAuth(), "onb-1")
	if err != nil || !onboarding.ConfirmationUsed || onboarding.GateNonce(api.GateConfirmSetup) != "nonce-1" {
		t.Fatal("link command reset consumed consent")
	}
	// A separate pending action tests expiry without reusing consumed consent.
	// It pins a different definition — the load-time slot dedup (same
	// principal+definition) would otherwise tombstone it behind onb-1.
	definition2 := api.CLICatalogDefinitions()[1]
	if err := registry.RegisterDefinition(definition2); err != nil {
		t.Fatal(err)
	}
	onboarding.OnboardingID, onboarding.Revision, onboarding.ConfirmationUsed = "onb-2", 1, false
	onboarding.DefinitionID, onboarding.DefinitionVersion = definition2.DefinitionID, definition2.Version
	onboarding.ArmGate(api.GateConfirmSetup, "nonce-1", time.Now().Add(-time.Minute))
	if err := registry.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	base[len(base)-1] = "onb-2"
	if _, err := captureStdout(t, func() error { return runConnector(ctx, append([]string{"link"}, append(base, "--confirm")...)) }); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("expired link was silently renewed: %v", err)
	}
	onboarding.ArmGate(api.GateConfirmSetup, "nonce-fresh", time.Now().Add(time.Hour))
	onboarding.Revision++
	if err := registry.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"link"}, append(base, "--origin", "http://127.0.0.1:8090", "--confirm")...))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "http://127.0.0.1:8090/confirm/onb-2?t=nonce-fresh") || !strings.Contains(out, api.PhaseConfirmed) {
		t.Fatalf("live link --confirm output=%q", out)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err = reloaded.OnboardingFor(aliceAuth(), "onb-2")
	if err != nil || !onboarding.ConfirmationUsed {
		t.Fatalf("live confirmation phase=%s err=%v", onboarding.NoticePhase(), err)
	}

}

// CHG-0132 S5.3: hubctl connector import parses a deterministic T1 ref into
// a recipe draft, prints the review digest, and writes only under --confirm.
func TestConnectorImportRef(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "registry.json")
	keyFile := filepath.Join(t.TempDir(), "key")
	secretFile := filepath.Join(t.TempDir(), "store.enc")
	imageDigest := "sha256:" + strings.Repeat("e", 64)
	ref := "docker:mcp/notion@" + imageDigest
	draft, err := api.ParseImportRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	reviewDigest, err := api.ResolutionDigest(draft)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"--user", "alice", "--toolhub-store", storePath, "--store", secretFile, "--key-file", keyFile}
	// No --confirm: the operator sees the digest but nothing is stored.
	err = runConnector(ctx, append(append([]string{"import"}, base...), ref))
	if err == nil || !strings.Contains(err.Error(), reviewDigest) {
		t.Fatalf("import without --confirm: %v", err)
	}
	if _, statErr := os.Stat(storePath); !os.IsNotExist(statErr) {
		t.Fatal("refused import still wrote the store")
	}
	out, err := captureStdout(t, func() error {
		return runConnector(ctx, append(append([]string{"import"}, base...), "--confirm", ref))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, reviewDigest) {
		t.Fatalf("confirmed import output=%q", out)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reloaded.Recipe(reviewDigest)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Trust != api.TrustT1 || stored.Launch.Digest != imageDigest {
		t.Fatalf("stored recipe=%+v", stored)
	}
	// Determinism: an identical ref always produces the same digest.
	again, err := api.ParseImportRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if digest2, err := api.ResolutionDigest(again); err != nil || digest2 != reviewDigest {
		t.Fatalf("import not deterministic: %q vs %q", digest2, reviewDigest)
	}
}

// S5.3: every advertised scheme parses into a T1 draft with a source ledger
// entry; malformed refs fail closed.
func TestParseImportRefSchemes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	commit := strings.Repeat("f", 40)
	cases := []struct {
		ref       string
		repo      string
		registry  string
		artifact  string
		launchDig string
	}{
		{"mcp-registry:notion@1.2.3", "", "mcp-registry", "", ""},
		{"docker:mcp/github@" + digest, "", "", "mcp/github", digest},
		{"toolhive:github", "", "toolhive", "", ""},
		{"mcpb:acme/servers@v1.0.0", "https://github.com/acme/servers", "", "", ""},
		{"hermes:search@" + commit, "https://github.com/nousresearch/hermes-agent", "", "", ""},
		{"aqua:cli/cli@v2.0.0", "", "aqua", "", ""},
	}
	for _, tc := range cases {
		draft, err := api.ParseImportRef(tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if draft.Trust != api.TrustT1 || draft.State != "draft" || len(draft.Ledger) == 0 {
			t.Fatalf("%s: draft=%+v", tc.ref, draft)
		}
		if draft.Source.Repository != tc.repo || draft.Source.PackageRegistry != tc.registry {
			t.Fatalf("%s: source=%+v", tc.ref, draft.Source)
		}
		if draft.Launch.Artifact != tc.artifact || draft.Launch.Digest != tc.launchDig {
			t.Fatalf("%s: launch=%+v", tc.ref, draft.Launch)
		}
	}
	for _, bad := range []string{"", "bogus:x", "docker:other/x@" + digest, "docker:mcp/x@latest", "hermes:x@abc", "mcp-registry:x", "mcpb:onlyrepo@tag"} {
		if _, err := api.ParseImportRef(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// CHG-0130 S3.7: hubctl connector <verb> runs the same facade the agent
// surface calls — the connectors capability bundle admits the verbs for the
// operator-selected profile.
func TestConnectorFacadeVerbsCLI(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "registry.json")
	keyFile := filepath.Join(t.TempDir(), "key")
	secretFile := filepath.Join(t.TempDir(), "store.enc")
	registry := toolhub.NewStore()
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example",
		Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(),
		Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&policy, "", policy.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default",
		PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", Environment: "dev", Generation: 1,
		PolicyVersion: "policy-1", PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "fixture",
		Status: api.ActiveStatus, Connectors: api.ConnectorsPersonal}
	if err := api.Confirm(&profile, "", profile.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "wri", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://wri.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Rationale: "user-owned"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"wri.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "wri",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: "wri",
		Revision: 1, Status: api.ActiveStatus}
	if err := registry.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice",
		DefinitionID: "wri", DefinitionVersion: "1.0.0", ConnectionID: connection.ConnectionID, ConnectionRevision: 1,
		PolicyVersion: "policy-1", WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := registry.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutOnboarding(api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-wri", Mode: api.OnboardingSelfInstall,
		PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", PolicyVersion: "policy-1",
		DefinitionID: "wri", DefinitionVersion: "1.0.0", Stage: api.StageLive,
		BindingID: binding.ToolBindingID, ConnectionID: connection.ConnectionID,
		Revision: 1, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	base := []string{"--user", "alice", "--toolhub-store", storePath, "--store", secretFile, "--key-file", keyFile,
		"--profile", "alice-default", "--env", "dev", "--generation", "1"}
	out, err := captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"list"}, base...))
	})
	if err != nil || !strings.Contains(out, `"state":"done"`) || !strings.Contains(out, "onb-wri") {
		t.Fatalf("list output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"status"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"state":"ready"`) || !strings.Contains(out, "wri") {
		t.Fatalf("status output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"disable"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"operation_id"`) || !strings.Contains(out, `"state":"done"`) {
		t.Fatalf("disable output=%q err=%v", out, err)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err := reloaded.OnboardingFor(aliceAuth(), "onb-wri")
	if err != nil || onboarding.Stage != api.StageLive {
		t.Fatalf("stage after CLI disable: %v %q", err, onboarding.Stage)
	}
	disabledOut, err := captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"status"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(disabledOut, `"state":"paused"`) {
		t.Fatalf("status after CLI disable: %q %v", disabledOut, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"enable"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"state":"done"`) {
		t.Fatalf("enable output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"search"}, append(base, "--query", "github")...))
	})
	if err != nil || !strings.Contains(out, `"candidates"`) {
		t.Fatalf("search output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"reauth"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"state":"waiting_you"`) || strings.Contains(out, `"link_sent"`) {
		t.Fatalf("reauth output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"remove"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"state":"waiting_you"`) || strings.Contains(out, `"link_sent"`) {
		t.Fatalf("remove output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"install"}, append(base, "--target", "github")...))
	})
	if err != nil || !strings.Contains(out, `"operation_id"`) {
		t.Fatalf("install output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"allow-writes"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"state":"done"`) {
		t.Fatalf("allow-writes off output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"allow-writes"}, append(base, "--target", "wri", "--on")...))
	})
	if err != nil || !strings.Contains(out, `"state":"waiting_you"`) || strings.Contains(out, `"link_sent"`) {
		t.Fatalf("allow-writes on output=%q err=%v", out, err)
	}
}

// --service carries facade verbs through /v1/connectors:invoke — the same
// admission path the agent tools use, never a parallel file write.
func TestConnectorServiceCallPostsInvoke(t *testing.T) {
	var gotTool, gotAuth string
	var gotArgs map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/connectors:invoke" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body struct {
			Tool string         `json:"tool"`
			Args map[string]any `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTool, gotArgs = body.Tool, body.Args
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "done", "summary": "1 connector(s)"})
	}))
	defer server.Close()
	result, err := connectorServiceCall(context.Background(), server.URL, "svc-token", "connector_list", map[string]any{"target": "github"})
	if err != nil {
		t.Fatal(err)
	}
	if gotTool != "connector_list" || gotAuth != "Bearer svc-token" || gotArgs["target"] != "github" {
		t.Fatalf("invoke payload: tool=%q auth=%q args=%v", gotTool, gotAuth, gotArgs)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil || decoded["state"] != "done" {
		t.Fatalf("invoke result: %v %s", err, result)
	}
}

func TestConnectorServiceTokenWalksUpToTokensFile(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "runtime", "toolhub", "store.db")
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := map[string]identity.Envelope{
		"token-alice-000000000000000000000000": {Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", ConversationID: "conv", DeliveryTargetID: "conv", PolicyVersion: "policy-1"},
		"token-bob-00000000000000000000000000": {Schema: identity.Schema, PrincipalID: "bob", ExternalIdentityID: "bob", ContextID: "bob", RuntimeID: "runtime", ConversationID: "conv", DeliveryTargetID: "conv", PolicyVersion: "policy-1"},
	}
	body, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(root, "toolhub-tokens.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := connectorServiceToken("", store, "alice")
	if err != nil || token != "token-alice-000000000000000000000000" {
		t.Fatalf("walk-up token: %q %v", token, err)
	}
	if _, err := connectorServiceToken("", store, "carol"); err == nil {
		t.Fatal("unenrolled principal must fail loudly, not guess a token")
	}
	if got, err := connectorServiceToken("explicit", store, "alice"); err != nil || got != "explicit" {
		t.Fatalf("explicit --token wins: %q %v", got, err)
	}
}
