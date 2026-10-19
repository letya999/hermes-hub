package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestOnboardingReferencesRecipeDigest(t *testing.T) {
	// The shared recipe is content-addressed: onboardings carry its digest,
	// not an embedded copy.
	store := NewStore()
	resolution := api.RecipeResolution{
		Source: api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("1", 40)},
		Launch: api.LaunchRecipe{Artifact: "ghcr.io/acme/weather@sha256:" + strings.Repeat("2", 64), Transport: api.ContainerMCP},
		State:  "ready",
	}
	digest, err := store.PutRecipe(resolution)
	if err != nil || digest == "" {
		t.Fatalf("PutRecipe: %v %q", err, digest)
	}
	loaded, err := store.Recipe(digest)
	if err != nil {
		t.Fatalf("Recipe(%s): %v", digest, err)
	}
	if loaded.Launch.Artifact != resolution.Launch.Artifact {
		t.Fatalf("stored recipe lost its launch: %+v", loaded)
	}
	again, err := store.PutRecipe(resolution)
	if err != nil || again != digest {
		t.Fatalf("same content must pin the same digest: %q vs %q err=%v", again, digest, err)
	}
}

func TestOnboardingSnapshotCarriesNoEmbeddedRecipe(t *testing.T) {
	// A snapshot onboarding references recipes/<sha256>; serializing it must
	// not smuggle the full resolution back in.
	store := NewStore()
	resolution := api.RecipeResolution{State: "ready", Launch: api.LaunchRecipe{Endpoint: "https://mcp.example/x", Transport: api.RemoteMCP}}
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{
		Schema:        api.SchemaVersion,
		OnboardingID:  "onboard-aaaabbbb",
		PrincipalID:   "alice",
		ContextID:     "alice",
		RuntimeID:     "runtime",
		PolicyVersion: "policy-1",
		Mode:          api.OnboardingSelfInstall,
		Phase:         api.PhaseAwaitingConfirm,
		Revision:      1,
	}
	onboarding.RecipeDigest = digest
	onboarding.Recipe = &resolution // in-memory copy; must not serialize
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/store.json"
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	stored := raw["onboardings"].([]any)[0].(map[string]any)
	if _, ok := stored["recipe"]; ok {
		t.Fatalf("onboarding persisted an embedded recipe copy: %v", stored)
	}
	if stored["recipe_digest"] != digest {
		t.Fatalf("onboarding lost its recipe reference: %v", stored)
	}
	if _, ok := raw["recipes"].(map[string]any)[digest]; !ok {
		t.Fatalf("snapshot lost the content-addressed recipe: %v", raw["recipes"])
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Recipe(digest); err != nil {
		t.Fatalf("recipe did not survive reload: %v", err)
	}
}

func TestPreparedOnboardingSurvivesReloadByRecipeDigest(t *testing.T) {
	// After a snapshot reload the onboarding no longer embeds its resolution;
	// status must still surface it through the shared recipe store.
	definition := userMCPDefinition()
	recipe := &api.RecipeResolution{
		Source: api.ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: strings.Repeat("3", 40)},
		State:  "draft",
		Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.example/x"},
	}
	fix := newControlFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review", Recipe: recipe}, nil
	})
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": "https://github.com/example/mcp/commit/" + strings.Repeat("3", 40), "request_key": "digest-reload"})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/store.json"
	if err := fix.store.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fix.control.Store = reloaded
	status, err := fix.control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if status["recipe"] == nil {
		t.Fatalf("reloaded onboarding lost its recipe: %+v", status)
	}
}

func TestCredentialSourceReplayAfterReload(t *testing.T) {
	// credential_source rebuild must work on a reloaded onboarding whose
	// recipe only exists as a store digest reference.
	review := func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []api.CredentialInput{{Name: "REQ_TOKEN", Required: true}, {Name: "EXTRA_PROJECT"}}
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review",
			Recipe: &api.RecipeResolution{Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{
				{Name: "REQ_TOKEN", Type: "secret", Required: true, Secret: true, Delivery: "env", Target: "REQ_TOKEN", Source: "server.json"},
				{Name: "EXTRA_PROJECT", Type: "string", Required: false, Delivery: "env", Target: "EXTRA_PROJECT", Source: ".env.example"},
			}}}}, nil
	}
	control := asyncFixture(t, review)
	control.PrepareSyncWindow = -1
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-reload"}); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/store.json"
	if err := control.Store.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control.Store = reloaded
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-reload", "credential_source": ".env.example"}); err != nil {
		t.Fatalf("rebuild after reload: %v", err)
	}
	onboarding, _ := control.Store.FindOnboardingByKey(aliceAuth(), "src-reload")
	if len(onboarding.Required) != 0 || len(onboarding.Optional) != 1 {
		t.Fatalf("rebuilt hints required=%+v optional=%+v", onboarding.Required, onboarding.Optional)
	}
}

func TestOnboardingFixtureShrinks(t *testing.T) {
	// 200 onboardings over one shared definition+recipe: the snapshot must
	// store the heavy payloads once, not 200 times.
	store := NewStore()
	definition := userMCPDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	resolution := api.RecipeResolution{State: "ready", Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.example/x"}}
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: fmt.Sprintf("onboard-%08d", i),
			PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
			Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingConfirm, Revision: 1,
			DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			Definition: &definition, Recipe: &resolution, RecipeDigest: digest,
		}
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
	}
	path := t.TempDir() + "/store.json"
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The naive baseline embeds definition+recipe per onboarding; the snapshot
	// must stay strictly smaller than that embedding.
	defBytes, _ := json.Marshal(definition)
	recipeBytes, _ := json.Marshal(resolution)
	if baseline := int64(len(defBytes)+len(recipeBytes)) * 200; int64(len(data)) >= baseline {
		t.Fatalf("snapshot %dB not smaller than the %dB embedded baseline", len(data), baseline)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, item := range raw["onboardings"].([]any) {
		o := item.(map[string]any)
		if _, ok := o["definition"]; ok {
			t.Fatal("onboarding persisted an embedded definition copy")
		}
	}
	if len(raw["recipes"].(map[string]any)) != 1 {
		t.Fatalf("shared recipe deduplicated to %d copies", len(raw["recipes"].(map[string]any)))
	}
}

func TestCredentialSourceRecordsLedgerEntry(t *testing.T) {
	// credential_source is owner-stated contract evidence, recorded on the
	// credential ladder at owner-words rank — not an agent's silent choice.
	review := func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []api.CredentialInput{{Name: "REQ_TOKEN", Required: true}, {Name: "EXTRA_PROJECT"}}
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review",
			Recipe: &api.RecipeResolution{Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{
				{Name: "REQ_TOKEN", Type: "secret", Required: true, Secret: true, Delivery: "env", Target: "REQ_TOKEN", Source: "server.json"},
				{Name: "EXTRA_PROJECT", Type: "string", Required: false, Delivery: "env", Target: "EXTRA_PROJECT", Source: ".env.example"},
			}}}}, nil
	}
	control := asyncFixture(t, review)
	control.PrepareSyncWindow = -1
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-ledger"}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-ledger", "credential_source": "server.json"}); err != nil {
		t.Fatal(err)
	}
	onboarding, _ := control.Store.FindOnboardingByKey(aliceAuth(), "src-ledger")
	if onboarding.Recipe == nil {
		t.Fatal("onboarding lost its recipe")
	}
	var found bool
	for _, entry := range onboarding.Recipe.Ledger {
		if entry.Rank == api.RankR7 && entry.Ref == "server.json" && entry.Claim.Field == "REQ_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("owner-confirmed source left no R7 ledger entry: %+v", onboarding.Recipe.Ledger)
	}
}
