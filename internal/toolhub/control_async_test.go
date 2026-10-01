package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func asyncFixture(t *testing.T, reviewer SourceReviewer) *ControlPlane {
	t.Helper()
	store := NewStore()
	if err := store.PutGrant(OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	return &ControlPlane{Store: store, Reviewer: reviewer, WorkloadRoot: t.TempDir(), Now: time.Now,
		ConfirmationTTL: 10 * time.Minute, PrepareSyncWindow: 50 * time.Millisecond}
}

func TestPrepareSelfInstallAsyncReturnsPreparingThenCompletes(t *testing.T) {
	release := make(chan struct{})
	notified := make(chan Onboarding, 1)
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		<-release
		definition := userMCPDefinition()
		return SourceReview{Definition: definition, Permissions: toolNames(definition), Effects: effectNames(definition), ReviewDigest: "sha256:review"}, nil
	})
	control.PrepareDone = func(_ context.Context, _ identity.Envelope, onboarding Onboarding) { notified <- onboarding }

	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "async-ok"})
	if err != nil {
		t.Fatal(err)
	}
	if body["phase"] != PhasePreparing || body["next_action"] != "poll_status" {
		t.Fatalf("expected preparing body, got %v", body)
	}
	id := body["onboarding_id"].(string)
	st, err := control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": id})
	if err != nil || st["phase"] != PhasePreparing {
		t.Fatalf("mid-flight status=%v err=%v", st, err)
	}
	close(release)
	select {
	case onboarding := <-notified:
		if onboarding.Phase != PhaseAwaitingConfirm {
			t.Fatalf("wake phase=%s", onboarding.Phase)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PrepareDone never fired")
	}
	st, err = control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": id})
	if err != nil || st["phase"] != PhaseAwaitingConfirm {
		t.Fatalf("final status=%v err=%v", st, err)
	}
}

func TestPrepareSelfInstallURLContractStillAwaitsCredentials(t *testing.T) {
	// A required URL field is not secret-named, so credentialHints returns
	// nothing and newOnboarding picks awaiting-confirm; the recipe overwrite
	// then repopulates Required. The phase must follow the final Required set
	// or confirm deadlocks on a locator that can never be submitted.
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []CredentialInput{{Name: "UPSTREAM_URL", Required: true}}
		return SourceReview{Definition: definition, Permissions: toolNames(definition), Effects: effectNames(definition), ReviewDigest: "sha256:review",
			Recipe: &RecipeResolution{Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "UPSTREAM_URL", Type: "url", Required: true, Secret: false, Delivery: "env", Target: "UPSTREAM_URL"}}}}}, nil
	})
	control.PrepareSyncWindow = -1
	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "url-contract"})
	if err != nil {
		t.Fatal(err)
	}
	if body["phase"] != PhaseAwaitingCreds {
		t.Fatalf("url-only contract skipped credentials phase: %v", body["phase"])
	}
	onboarding, ok := control.Store.FindOnboardingByKey(aliceAuth(), "url-contract")
	if !ok || onboarding.Phase != PhaseAwaitingCreds || len(onboarding.Required) != 1 || onboarding.ConfirmationNonce != "" {
		t.Fatalf("deadlocked onboarding persisted: %+v", onboarding)
	}
}

func TestPrepareSelfInstallDriftedContractIsReReviewed(t *testing.T) {
	// A stored definition whose confirmed tool contract no longer verifies
	// (for example, stamped by an older pipeline) must not be reused: it can
	// only fail admission with a digest-drift error. Reuse skips re-review, so
	// the intact check has to happen before the reuse decision.
	stored := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "mcp", Version: "0.0.2", Transport: ContainerMCP,
		Source: DefinitionSource{
			Repository:         "https://github.com/example/mcp",
			CommitSHA:          "0123456789abcdef0123456789abcdef01234567",
			Image:              "hermes-artifact/mcp",
			Digest:             "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			ToolContractSource: ToolContractPreflight,
		},
		Tools:     []ToolSpec{{Name: "search", Effect: ReadEffect, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Workload:  WorkloadPolicy{Class: PerUser, Rationale: "user self-install", ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Execution: ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64, Egress: []string{"example.com"}},
		Health:    HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
	for _, scenario := range []string{"intact", "drifted"} {
		t.Run(scenario, func(t *testing.T) {
			definition := stored
			if scenario == "intact" {
				digest, err := confirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractPreflight, Tools: definition.Tools})
				if err != nil {
					t.Fatal(err)
				}
				definition.Source.ToolContractDigest = digest
			} else {
				definition.Source.ToolContractDigest = "sha256:" + strings.Repeat("0", 64)
			}
			var calls atomic.Int32
			control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
				calls.Add(1)
				return SourceReview{Definition: userMCPDefinition()}, nil
			})
			// The reuse decision must resolve synchronously: the default 50ms
			// async window can expire before the goroutine runs on a slow host.
			control.PrepareSyncWindow = -1
			if err := control.Store.RegisterDefinition(definition); err != nil {
				t.Fatal(err)
			}
			if err := control.Store.PutPublication(DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
				t.Fatal(err)
			}
			body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "reuse-" + scenario})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "intact" {
				if calls.Load() != 0 || body["phase"] != PhaseAwaitingConfirm {
					t.Fatalf("intact contract not reused: calls=%d phase=%v", calls.Load(), body["phase"])
				}
				return
			}
			if calls.Load() != 1 {
				t.Fatalf("drifted contract reused instead of re-review: calls=%d", calls.Load())
			}
		})
	}
}

func TestPrepareSelfInstallAsyncFailureIsDurable(t *testing.T) {
	release := make(chan struct{})
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		<-release
		return SourceReview{}, errors.New("build blew up")
	})
	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "async-fail"})
	if err != nil || body["phase"] != PhasePreparing {
		t.Fatalf("expected preparing, got %v err=%v", body, err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if existing, ok := control.Store.FindOnboardingByKey(aliceAuth(), "async-fail"); ok && existing.Phase == PhaseFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	existing, ok := control.Store.FindOnboardingByKey(aliceAuth(), "async-fail")
	if !ok || existing.Phase != PhaseFailed {
		t.Fatalf("failed prepare left phase=%v ok=%v", existing.Phase, ok)
	}
	st, err := control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": existing.OnboardingID})
	if err != nil || st["next_action"] != "retry" {
		t.Fatalf("failed status=%v err=%v", st, err)
	}
}

func TestPrepareSelfInstallAsyncRetryWhilePreparingDoesNotDuplicate(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		calls.Add(1)
		<-release
		return SourceReview{Definition: userMCPDefinition()}, nil
	})
	args := map[string]any{"source": githubCommitURL(), "request_key": "async-dup"}
	first, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", args)
	if err != nil || first["phase"] != PhasePreparing {
		t.Fatalf("first=%v err=%v", first, err)
	}
	again, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", args)
	if err != nil || again["phase"] != PhasePreparing || again["onboarding_id"] != first["onboarding_id"] {
		t.Fatalf("retry=%v err=%v", again, err)
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("duplicate retry spawned %d reviews", calls.Load())
	}
}

func TestNotifyPrepareDoneReachesSubscribedSession(t *testing.T) {
	fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
	messages := make(chan *mcp.LoggingMessageParams, 1) //lint:ignore SA1019 deprecation-window API under test
	client := mcp.NewClient(&mcp.Implementation{Name: "wake-test", Version: "1"}, &mcp.ClientOptions{
		LoggingMessageHandler: func(_ context.Context, r *mcp.LoggingMessageRequest) { messages <- r.Params }, //lint:ignore SA1019 deprecation-window API under test
	})
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   fix.server.URL + DefaultEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: aliceToken}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	//lint:ignore SA1019 deprecation-window API under test
	if err := session.SetLoggingLevel(context.Background(), &mcp.SetLoggingLevelParams{Level: "notice"}); err != nil {
		t.Fatal(err)
	}
	fix.gateway.notifyPrepareDone(context.Background(), aliceAuth(), Onboarding{OnboardingID: "onboard-x", Phase: PhaseAwaitingConfirm, DefinitionID: "user-mcp", SourceURL: "https://github.com/example/mcp"})
	select {
	case params := <-messages:
		data, _ := json.Marshal(params.Data)
		if !strings.Contains(string(data), "onboard-x") {
			t.Fatalf("wake payload=%s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wake notification")
	}
	// A different owner's session is never woken for alice's onboarding.
	fix.gateway.notifyPrepareDone(context.Background(), bobAuth(), Onboarding{OnboardingID: "onboard-y", Phase: PhaseFailed})
	select {
	case params := <-messages:
		t.Fatalf("foreign wake leaked: %v", params.Data)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPrepareSelfInstallAsyncWindowBranches(t *testing.T) {
	// Negative window: fully synchronous, preserves the pre-async contract.
	control := asyncFixture(t, fixtureReviewer(userMCPDefinition()))
	control.PrepareSyncWindow = -1
	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "sync-mode"})
	if err != nil || body["phase"] == PhasePreparing {
		t.Fatalf("sync prepare=%v err=%v", body, err)
	}

	// A canceled caller context still returns the durable preparing record.
	control = asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		select {} // reviewer never returns
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, err = control.Invoke(ctx, aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "gone-client"})
	if err != nil || body["phase"] != PhasePreparing {
		t.Fatalf("disconnected prepare=%v err=%v", body, err)
	}

	// An unresolvable source fails before any background work is spawned.
	control = asyncFixture(t, nil)
	if _, err = control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": "not-a-url"}); err == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestPrepareRequestKeyDedupeRefreshesExisting(t *testing.T) {
	control := asyncFixture(t, fixtureReviewer(userMCPDefinition()))
	control.PrepareSyncWindow = -1
	args := map[string]any{"source": githubCommitURL(), "request_key": "dedupe-1"}
	first, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", args)
	if err != nil {
		t.Fatal(err)
	}
	again, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", args)
	if err != nil || again["onboarding_id"] != first["onboarding_id"] {
		t.Fatalf("dedupe=%v err=%v", again, err)
	}
}

func TestLatestPrepareRecordFindsBumpedSibling(t *testing.T) {
	control := asyncFixture(t, nil)
	stub := Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-stub", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: OnboardingSelfInstall, Phase: PhaseRemoved, SourceURL: "https://github.com/example/mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567", IdempotencyKey: "k1", Revision: 1, CreatedAt: time.Now().Add(-time.Hour)}
	if err := control.Store.PutOnboarding(stub); err != nil {
		t.Fatal(err)
	}
	sibling := stub
	sibling.OnboardingID = "onboard-real"
	sibling.Phase = PhaseAwaitingConfirm
	sibling.CreatedAt = time.Now()
	if err := control.Store.PutOnboarding(sibling); err != nil {
		t.Fatal(err)
	}
	if got := control.latestPrepareRecord(aliceAuth(), stub); got.OnboardingID != "onboard-real" {
		t.Fatalf("resolved %s", got.OnboardingID)
	}
	// No sibling: the removed stub itself is returned.
	only := stub
	only.OnboardingID = "onboard-lonely"
	only.SourceURL = "https://github.com/example/other"
	only.IdempotencyKey = ""
	if err := control.Store.PutOnboarding(only); err != nil {
		t.Fatal(err)
	}
	if got := control.latestPrepareRecord(aliceAuth(), only); got.OnboardingID != "onboard-lonely" {
		t.Fatalf("resolved %s", got.OnboardingID)
	}
}

func TestPrepareSelfInstallDedupesInFlightKeylessRetry(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		calls.Add(1)
		<-release
		return SourceReview{Definition: userMCPDefinition()}, nil
	})
	// No request_key: the deterministic stub id still dedupes the retry.
	args := map[string]any{"source": githubCommitURL()}
	first, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", args)
	if err != nil || first["phase"] != PhasePreparing {
		t.Fatalf("first=%v err=%v", first, err)
	}
	again, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", args)
	if err != nil || again["onboarding_id"] != first["onboarding_id"] {
		t.Fatalf("keyless retry=%v err=%v", again, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("keyless retry spawned %d reviews", calls.Load())
	}
	close(release)
}

func TestPrepareSelfInstallRetriesStalePreparing(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		calls.Add(1)
		<-release
		return SourceReview{Definition: userMCPDefinition()}, nil
	})
	// A stub older than the background deadline belongs to a crashed prepare;
	// the retry re-runs review+build instead of trusting it forever.
	stale := Onboarding{Schema: SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: OnboardingSelfInstall, Phase: PhasePreparing, Revision: 1, CreatedAt: time.Now().Add(-time.Hour),
		SourceURL: "https://github.com/example/mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		DefinitionID: "mcp", DefinitionVersion: "0.0.1"}
	stale.OnboardingID = deterministicID("onboard", "alice", "alice", "runtime", string(OnboardingSelfInstall)+":mcp@0.0.1:https://github.com/example/mcp:0123456789abcdef0123456789abcdef01234567")
	if err := control.Store.PutOnboarding(stale); err != nil {
		t.Fatal(err)
	}
	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL()})
	if err != nil || body["phase"] != PhasePreparing {
		t.Fatalf("stale retry=%v err=%v", body, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("stale preparing record did not retry: %d reviews", calls.Load())
	}
	close(release)
}

func TestPrepareSelfInstallConcurrentCallsShareOneReview(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	control := asyncFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		calls.Add(1)
		<-release
		return SourceReview{Definition: userMCPDefinition()}, nil
	})
	// Two truly concurrent identical prepares must not both run review+build:
	// ClaimPreparingOnboarding decides under the store lock.
	first := make(chan map[string]any, 1)
	go func() {
		body, _ := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL()})
		first <- body
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	second, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL()})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	a := <-first
	if calls.Load() != 1 {
		t.Fatalf("concurrent prepare spawned %d reviews", calls.Load())
	}
	// The responses may carry different ids: the fast caller gets the finished
	// record, the timed-out one the stub — which must resolve to the same
	// onboarding through the superseded_by pointer. The released review runs
	// in the background, so status is polled until the result lands.
	var st map[string]any
	deadline = time.Now().Add(10 * time.Second)
	for {
		var err error
		st, err = control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": second["onboarding_id"]})
		if err != nil {
			t.Fatal(err)
		}
		if st["phase"] == PhaseAwaitingConfirm || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st["phase"] != PhaseAwaitingConfirm {
		t.Fatalf("stub did not resolve to finished record: status=%v", st)
	}
	if st["onboarding_id"] == second["onboarding_id"] && st["onboarding_id"] != a["onboarding_id"] {
		t.Fatalf("status kept the superseded stub instead of the finished record: status=%v", st)
	}
	if a["phase"] == PhaseAwaitingConfirm && st["onboarding_id"] != a["onboarding_id"] {
		t.Fatalf("concurrent callers disagree on the finished record: first=%v status=%v", a, st)
	}
}

func TestPreparingRecordSurvivesStoreReload(t *testing.T) {
	store := NewStore()
	stub := Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-reload", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: OnboardingSelfInstall, Phase: PhasePreparing, Revision: 1, CreatedAt: time.Now(),
		SourceURL: "https://github.com/example/mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567", IdempotencyKey: "k-reload"}
	if err := store.PutOnboarding(stub); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "toolhub", "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	// A ToolHub restart reloads the file store: the in-flight prepare must be
	// visible instead of "record not found".
	fresh, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err := fresh.onboarding("onboard-reload")
	if err != nil || onboarding.Phase != PhasePreparing {
		t.Fatalf("reloaded onboarding=%+v err=%v", onboarding, err)
	}
	if _, ok := fresh.FindOnboardingByKey(aliceAuth(), "k-reload"); !ok {
		t.Fatal("request_key lost across reload")
	}
}

func TestStatusRejectsForeignSupersededPointer(t *testing.T) {
	control := asyncFixture(t, nil)
	real := Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-real", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: OnboardingSelfInstall, Phase: PhaseAwaitingConfirm, Revision: 1, CreatedAt: time.Now()}
	stub := real
	stub.OnboardingID = "onboard-stub"
	stub.Phase = PhaseRemoved
	stub.SupersededBy = "onboard-real"
	for _, record := range []Onboarding{real, stub} {
		if err := control.Store.PutOnboarding(record); err != nil {
			t.Fatal(err)
		}
	}
	// Bob must not follow alice's pointer — nor read either record.
	if _, err := control.Invoke(context.Background(), bobAuth(), "status", map[string]any{"onboarding_id": "onboard-stub"}); err == nil {
		t.Fatal("bob read alice's superseded stub")
	}
	// Alice's stub resolves through the pointer to the live record.
	st, err := control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": "onboard-stub"})
	if err != nil || st["onboarding_id"] != "onboard-real" || st["phase"] != PhaseAwaitingConfirm {
		t.Fatalf("superseded status=%v err=%v", st, err)
	}
}
