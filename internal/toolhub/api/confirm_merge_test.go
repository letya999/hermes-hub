package api

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuthorityArgumentsAndDigests(t *testing.T) {
	// authority selectors are denied unless the tool schema declares them
	tool := ToolSpec{Name: "t", Effect: ReadEffect, InputSchema: []byte(`{"properties":{"owner":{"type":"string"}}}`)}
	if err := RejectToolAuthorityArguments(tool, map[string]any{"owner": "acme"}); err != nil {
		t.Fatalf("declared owner: %v", err)
	}
	for key := range map[string]any{
		"principal_id": nil, "owner_id": nil, "credential_ref": nil, "backend_url": nil, "grant_id": nil,
	} {
		if err := RejectToolAuthorityArguments(tool, map[string]any{key: "x"}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if err := RejectAuthorityArguments(map[string]any{"API_TOKEN": "secret"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("credential value argument accepted")
	}

	// digests are canonical and confirmable
	def := readyDefinition("sha256:" + strings.Repeat("ab", 32))
	if got := DefinitionDigest(def); !strings.HasPrefix(got, "sha256:") || len(got) != 71 {
		t.Fatalf("definition digest: %q", got)
	}
	policy := CapabilityPolicy{PolicyID: "pol-1", IssuedBy: "op"}
	if err := Confirm(&policy, "operator", time.Now()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if policy.Confirmation == nil || policy.Confirmation.IssuedBy != "operator" {
		t.Fatalf("confirmation: %+v", policy.Confirmation)
	}
	// stamped and unstamped records digest identically
	if ConfirmationDigest(policy) != policy.Confirmation.Digest {
		t.Fatal("confirmation digest drifted after stamping")
	}
	if err := Confirm(&policy, "model", time.Now()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("model issuer: %v", err)
	}
	other := struct{}{}
	if err := Confirm(&other, "operator", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported record: %v", err)
	}

	// artifact review digests and entrypoint alignment
	imported := ImportedArtifact{
		Definition: def,
		Recipe:     ArtifactRecipe{Entrypoint: []string{"/bin/server", "--port", "8080"}},
	}
	digest, err := ImportedReviewDigest(imported)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("review digest: %q %v", digest, err)
	}
	if rd, err := RecipeDigest(imported.Recipe); err != nil || rd == "" {
		t.Fatalf("recipe digest: %v", err)
	}
	AlignImportedEntrypoint(&imported)
	if imported.Definition.Source.Command != "/bin/server" || len(imported.Definition.Source.Args) != 2 {
		t.Fatalf("entrypoint: %+v", imported.Definition.Source)
	}
	if err := ValidateImportedReview(imported); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing review digest: %v", err)
	}
}
