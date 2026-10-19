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
