package api

import (
	"strings"
	"testing"
)

// CHG-0128 S1.1: ledger + recipe types with a canonical content digest.

func TestLedgerEntryCanonicalDigest(t *testing.T) {
	recipe := Recipe{
		Subject: RecipeSubject{Kind: SubjectContainerMCP, Ref: "https://github.com/acme/x@abc123"},
		Launch:  LaunchRecipe{Transport: ContainerMCP, Artifact: "img@sha256:aaa", Entrypoint: []string{"./server"}, Mounts: []Mount{{Source: "s1", Target: "/b"}, {Source: "s0", Target: "/a"}}},
		Credentials: ConnectionRecipe{Fields: []ConnectionField{
			{Name: "B_TOKEN", Required: true, Secret: true, Delivery: "env"},
			{Name: "A_TOKEN", Required: true, Secret: true, Delivery: "env"},
		}},
		Egress: []string{"b.example", "a.example"},
		Trust:  TrustT1,
	}
	first := recipe.Digest()
	// Shuffled slices must produce the identical digest.
	shuffled := recipe
	shuffled.Credentials.Fields = []ConnectionField{recipe.Credentials.Fields[1], recipe.Credentials.Fields[0]}
	shuffled.Egress = []string{"a.example", "b.example"}
	shuffled.Launch.Mounts = []Mount{recipe.Launch.Mounts[1], recipe.Launch.Mounts[0]}
	if shuffled.Digest() != first {
		t.Fatal("digest changes with field ordering — not canonical")
	}
	mutated := recipe
	mutated.Launch.Artifact = "img@sha256:bbb"
	if mutated.Digest() == first {
		t.Fatal("digest did not change on claim mutation")
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest=%q", first)
	}
}

func TestRecipeContainsNoSecrets(t *testing.T) {
	leaking := Recipe{
		Subject: RecipeSubject{Kind: SubjectContainerMCP, Ref: "repo@abc"},
		Launch:  LaunchRecipe{Transport: ContainerMCP, Environment: map[string]string{"UPSTREAM": "ghp_0123456789abcdef0123456789abcdef1234"}},
		Trust:   TrustT2,
	}
	if err := leaking.Validate(); err == nil {
		t.Fatal("recipe carrying a literal token accepted")
	}
	clean := Recipe{
		Subject:     RecipeSubject{Kind: SubjectContainerMCP, Ref: "repo@abc"},
		Launch:      LaunchRecipe{Transport: ContainerMCP, Environment: map[string]string{"MODE": "prod"}},
		Credentials: ConnectionRecipe{Fields: []ConnectionField{{Name: "UPSTREAM_TOKEN", Required: true, Secret: true, Delivery: "env"}}},
		Trust:       TrustT2,
	}
	if err := clean.Validate(); err != nil {
		t.Fatalf("clean recipe rejected: %v", err)
	}
}
