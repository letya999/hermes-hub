package identityhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestOIDCLinksBindExactSubjectAndChannelOwnership(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	const issuer = "https://issuer.example"
	_, err = r.ImportLegacyConfiguration(ctx, LegacyConfiguration{Name: "oidc", Organization: "personal", Issuer: issuer, Users: []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}, OIDCSubject: "subject-a"}, {ID: "bob", Enabled: true, TelegramIDs: []int64{8}, OIDCSubject: "subject-b"}}})
	if err != nil {
		t.Fatal(err)
	}
	proof := contract.OIDCLink{Channel: contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}, PrincipalID: "alice", Issuer: issuer, Subject: "subject-a"}
	if err := r.VerifyOIDC(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyOIDC(ctx, proof); err != nil {
		t.Fatalf("identical proof not idempotent: %v", err)
	}
	proof.Channel.ID = "9"
	if err := r.VerifyOIDC(ctx, proof); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("unmapped channel invented a verified owner")
	}
	proof.PrincipalID = ""
	if err := r.VerifyOIDC(ctx, proof); err != nil {
		t.Fatalf("provisioned subject enrolment: %v", err)
	}
	links, err := r.Bindings(ctx, "alice")
	if err != nil || len(links) != 2 {
		t.Fatal("verified links missing")
	}
	for _, link := range links {
		if link.Issuer != issuer || link.Subject != "subject-a" {
			t.Fatal("provenance changed")
		}
	}
	if err := r.Unlink(ctx, contract.UnlinkRequest{Channel: proof.Channel, PrincipalID: "bob"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal("foreign owner revoked link")
	}
	proof.Channel.ID = "8"
	if err := r.VerifyOIDC(ctx, proof); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("foreign channel acquired: %v", err)
	}
	proof.PrincipalID = "bob"
	if err := r.VerifyOIDC(ctx, proof); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("subject acquired by another owner: %v", err)
	}
	if err := r.Unlink(ctx, contract.UnlinkRequest{Channel: contract.ChannelIdentity{Channel: contract.Telegram, ID: "9"}, PrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "9"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal("revoked OIDC link resolved")
	}
}

func TestOIDCLegacyImportRollsBackForVerifiedLinkToDisabledHuman(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "existing", Enabled: true, TelegramIDs: []int64{7}})
	ctx := context.Background()
	const issuer = "https://issuer.example"
	input := LegacyConfiguration{
		Name: "disabled-proof", Organization: "personal", Issuer: issuer,
		Users: []commodel.User{{ID: "alice", Enabled: true}, {ID: "retired"}},
		VerifiedLinks: []contract.OIDCLink{
			{Channel: contract.ChannelIdentity{Channel: contract.Slack, ID: "t1/u1"}, PrincipalID: "alice", Issuer: issuer, Subject: "subject-a"},
			{Channel: contract.ChannelIdentity{Channel: contract.Slack, ID: "t1/u2"}, PrincipalID: "retired", Issuer: issuer, Subject: "subject-b"},
		},
	}
	if _, err := r.ImportLegacyConfiguration(ctx, input); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("disabled owner in verified link import: %v", err)
	}
	if _, err := r.Principal(ctx, "alice"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("failed transaction created an active human: %v", err)
	}
	if _, err := r.Resolve(ctx, input.VerifiedLinks[0].Channel); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("failed transaction created a verified link: %v", err)
	}
	input.VerifiedLinks = input.VerifiedLinks[:1]
	if _, err := r.ImportLegacyConfiguration(ctx, input); err != nil {
		t.Fatalf("corrected import could not retry: %v", err)
	}
}

func TestOIDCProofFitsEnvelopeProvenanceLimit(t *testing.T) {
	user := commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}
	r, _ := fixture(t, user)
	ctx := context.Background()
	const base = "https://issuer.example/"
	proof := contract.OIDCLink{
		Channel: contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}, PrincipalID: user.ID,
		Issuer: base + strings.Repeat("a", identity.MaxProvenanceBytes-len(base)), Subject: strings.Repeat("b", identity.MaxProvenanceBytes),
	}
	longIssuer, longSubject := proof, proof
	longIssuer.Issuer += "a"
	longSubject.Subject += "b"
	for _, invalid := range []contract.OIDCLink{longIssuer, longSubject} {
		if err := r.VerifyOIDC(ctx, invalid); !errors.Is(err, contract.ErrInvalid) {
			t.Fatalf("oversized proof: %v", err)
		}
	}
	if err := r.VerifyOIDC(ctx, proof); err != nil {
		t.Fatal(err)
	}
	_, binding, err := contract.ResolveChannel(ctx, r, proof.Channel)
	if err != nil {
		t.Fatal(err)
	}
	user.SSOIssuer, user.SSOSubject = binding.Issuer, binding.Subject
	env := user.Envelope(7)
	if err := env.Validate(env.PrincipalID, env.ContextID, env.RuntimeID, env.PolicyVersion); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCLegacyImportPreservesProvenanceAtomically(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	proof := contract.OIDCLink{Channel: contract.ChannelIdentity{Channel: contract.Slack, ID: "t1/u1"}, PrincipalID: "alice", Issuer: "https://issuer.example", Subject: "opaque-subject"}
	input := LegacyConfiguration{Name: "legacy", Organization: "personal", Issuer: proof.Issuer, Users: []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}}, VerifiedLinks: []contract.OIDCLink{proof}}
	if _, err := r.ImportLegacyConfiguration(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ImportLegacyConfiguration(ctx, input); err != nil {
		t.Fatal("identical OIDC import refused")
	}
	links, err := r.Bindings(ctx, "alice")
	if err != nil || len(links) != 2 {
		t.Fatal("legacy links missing")
	}
	for _, link := range links {
		if link.Channel.Channel == contract.Slack && (link.Issuer != proof.Issuer || link.Subject != proof.Subject) {
			t.Fatal("legacy provenance lost")
		}
		if link.Channel.Channel == contract.Telegram && link.Issuer != "" {
			t.Fatal("provenance spread to unrelated channel")
		}
	}
	input.Name = "foreign"
	input.Users = []commodel.User{{ID: "carol", Enabled: true}}
	input.VerifiedLinks[0].PrincipalID = "carol"
	if _, err := r.ImportLegacyConfiguration(ctx, input); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("duplicate subject imported: %v", err)
	}
	if _, err := r.Principal(ctx, "carol"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal("failed import left a human")
	}
}

func TestIdentityLinkContractRejectsReadTokenAndOrigin(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}})
	server := httptest.NewServer(contract.LinkHandler(r, "ingress-token"))
	defer server.Close()
	proof := contract.OIDCLink{Channel: contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}, PrincipalID: "alice", Issuer: "https://issuer.example", Subject: "subject"}
	if err := contract.NewLinkHTTPClient(server.URL, "read-token", server.Client()).VerifyOIDC(context.Background(), proof); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("read credential asserted OIDC proof")
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/identity/links/oidc", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer ingress-token")
	req.Header.Set("Origin", "https://browser.example")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("browser-origin proof accepted")
	}
	if err := contract.NewLinkHTTPClient(server.URL, "ingress-token", server.Client()).VerifyOIDC(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
}
