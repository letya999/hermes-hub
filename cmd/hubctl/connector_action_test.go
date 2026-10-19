package main

import (
	"context"
	"io"
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
		Phase: api.PhaseAwaitingConfirm, Revision: 1,
		ConfirmationNonce: "nonce-1", ConfirmationExpires: time.Now().Add(time.Hour),
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
	// --confirm finishes the flow for scripts: the onboarding is confirmed,
	// and the link line still leads the output for the same script surface.
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"link"}, append(base, "--origin", "http://127.0.0.1:8090", "--confirm")...))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "http://127.0.0.1:8090/confirm/onb-1?t=nonce-1") || !strings.Contains(out, api.PhaseConfirmed) {
		t.Fatalf("link --confirm output=%q", out)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	onboarding, err := reloaded.OnboardingFor(auth, "onb-1")
	if err != nil {
		t.Fatal(err)
	}
	if onboarding.Phase != api.PhaseConfirmed {
		t.Fatalf("phase after --confirm: %q", onboarding.Phase)
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
		DefinitionID: "wri", DefinitionVersion: "1.0.0", Phase: api.PhaseEnabled,
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
	if err != nil || !strings.Contains(out, `"state":"done"`) || !strings.Contains(out, "wri") {
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
	if err != nil || onboarding.Phase != api.PhaseDisabled {
		t.Fatalf("phase after CLI disable: %v %q", err, onboarding.Phase)
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
	if err != nil || !strings.Contains(out, `"link_sent":true`) {
		t.Fatalf("reauth output=%q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runConnector(ctx, append([]string{"remove"}, append(base, "--target", "wri")...))
	})
	if err != nil || !strings.Contains(out, `"link_sent":true`) {
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
	if err != nil || !strings.Contains(out, `"link_sent":true`) {
		t.Fatalf("allow-writes on output=%q err=%v", out, err)
	}
}
