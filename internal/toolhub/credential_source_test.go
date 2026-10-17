package toolhub

import (
	"context"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// credential_source rebuilds the credential recipe from one authoritative
// metadata file: fields from other sources leave both the required set and
// the definition credentials.
func TestPrepareSourceCredentialSourceFilter(t *testing.T) {
	control := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []api.CredentialInput{
			{Name: "REQ_TOKEN", Required: true},
			{Name: "EXTRA_PROJECT"},
		}
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review",
			Recipe: &api.RecipeResolution{Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{
				{Name: "REQ_TOKEN", Type: "secret", Required: true, Secret: true, Delivery: "env", Target: "REQ_TOKEN", Source: "server.json"},
				{Name: "EXTRA_PROJECT", Type: "string", Required: false, Delivery: "env", Target: "EXTRA_PROJECT", Source: ".env.example"},
			}}}}, nil
	})
	control.PrepareSyncWindow = -1
	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-filter", "credential_source": "server.json"})
	if err != nil {
		t.Fatal(err)
	}
	if body["phase"] != api.PhaseAwaitingCreds {
		t.Fatalf("phase=%v body=%v", body["phase"], body)
	}
	sources, _ := body["credential_sources"].([]string)
	if strings.Join(sources, ",") != ".env.example,server.json" {
		t.Fatalf("credential_sources=%v", body["credential_sources"])
	}
	onboarding, ok := control.Store.FindOnboardingByKey(aliceAuth(), "src-filter")
	if !ok {
		t.Fatal("onboarding not recorded")
	}
	if len(onboarding.Required) != 1 || onboarding.Required[0].Name != "REQ_TOKEN" {
		t.Fatalf("required=%+v", onboarding.Required)
	}
	if len(onboarding.Optional) != 0 {
		t.Fatalf("optional=%+v", onboarding.Optional)
	}
	if onboarding.Recipe == nil || len(onboarding.Recipe.Connection.Fields) != 1 {
		t.Fatalf("recipe fields=%+v", onboarding.Recipe)
	}
	if onboarding.Definition == nil || len(onboarding.Definition.Credentials) != 1 || onboarding.Definition.Credentials[0].Name != "REQ_TOKEN" {
		t.Fatalf("definition credentials=%+v", onboarding.Definition.Credentials)
	}
}

// Replaying prepare_source with the same request_key and a credential_source
// rebuilds the hint set on the existing onboarding instead of starting over.
func TestPrepareSourceCredentialSourceReplay(t *testing.T) {
	review := func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []api.CredentialInput{
			{Name: "REQ_TOKEN", Required: true},
			{Name: "EXTRA_PROJECT"},
		}
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review",
			Recipe: &api.RecipeResolution{Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{
				{Name: "REQ_TOKEN", Type: "secret", Required: true, Secret: true, Delivery: "env", Target: "REQ_TOKEN", Source: "server.json"},
				{Name: "EXTRA_PROJECT", Type: "string", Required: false, Delivery: "env", Target: "EXTRA_PROJECT", Source: ".env.example"},
			}}}}, nil
	}
	control := asyncFixture(t, review)
	control.PrepareSyncWindow = -1
	if _, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-replay"}); err != nil {
		t.Fatal(err)
	}
	onboarding, ok := control.Store.FindOnboardingByKey(aliceAuth(), "src-replay")
	if !ok || len(onboarding.Optional) != 1 {
		t.Fatalf("pre-replay onboarding=%+v", onboarding)
	}
	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{
		"source": githubCommitURL(), "request_key": "src-replay", "credential_source": ".env.example"})
	if err != nil {
		t.Fatal(err)
	}
	if body["phase"] != api.PhaseAwaitingCreds {
		t.Fatalf("phase=%v", body["phase"])
	}
	onboarding, _ = control.Store.FindOnboardingByKey(aliceAuth(), "src-replay")
	if len(onboarding.Required) != 0 || len(onboarding.Optional) != 1 || onboarding.Optional[0].Name != "EXTRA_PROJECT" {
		t.Fatalf("rebuilt hints required=%+v optional=%+v", onboarding.Required, onboarding.Optional)
	}
	if len(onboarding.Recipe.Connection.Fields) != 1 {
		t.Fatalf("recipe fields=%+v", onboarding.Recipe.Connection.Fields)
	}
}

// The agent must discover credential_source from the tool schema — the
// conflict-recovery path only works when the argument is documented there.
func TestControlToolContractDocumentsCredentialSource(t *testing.T) {
	_, schema := controlToolContract("prepare_source")
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["credential_source"] == nil {
		t.Fatalf("prepare_source schema missing credential_source: %#v", schema["properties"])
	}
}
