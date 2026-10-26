package toolhub

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The deferred auth-first review hands the stored recipe's connection back to
// the resolver, which stamps provenance, compacts and appends field slices in
// place. If those writes reach the store's copy, the recipe no longer matches
// its content digest and the next load fails closed on "persisted recipe
// digest".
func TestStoreRecipeSurvivesCallerMutation(t *testing.T) {
	store := NewStore()
	trueValue := true
	resolution := api.RecipeResolution{
		Source:   api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("1", 40)},
		Launch:   api.LaunchRecipe{Artifact: "ghcr.io/acme/weather@sha256:" + strings.Repeat("2", 64), Transport: api.ContainerMCP},
		State:    "ready",
		Egress:   []string{"api.example"},
		Trust:    api.TrustT2,
		Warnings: []string{"b-warning", "a-warning"},
		Ledger: []api.LedgerEntry{{Source: "repo", Ref: ".env.example", Rank: api.RankR3, Claim: api.Claim{
			Field: "WEATHER_TOKEN", Required: &trueValue, Secret: &trueValue,
		}}},
		Connection: api.ConnectionRecipe{
			Fields: []api.ConnectionField{
				{Name: "WEATHER_TOKEN", Type: "token", Required: true, Secret: true, Delivery: "env"},
				{Name: "WEATHER_REGION", Type: "text", Delivery: "env"},
			},
			Alternatives: []api.ConnectionAlternative{{Name: "oauth", Fields: []api.ConnectionField{{Name: "WEATHER_OAUTH", Type: "token"}}}},
		},
	}
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatalf("PutRecipe: %v", err)
	}

	// Replay exactly what connectionWithSource + mergeConnection +
	// normalizeConnection + Overlay + the resolver's warning sort do to the
	// caller's copy: stamp provenance on fields and alternative fields, drop
	// fields, append catalog claims, sort warnings in place.
	mutated := resolution
	for i := range mutated.Connection.Fields {
		mutated.Connection.Fields[i].Source = "install-recipe"
	}
	for i := range mutated.Connection.Alternatives {
		for j := range mutated.Connection.Alternatives[i].Fields {
			mutated.Connection.Alternatives[i].Fields[j].Source = "install-recipe"
		}
	}
	mutated.Connection.Fields = slices.DeleteFunc(mutated.Connection.Fields, func(f api.ConnectionField) bool {
		return f.Name == "WEATHER_REGION"
	})
	mutated.Connection.Fields = append(mutated.Connection.Fields, api.ConnectionField{Name: "WEATHER_ORG", Type: "text", Delivery: "env"})
	mutated.Ledger = append(mutated.Ledger, api.LedgerEntry{Source: "catalog", Ref: "entry", Claim: api.Claim{Field: "WEATHER_ORG"}})
	mutated.Ledger[0].Rank = api.RankR7
	*mutated.Ledger[0].Claim.Required = false
	*mutated.Ledger[0].Claim.Secret = false
	slices.Sort(mutated.Warnings)

	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("store must survive caller-side mutation: %v", err)
	}
	stored, err := reloaded.Recipe(digest)
	if err != nil {
		t.Fatalf("Recipe(%s): %v", digest, err)
	}
	if len(stored.Connection.Fields) != 2 || stored.Connection.Fields[0].Source != "" {
		t.Fatalf("stored recipe mutated through caller's slice: %+v", stored.Connection.Fields)
	}
	if stored.Connection.Alternatives[0].Fields[0].Source != "" {
		t.Fatalf("stored alternative field stamped through caller's slice: %+v", stored.Connection.Alternatives[0].Fields)
	}
	if len(stored.Ledger) != 1 || stored.Ledger[0].Rank != api.RankR3 ||
		!*stored.Ledger[0].Claim.Required || !*stored.Ledger[0].Claim.Secret {
		t.Fatalf("stored ledger mutated through caller's slice: %+v", stored.Ledger)
	}
	if stored.Warnings[0] != "b-warning" {
		t.Fatalf("stored warnings reordered through caller's slice: %+v", stored.Warnings)
	}
}

// Recipe hands callers a resolution they are free to filter and merge — the
// credential-source rebuild stamps and appends on it — so those writes must
// never reach the map entry either.
func TestStoreRecipeReadReturnsIsolatedCopy(t *testing.T) {
	store := NewStore()
	resolution := api.RecipeResolution{
		Source:     api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("3", 40)},
		State:      "ready",
		Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "WEATHER_TOKEN", Type: "token", Required: true}}},
	}
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatalf("PutRecipe: %v", err)
	}
	read, err := store.Recipe(digest)
	if err != nil {
		t.Fatalf("Recipe: %v", err)
	}
	read.Connection.Fields[0].Source = "install-recipe"
	read.Connection.Fields = append(read.Connection.Fields, api.ConnectionField{Name: "INJECTED", Type: "text"})
	reread, err := store.Recipe(digest)
	if err != nil {
		t.Fatalf("Recipe reread: %v", err)
	}
	if len(reread.Connection.Fields) != 1 || reread.Connection.Fields[0].Source != "" {
		t.Fatalf("read-side mutation reached the stored recipe: %+v", reread.Connection.Fields)
	}
}

// recipeOf hands the live onboarding recipe to callers that filter and
// extend it (rebuildCredentialSource rewrites connection and appends ledger
// claims). Returning the embedded pointer lets those writes drift the
// in-memory recipe away from the onboarding's own recipe_digest.
func TestRecipeOfReturnsIsolatedOnboardingRecipe(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store}
	onboarding := api.Onboarding{
		Recipe: &api.RecipeResolution{
			State:      "ready",
			Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "WEATHER_TOKEN", Type: "token", Required: true}}},
			Ledger:     []api.LedgerEntry{{Source: "repo", Claim: api.Claim{Field: "WEATHER_TOKEN"}}},
		},
	}
	recipe := control.recipeOf(onboarding)
	if recipe == nil {
		t.Fatal("recipeOf returned nil for an embedded recipe")
	}
	recipe.Connection.Fields[0].Source = "owner"
	recipe.Ledger = append(recipe.Ledger, api.LedgerEntry{Source: "owner", Claim: api.Claim{Field: "WEATHER_ORG"}})
	if onboarding.Recipe.Connection.Fields[0].Source != "" || len(onboarding.Recipe.Ledger) != 1 {
		t.Fatalf("recipeOf caller mutated the embedded recipe: %+v", onboarding.Recipe.Connection.Fields[0])
	}
}
