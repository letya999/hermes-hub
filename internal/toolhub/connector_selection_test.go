package toolhub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type stalledDiscoveryCatalog struct{}

func (stalledDiscoveryCatalog) Lookup(context.Context, api.RecipeLookup) ([]api.RecipeCandidate, error) {
	return nil, nil
}
func (stalledDiscoveryCatalog) SearchSources(ctx context.Context, _ string) ([]api.DiscoveryCandidate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDiscoveryGitHubSurvivesRegistryTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"full_name":"acme/fallback","html_url":"https://github.com/acme/fallback"}]}`))
	}))
	defer server.Close()
	c := asyncFixture(t, nil)
	c.RecipeCatalogs = []api.RecipeCatalog{stalledDiscoveryCatalog{}, api.GitHubSearchCatalog{Endpoint: server.URL}}
	result, err := c.connectorSearch(t.Context(), aliceAuth(), "zz-timeout-regression-0148", "")
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Source != "https://github.com/acme/fallback" {
		t.Fatalf("registry timeout prevented GitHub fallback: %+v %v", result, err)
	}
}

func TestConnectorInstallUsesSelectedSource(t *testing.T) {
	want := api.ArtifactSource{Repository: "https://github.com/acme/selected-mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	var seen api.ArtifactSource
	c := asyncFixture(t, func(_ context.Context, source api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		seen = source
		return api.SourceReview{}, api.ErrInvalid
	})
	c.PrepareSyncWindow = -1
	c.RecipeCatalogs = []api.RecipeCatalog{stubSourceCatalog{found: []api.DiscoveryCandidate{{Name: "selected", Source: want, Status: "registry-source"}}}}
	result, err := c.connectorSearch(t.Context(), aliceAuth(), "selected", "")
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("search: %+v %v", result, err)
	}
	_, _ = c.prepareInstallTarget(t.Context(), aliceAuth(), result.Candidates[0].ID, "", "selection", nil)
	if seen != want {
		t.Fatalf("selected repository was not reviewed: got %+v want %+v", seen, want)
	}
}

func TestConnectorInstallResolvesBareGitHubURL(t *testing.T) {
	want := api.ArtifactSource{Repository: "https://github.com/acme/direct-mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	var seen api.ArtifactSource
	c := asyncFixture(t, func(_ context.Context, source api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		seen = source
		return api.SourceReview{}, api.ErrInvalid
	})
	c.PrepareSyncWindow = -1
	c.SourceResolver = func(_ context.Context, raw string) (api.ArtifactSource, error) {
		if raw != want.Repository {
			t.Fatalf("wrong repository sent to resolver: %s", raw)
		}
		return want, nil
	}
	_, _ = c.prepareInstallTarget(t.Context(), aliceAuth(), want.Repository, "", "direct-source", nil)
	if seen != want {
		t.Fatalf("bare GitHub URL did not reach pinned source review: got %+v", seen)
	}
}

func TestConnectorInstallResolvesBareGitLabURL(t *testing.T) {
	want := api.ArtifactSource{Repository: "https://gitlab.com/acme/team/direct-mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	var seen api.ArtifactSource
	c := asyncFixture(t, func(_ context.Context, source api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		seen = source
		return api.SourceReview{}, api.ErrInvalid
	})
	c.PrepareSyncWindow = -1
	c.SourceResolver = func(_ context.Context, raw string) (api.ArtifactSource, error) {
		if raw != want.Repository {
			t.Fatalf("wrong repository: %s", raw)
		}
		return want, nil
	}
	_, _ = c.prepareInstallTarget(t.Context(), aliceAuth(), want.Repository, "", "gitlab-source", nil)
	if seen != want {
		t.Fatalf("GitLab URL did not reach source review: %+v", seen)
	}
}

func TestConnectorInstallForwardsConnectionRecipe(t *testing.T) {
	var got api.ConnectionRecipe
	c := asyncFixture(t, func(ctx context.Context, _ api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		got, _ = ctx.Value(api.ConnectionRecipeContextKey{}).(api.ConnectionRecipe)
		return api.SourceReview{}, api.ErrInvalid
	})
	c.PrepareSyncWindow = -1
	args := map[string]any{"connection_recipe": map[string]any{"fields": []any{map[string]any{"name": "GOOGLE_APPLICATION_CREDENTIALS", "type": "json", "required": true, "secret": true, "delivery": "file", "target": "/run/mcp-secrets/adc.json"}}}}
	_, _ = c.prepareInstallTarget(t.Context(), aliceAuth(), githubCommitURL(), "", "recipe-forward", args)
	if len(got.Fields) != 1 || got.Fields[0].Delivery != "file" {
		t.Fatalf("recipe was ignored: %+v", got)
	}
}

func TestConnectorStatusObservesInstallPhase(t *testing.T) {
	c := asyncFixture(t, nil)
	auth := aliceAuth()
	onboarding := m13P3Onboarding(auth, "phase", "phase-mcp", "https://github.com/acme/phase-mcp", api.PhasePreparing)
	op := c.newConnectorOperation(auth, api.OpIntentInstall, "phase-mcp", connectorInstallSteps)
	op.OnboardingID, op.State = onboarding.OnboardingID, api.OpStateRunning
	if err := c.Store.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ phase, state string }{{api.PhasePreparing, api.OpStateRunning}, {api.PhaseAwaitingCreds, api.OpStateAwaitingUser}, {api.PhaseAwaitingOAuth, api.OpStateAwaitingUser}, {api.PhaseAwaitingConfirm, api.OpStateAwaitingConfirm}, {api.PhaseFailed, api.OpStateFailed}, {api.PhaseEnabled, api.OpStateDone}} {
		onboarding.Phase = tc.phase
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
		for _, byID := range []bool{true, false} {
			target, id := "phase-mcp", ""
			if byID {
				target, id = "", op.OperationID
			}
			got, err := c.connectorStatus(t.Context(), auth, target, id)
			if err != nil || got.State != tc.state {
				t.Fatalf("phase=%s byID=%v: %+v %v", tc.phase, byID, got, err)
			}
		}
	}
}

func TestSourceInstallRejectsIgnoredCredentials(t *testing.T) {
	called := false
	c := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		called = true
		return api.SourceReview{}, api.ErrInvalid
	})
	c.PrepareSyncWindow = -1
	_, err := c.prepareSource(t.Context(), aliceAuth(), map[string]any{"source": githubCommitURL(), "credentials": []any{map[string]any{"name": "API_KEY", "required": true}}})
	if err == nil || called {
		t.Fatal("unsupported credentials argument must fail before source review; use connection_recipe")
	}
}

func TestConnectorInstallReportsAsynchronousFailure(t *testing.T) {
	c := asyncFixture(t, nil)
	auth := aliceAuth()
	onboarding := m13P3Onboarding(auth, "async-failed", "broken-mcp", "https://github.com/acme/broken-mcp", api.PhaseFailed)
	onboarding.Error = "MCP tools/list failed"
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	op := c.newConnectorOperation(auth, api.OpIntentInstall, "broken-mcp", connectorInstallSteps)
	op.OnboardingID, op.State = onboarding.OnboardingID, api.OpStateRunning
	if err := c.Store.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	got, err := c.connectorInstall(t.Context(), auth, map[string]any{"target": "broken-mcp"})
	if err != nil || got.State != api.OpStateFailed || got.Code != api.OpCodeProbeFailed || !strings.Contains(got.Summary, onboarding.Error) {
		t.Fatalf("failed install stayed running or hid cause: %+v %v", got, err)
	}
	status, err := c.connectorStatus(t.Context(), auth, "", op.OperationID)
	if err != nil || status.Code != api.OpCodeProbeFailed || !strings.Contains(status.Summary, onboarding.Error) {
		t.Fatalf("status hid install cause: %+v %v", status, err)
	}
}

func TestConnectorInstallReportsImmediateFailure(t *testing.T) {
	secret := "ghp_" + strings.Repeat("x", 24)
	c := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		return api.SourceReview{}, fmt.Errorf("upstream dependency unavailable: %s", secret)
	})
	c.PrepareSyncWindow = -1
	result, err := c.connectorInstall(t.Context(), aliceAuth(), map[string]any{"target": githubCommitURL()})
	if err != nil || result.State != api.OpStateFailed || result.Code == "" || !strings.Contains(result.Summary, "upstream dependency unavailable") || strings.Contains(result.Summary, secret) {
		t.Fatalf("failure concealed: %+v %v", result, err)
	}
}

func TestConnectorRecipeCorrectionStartsDistinctIdempotentOperation(t *testing.T) {
	calls := 0
	c := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		calls++
		return api.SourceReview{}, fmt.Errorf("fixture review failure")
	})
	c.PrepareSyncWindow = -1
	args := map[string]any{"target": githubCommitURL()}
	original, err := c.connectorInstall(t.Context(), aliceAuth(), args)
	if err != nil {
		t.Fatal(err)
	}
	args["connection_recipe"] = map[string]any{"fields": []any{map[string]any{"name": "API_KEY", "required": true, "secret": true, "delivery": "env", "target": "API_KEY"}}}
	corrected, err := c.connectorInstall(t.Context(), aliceAuth(), args)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := c.connectorInstall(t.Context(), aliceAuth(), args)
	if err != nil || calls != 2 || original.OperationID == corrected.OperationID || replay.OperationID != corrected.OperationID {
		t.Fatalf("correction/replay: calls=%d original=%+v corrected=%+v replay=%+v err=%v", calls, original, corrected, replay, err)
	}
}
