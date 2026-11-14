package toolhub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	for _, tc := range []struct{ phase, state string }{{api.PhasePreparing, api.ModelStateWorking}, {api.PhaseAwaitingCreds, api.ModelStateWaitingYou}, {api.PhaseAwaitingOAuth, api.ModelStateWaitingYou}, {api.PhaseFailed, api.ModelStateBroken}, {api.PhaseAwaitingConfirm, api.ModelStateWaitingYou}, {api.PhaseEnabled, api.ModelStateReady}} {
		applyOnboardingPhase(&onboarding, tc.phase)
		onboarding.Revision++
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
	onboarding.Finalizers = []api.Finalizer{{Name: api.FinalizerBrokerCredential, State: api.FinalizerFailed}}
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	op := c.newConnectorOperation(auth, api.OpIntentInstall, "broken-mcp", connectorInstallSteps)
	op.OnboardingID, op.State = onboarding.OnboardingID, api.OpStateRunning
	if err := c.Store.PutOperation(op); err != nil {
		t.Fatal(err)
	}
	got, err := c.connectorInstall(t.Context(), auth, map[string]any{"target": "broken-mcp"})
	if err != nil || got.State != api.ModelStateBroken || got.Code != api.OpCodeProbeFailed || got.YouMust != api.YouMustRemove || !strings.Contains(got.Summary, onboarding.Error) {
		t.Fatalf("failed install stayed running or hid cause: %+v %v", got, err)
	}
	status, err := c.connectorStatus(t.Context(), auth, "", op.OperationID)
	if err != nil || status.Code != api.OpCodeProbeFailed || !strings.Contains(status.Summary, onboarding.Error) {
		t.Fatalf("status hid install cause: %+v %v", status, err)
	}
}

// A clean install failure is retryable: the repeat call must mint the next
// operation generation instead of converging on the dead record — the
// documented you_must=retry path.
func TestConnectorInstallRetriesFailedGeneration(t *testing.T) {
	calls := 0
	c := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		calls++
		return api.SourceReview{}, fmt.Errorf("fixture review failure")
	})
	c.PrepareSyncWindow = -1
	auth := aliceAuth()
	args := map[string]any{"target": githubCommitURL()}
	first, err := c.connectorInstall(t.Context(), auth, args)
	if err != nil || first.State != api.ModelStateBroken || calls != 1 {
		t.Fatalf("first install: %+v calls=%d err=%v", first, calls, err)
	}
	second, err := c.connectorInstall(t.Context(), auth, args)
	if err != nil || calls != 2 || second.OperationID == first.OperationID {
		t.Fatalf("retry did not mint a fresh generation: calls=%d first=%+v second=%+v err=%v", calls, first, second, err)
	}
}

// Reauth on a failed record must not resurrect it: the old code persisted a
// pending credentials gate with an empty nonce, then died on the missing
// definition — leaving a wedged record that claims waiting_you forever.
func TestConnectorReauthRefusesDeadRecord(t *testing.T) {
	c := asyncFixture(t, nil)
	auth := aliceAuth()
	onboarding := m13P3Onboarding(auth, "reauth-dead", "unregistered-def", "https://github.com/acme/dead-mcp", api.PhaseFailed)
	onboarding.Error = "pinned builder image unavailable"
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	got, err := c.connectorReauth(t.Context(), auth, map[string]any{"target": onboarding.OnboardingID})
	if err != nil || got.State != api.ModelStateBroken || got.YouMust != api.YouMustRetry {
		t.Fatalf("reauth on dead record: %+v %v", got, err)
	}
	stored, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || stored.Stage != api.StageFailed || stored.Gate != nil {
		t.Fatalf("dead record mutated by reauth: %+v %v", stored, err)
	}
}

// A credentials wait with an empty nonce and no Broker request is a wedge —
// resend must not mint a form no submission can satisfy; the exit is a fresh
// install generation.
func TestConnectorResendRefusesCredentialDeadEnd(t *testing.T) {
	c := asyncFixture(t, nil)
	auth := aliceAuth()
	onboarding := m13P3Onboarding(auth, "wedged", "unregistered-def", "https://github.com/acme/dead-mcp", api.PhasePreparing)
	onboarding.Stage, onboarding.PreparingStub = api.StagePending, false
	onboarding.ArmGate(api.GateCredentials, "", time.Time{})
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	got, err := c.connectorResend(t.Context(), auth, map[string]any{"target": onboarding.OnboardingID})
	if err != nil || got.State != api.ModelStateBroken || got.YouMust != api.YouMustRetry {
		t.Fatalf("resend on wedged record: %+v %v", got, err)
	}
}

// The wedge is not a slot holder either: an install must proceed to a fresh
// prepare instead of attaching to a phantom pending record.
func TestOnboardingSlotSkipsCredentialDeadEnd(t *testing.T) {
	c := asyncFixture(t, nil)
	auth := aliceAuth()
	wedged := m13P3Onboarding(auth, "slot-wedge", "wedged-def", "https://github.com/acme/dead-mcp", api.PhasePreparing)
	wedged.Stage, wedged.PreparingStub = api.StagePending, false
	wedged.ArmGate(api.GateCredentials, "", time.Time{})
	healthy := m13P3Onboarding(auth, "slot-ok", "ok-def", "https://github.com/acme/ok-mcp", api.PhaseAwaitingCreds)
	healthy.BrokerRequestID = "r-alive"
	for _, o := range []api.Onboarding{wedged, healthy} {
		if err := c.Store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	if _, held := c.Store.OnboardingSlot(auth, "wedged-def"); held {
		t.Fatal("credential dead end held the onboarding slot")
	}
	if _, held := c.Store.OnboardingSlot(auth, "ok-def"); !held {
		t.Fatal("live credentials wait lost the onboarding slot")
	}
}

func TestConnectorInstallReportsImmediateFailure(t *testing.T) {
	secret := "ghp_" + strings.Repeat("x", 24)
	c := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		return api.SourceReview{}, fmt.Errorf("upstream dependency unavailable: %s", secret)
	})
	c.PrepareSyncWindow = -1
	result, err := c.connectorInstall(t.Context(), aliceAuth(), map[string]any{"target": githubCommitURL()})
	if err != nil || result.State != api.ModelStateBroken || result.Code == "" || !strings.Contains(result.Summary, "upstream dependency unavailable") || strings.Contains(result.Summary, secret) {
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
	// The corrected call failed too, so its replay is a retry: it advances to
	// a third generation rather than converging on the dead record.
	replay, err := c.connectorInstall(t.Context(), aliceAuth(), args)
	if err != nil || calls != 3 || original.OperationID == corrected.OperationID || replay.OperationID == corrected.OperationID {
		t.Fatalf("correction/replay: calls=%d original=%+v corrected=%+v replay=%+v err=%v", calls, original, corrected, replay, err)
	}
}
