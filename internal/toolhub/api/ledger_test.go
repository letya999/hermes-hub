package api

import (
	"errors"
	"slices"
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

func boolp(v bool) *bool { return &v }

func credEntry(field string, required bool, rank Rank, source string) LedgerEntry {
	return LedgerEntry{
		Claim:  Claim{Field: field, Required: boolp(required), Secret: boolp(true), Delivery: "env"},
		Source: source,
		Rank:   rank,
	}
}

func TestLedgerR2OverridesR6(t *testing.T) {
	entries := []LedgerEntry{
		credEntry("API_TOKEN", true, RankR6, ".env.example"),
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(false), Secret: boolp(true), Delivery: "env"}, Source: "mcp-registry", Rank: RankR2},
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range recipe.Credentials.Fields {
		if f.Name == "API_TOKEN" && f.Required {
			t.Fatal("R6 requiredness beat the R2 registry claim")
		}
	}
}

func TestLedgerSameRankAmbiguous(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(true), Delivery: "env"}, Source: "registry-a", Rank: RankR2},
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(false), Delivery: "env"}, Source: "registry-b", Rank: RankR2},
	}
	_, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if !errors.Is(err, ErrAmbiguousCredentialContract) {
		t.Fatalf("same-rank disagreement: %v", err)
	}
}

func TestLedgerR1ResolvesAmbiguity(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(true), Delivery: "env"}, Source: "registry-a", Rank: RankR2},
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(false), Delivery: "env"}, Source: "registry-b", Rank: RankR2},
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(false), Secret: boolp(true), Delivery: "env"}, Source: "operator", Rank: RankR1},
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if err != nil {
		t.Fatalf("R1 must close the same-rank conflict: %v", err)
	}
	for _, f := range recipe.Credentials.Fields {
		if f.Name == "API_TOKEN" && f.Required {
			t.Fatal("R1 optional claim lost")
		}
	}
}

func TestLedgerLowerRankCannotRequire(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(false), Delivery: "env"}, Source: "mcp-registry", Rank: RankR2},
		credEntry("API_TOKEN", true, RankR6, ".env.example"),
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range recipe.Credentials.Fields {
		if f.Name == "API_TOKEN" && f.Required {
			t.Fatal("lower rank strengthened required")
		}
	}
}

func TestLedgerR6OnlyNeverRequired(t *testing.T) {
	entries := []LedgerEntry{
		credEntry("FOO_TOKEN", true, RankR6, ".env.example"),
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.Credentials.Fields) != 1 {
		t.Fatalf("fields=%+v", recipe.Credentials.Fields)
	}
	f := recipe.Credentials.Fields[0]
	if f.Required || !f.Unverified {
		t.Fatalf("R6-only field must be optional and unverified: %+v", f)
	}
	if recipe.Trust != TrustT2 {
		t.Fatalf("trust=%s, R6-only contract must be T2", recipe.Trust)
	}
}

func TestLedgerTrustTiers(t *testing.T) {
	t0, err := ResolveLedger(RecipeSubject{Kind: SubjectContainerMCP, Ref: "r@1"}, []LedgerEntry{
		{Claim: Claim{Attribute: "artifact", Value: `"img@sha256:x"`}, Source: "hub-catalog", Rank: RankL0},
		{Claim: Claim{Field: "T", Required: boolp(true), Delivery: "env"}, Source: "hub-catalog", Rank: RankR0},
	})
	if err != nil || t0.Trust != TrustT0 {
		t.Fatalf("T0: %v %s", err, t0.Trust)
	}
	t1, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, []LedgerEntry{
		{Claim: Claim{Attribute: "endpoint", Value: `"https://x.example/mcp"`}, Source: "mcp-registry", Rank: RankL3},
		{Claim: Claim{Field: "T", Required: boolp(true), Delivery: "env"}, Source: "toolhive", Rank: RankR4},
	})
	if err != nil || t1.Trust != TrustT1 {
		t.Fatalf("T1: %v %s", err, t1.Trust)
	}
}

func TestLedgerLaunchAmbiguous(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Attribute: "endpoint", Value: `"https://a.example/mcp"`}, Source: "registry-a", Rank: RankL3},
		{Claim: Claim{Attribute: "endpoint", Value: `"https://b.example/mcp"`}, Source: "registry-b", Rank: RankL3},
	}
	_, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if !errors.Is(err, ErrAmbiguousEntrypoint) {
		t.Fatalf("same-rank launch disagreement: %v", err)
	}
}

func TestLedgerIgnoredNeverInfluences(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Field: "EVIL", Required: boolp(true), Delivery: "env"}, Source: "forged-catalog", Rank: RankR0, Ignored: true},
		{Claim: Claim{Field: "T", Required: boolp(true), Delivery: "env"}, Source: "mcp-registry", Rank: RankR2},
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range recipe.Credentials.Fields {
		if f.Name == "EVIL" {
			t.Fatal("ignored ledger entry influenced the recipe")
		}
	}
}

func TestResolveLedgerIsDeterministic(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Attribute: "endpoint", Value: `"https://x.example/mcp"`}, Source: "mcp-registry", Rank: RankL3},
		credEntry("B_TOKEN", true, RankR2, "mcp-registry"),
		credEntry("A_TOKEN", true, RankR3, "server.json"),
		{Claim: Claim{Field: "OPT", Required: boolp(false), Delivery: "env"}, Source: "x", Rank: RankR6},
	}
	subject := RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}
	first, err := ResolveLedger(subject, entries)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		shuffled := slices.Clone(entries)
		for j := range shuffled {
			k := (i + j) % len(shuffled)
			shuffled[j], shuffled[k] = shuffled[k], shuffled[j]
		}
		got, err := ResolveLedger(subject, shuffled)
		if err != nil || got.Digest() != first.Digest() {
			t.Fatalf("iteration %d: digest differs or err=%v", i, err)
		}
	}
}

func TestLedgerR3KeepsDeclaredRequired(t *testing.T) {
	entries := []LedgerEntry{
		{Claim: Claim{Field: "API_TOKEN", Required: boolp(true), Secret: boolp(true), Delivery: "env"}, Source: "server.json", Rank: RankR3},
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: SubjectRemoteMCP, Ref: "r@1"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.Credentials.Fields) != 1 || !recipe.Credentials.Fields[0].Required || recipe.Credentials.Fields[0].Unverified {
		t.Fatalf("author-declared R3 field must stay required: %+v", recipe.Credentials.Fields)
	}
}
