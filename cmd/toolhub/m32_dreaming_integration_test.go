//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/commops"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/night"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
	"github.com/letya999/hermes-hub/internal/vault"
)

// Uses production ToolHub, relay, PDP, vault and automation constructors. Only
// upstream model/channel I/O and the supplied clock tick are controlled here.
func TestM32DreamingRelayIsolationAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var calls [2]atomic.Int32
	users := []string{"m32adream", "m32bdream"}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(contract.ModelServiceTokenHeader) != "" {
			t.Error("provider received the dreaming proof")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || r.URL.Path != "/v1/chat/completions" {
			t.Error("unexpected dreaming upstream request")
			w.WriteHeader(400)
			return
		}
		owner := -1
		for i, user := range users {
			if strings.Contains(string(body), "M32-dream-private-"+user) {
				if owner != -1 {
					t.Error("dreaming prompt contains both owners' private entries")
					w.WriteHeader(403)
					return
				}
				owner = i
			}
		}
		if owner == -1 {
			t.Error("dreaming relay omitted the eligible owner entry")
			w.WriteHeader(400)
			return
		}
		count := calls[owner].Add(1)
		if owner == 0 && count == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		marker := "M32-dream-private-" + users[owner]
		proposal, err := json.Marshal(map[string]any{
			"summary": marker,
			"facts":   []map[string]string{{"card": "notes/m32-dream", "fact": marker, "quote": marker}},
			"intents": []map[string]string{{"text": "Review " + marker, "quote": marker}},
		})
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": string(proposal)}}}})
	}))
	t.Cleanup(provider.Close)
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", provider.URL+"/v1", "dream")
	m32Require(t, err)
	const dreamingAuth = "independent-fixture-dreaming"
	h := newM32LifecycleWithDreaming(t, ctx, "", "prod", f, dreamingAuth)
	runs := map[string]api.RunContext{}
	for _, user := range f.Users {
		runs["job-dream-read-"+user] = api.RunContext{JobID: "job-dream-read-" + user, RunID: "run-dream-read-" + user,
			RuntimeGeneration: "dream-gen", CurrentGeneration: "dream-gen", Environment: "prod", Status: "running",
			ScopeID: "user:" + user, Trigger: "message", Channel: "telegram_bot", UserID: user, Envelope: f.Auth[user]}
	}
	control := http.NewServeMux()
	control.Handle("/v1/identity/", h.manager.Handler())
	control.HandleFunc("/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		run, ok := runs[strings.TrimPrefix(r.URL.Path, "/v1/jobs/")]
		if r.Method != http.MethodGet || !ok || r.Header.Get("Authorization") != "Bearer "+h.control {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
	})
	authority := httptest.NewServer(control)
	t.Cleanup(authority.Close)
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", authority.URL)
	t.Setenv(contract.DreamingServiceAuthEnv, dreamingAuth)
	t.Setenv("HUB_RUN_VERIFY_URL", authority.URL)
	t.Setenv("HUB_RUN_VERIFY_AUTH", task.VerificationCredential(h.control))
	t.Setenv("HUB_TOOLHUB_URL", h.toolURL)
	t.Setenv("HUB_TOOLHUB_CONTROL", h.control)
	t.Setenv("HUB_ENVIRONMENT", "prod")
	t.Setenv("HUB_CREDENTIAL_BROKER_MODEL_URL", "")
	t.Setenv("HUB_CREDENTIAL_BROKER_MODEL_TOKEN", "")

	// Resolve only the rendered private relay aliases to native loopback servers.
	// Requests use the real issuer, verifier and relay PDP.
	relays := map[string]string{}
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if target, ok := relays[address]; ok {
			address = target
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	for _, user := range f.Users {
		dir := filepath.Join(f.Root, "spaces", user)
		auth, err := stack.ReadSecrets(filepath.Join(dir, "model-relay.auth"))
		m32Require(t, err)
		for key, value := range map[string]string{
			"HUB_MODEL_RELAY_AUTH": auth["HUB_MODEL_RELAY_AUTH"], "HUB_MODEL_RELAY_ADMIN": auth["HUB_MODEL_RELAY_ADMIN"],
			"HUB_MODEL_RELAY_REGISTRY": filepath.Join(dir, "generated", "model-registry", "prod", "models.yaml"),
			"HUB_MODEL_RELAY_STATE":    filepath.Join(dir, "managed", "prod", "model-relay", "model-select.json"), "HUB_MODEL_RELAY_OWNER": user,
		} {
			t.Setenv(key, value)
		}
		handler, err := hubruntime.ModelRelayHandlerFromEnv()
		m32Require(t, err)
		relay := httptest.NewServer(handler)
		t.Cleanup(relay.Close)
		relays["model-relay-"+user+":8318"] = relay.Listener.Addr().String()
		zone := time.UTC
		if user == f.Users[1] {
			zone, err = time.LoadLocation("America/New_York")
			m32Require(t, err)
		}
		daily, err := vault.OpenDaily(filepath.Join(f.Root, "vaults", user), zone)
		m32Require(t, err)
		m32Require(t, daily.Append(memory.Entry{ID: "m32-dream-" + user, Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
			Body: "M32-dream-private-" + user, ObservedAt: time.Date(2026, 3, 7, 12, 0, 0, 0, zone)}, "telegram_bot"))
	}
	// The older memory-read scenarios in this fixture have synthetic run records.
	t.Setenv("HUB_RUN_VERIFY_URL", "")
	t.Setenv("HUB_RUN_VERIFY_AUTH", "")
	config, err := toolhub.EndpointConfigFromEnv()
	m32Require(t, err)
	store, err := toolhub.Load(f.StorePath)
	m32Require(t, err)
	docs, err := catalog.Generate()
	m32Require(t, err)
	store.RightsDoc = docs.Rights
	handler, err := toolhub.NewEndpointHandler(config, store)
	m32Require(t, err)
	projection := handler.(toolhub.AutomationProjection)
	var current atomic.Pointer[automation.Host]
	restart := func() *automation.Host {
		t.Helper()
		if old := current.Load(); old != nil {
			m32Require(t, old.Close())
		}
		host, err := toolhub.NewAutomationHost(ctx, projection, store, nil)
		m32Require(t, err)
		current.Store(host)
		return host
	}
	host := restart()
	t.Cleanup(func() { m32Require(t, current.Load().Close()) })
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/automation/") {
			current.Load().ControlHandler(h.control).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	m32Require(t, server.Listener.Close())
	server.Listener, err = net.Listen("tcp", config.Listen)
	m32Require(t, err)
	server.Start()
	t.Cleanup(server.Close)
	h.communicationOnline.Store(false)
	gov := toolhub.NewGovernanceStore(filepath.Join(filepath.Dir(f.StorePath), "governance.json"))
	m32Require(t, gov.RevokeRight(api.DeterministicID("grant", f.Users[0], "memory", "dreaming"), "operator", "M32 admission fence", time.Now()))
	// Before the gap, New York is still at 01:59 and must not mint today's day.
	beforeGap := time.Date(2026, 3, 8, 6, 59, 0, 0, time.UTC)
	m32Require(t, host.Tick(ctx, beforeGap))
	if _, err := os.Stat(filepath.Join(f.Root, "owners", f.Users[0], "system", "dreaming", "2026-03-08.json")); !os.IsNotExist(err) {
		t.Fatal("revoked owner obtained a dreaming day checkpoint")
	}
	if _, err := os.Stat(filepath.Join(f.Root, "owners", f.Users[1], "system", "dreaming", "2026-03-08.json")); !os.IsNotExist(err) || calls[0].Load() != 0 || calls[1].Load() != 0 {
		t.Fatal("dreaming ran before the local due time or without its right")
	}
	grantDreaming := func(id string) {
		t.Helper()
		grant := api.NewRightGrant(id, f.Users[0], "memory", "dreaming", "", "M32 recovery", "operator", time.Now().Add(time.Hour))
		m32Require(t, api.Confirm(&grant, "operator", time.Now()))
		m32Require(t, gov.GrantRight(grant, docs.Rights, time.Now()))
	}
	grantDreaming("grant-m32-dream-admission")
	// 02:30 does not exist in New York today; the first post-gap tick is due.
	now := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	finished := make(chan error, 1)
	go func() { finished <- host.Tick(ctx, now) }()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("dreaming finished before reaching the model provider: %v", err)
	case <-ctx.Done():
		t.Fatal("dreaming did not reach the model provider")
	}
	gitHead := func(user string) string {
		t.Helper()
		body, err := exec.CommandContext(ctx, "git", "-C", filepath.Join(f.Root, "vaults", user), "rev-parse", "HEAD").Output()
		m32Require(t, err)
		return string(body)
	}
	beforeRevocation := gitHead(f.Users[0])
	dayPath := func(user string) string {
		return filepath.Join(f.Root, "owners", user, "system", "dreaming", "2026-03-08.json")
	}
	complete := func(user string) bool {
		data, err := os.ReadFile(dayPath(user))
		if err != nil {
			return false
		}
		var day struct {
			PrincipalID string `json:"principal_id"`
			RunDate     string `json:"run_date"`
			LeafDate    string `json:"leaf_date"`
			State       string `json:"state"`
		}
		m32Require(t, json.Unmarshal(data, &day))
		return day.PrincipalID == user && day.RunDate == "2026-03-08" && day.LeafDate == "2026-03-07" && day.State == "complete"
	}
	m32Wait(t, ctx, 15*time.Second, func() bool { return complete(f.Users[1]) })
	m32Require(t, gov.RevokeRight("grant-m32-dream-admission", "operator", "M32 model barrier", time.Now()))
	unblock()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("revoked dreaming completed its commit")
		}
	case <-ctx.Done():
		t.Fatal("dreaming did not release the revoked owner")
	}
	if complete(f.Users[0]) || night.DayComplete(filepath.Join(f.Root, "vaults", f.Users[0]), "2026-03-07") || beforeRevocation != gitHead(f.Users[0]) {
		t.Fatal("revoked owner persisted a completed night")
	}
	for _, rel := range []string{"memory/cards/notes/m32-dream.md", "runtime/memory/night/2026-03-07.json"} {
		if _, err := os.Stat(filepath.Join(f.Root, "vaults", f.Users[0], filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatal("revocation barrier allowed a vault write")
		}
	}
	grantDreaming("grant-m32-dream-recovery")
	host = restart()
	m32Require(t, host.Tick(ctx, now))
	for i, user := range f.Users {
		if !complete(user) || !night.DayComplete(filepath.Join(f.Root, "vaults", user), "2026-03-07") {
			t.Fatal("authorized recovery did not complete each owner day")
		}
		card, err := os.ReadFile(filepath.Join(f.Root, "vaults", user, "memory", "cards", "notes", "m32-dream.md"))
		m32Require(t, err)
		if strings.Count(string(card), "M32-dream-private-"+user) != 1 || strings.Contains(string(card), "M32-dream-private-"+f.Users[1-i]) {
			t.Fatal("dreaming fact duplicated or crossed owner vaults")
		}
		var search struct {
			Hits []struct{ ID, Evidence string } `json:"hits"`
		}
		var reply struct {
			Structured json.RawMessage `json:"structuredContent"`
		}
		m32Require(t, json.Unmarshal([]byte(h.call(t, ctx, user, runs["job-dream-read-"+user], "memory_search", map[string]any{"query": "M32-dream-private-" + user}, true)), &reply))
		m32Require(t, json.Unmarshal(reply.Structured, &search))
		cardID := ""
		for _, hit := range search.Hits {
			if strings.Contains(hit.Evidence, "m32-dream.md") {
				cardID = hit.ID
			}
		}
		if cardID == "" {
			t.Fatal("dreaming result was not searchable through ToolHub")
		}
		read := h.call(t, ctx, user, runs["job-dream-read-"+user], "memory_read", map[string]any{"id": cardID}, true)
		if !strings.Contains(read, "M32-dream-private-"+user) || strings.Contains(read, "M32-dream-private-"+f.Users[1-i]) {
			t.Fatal("ToolHub read lost the dreaming result or crossed owner vaults")
		}
		items, err := host.ListAssignments(ctx, f.Auth[user])
		m32Require(t, err)
		if len(items) != 1 || items[0].State != "draft" || items[0].Plan != nil || len(items[0].Applied) != 0 || items[0].Envelope.PrincipalID != user {
			t.Fatal("dreaming intent activated work or lost its owner")
		}
		schedules, err := host.ListSchedules(ctx, f.Auth[user])
		m32Require(t, err)
		if len(schedules) != 0 {
			t.Fatal("protected dreaming or its intent leaked into ordinary schedules")
		}
		control := commops.ControlClient{URL: h.toolURL, Token: h.control}
		if err := control.Call(ctx, f.Auth[user], "schedule.pause", map[string]any{"id": "system-dreaming", "expected_revision": 1}, nil); err == nil {
			t.Fatal("ordinary ToolHub control paused the protected system routine")
		}
	}
	pending := h.deliveryRecords(t)
	digestCount := 0
	for _, record := range pending {
		if strings.HasPrefix(record.Delivery.ID, "night-digest-") {
			digestCount++
			if record.Sent {
				t.Fatal("communication outage accepted a digest")
			}
		}
	}
	if digestCount != 2 {
		t.Fatal("communication outage lost or accepted a digest")
	}
	heads := []string{gitHead(f.Users[0]), gitHead(f.Users[1])}
	host = restart()
	m32Require(t, host.Tick(ctx, now.Add(time.Minute)))
	digestCount = 0
	for _, record := range h.deliveryRecords(t) {
		if strings.HasPrefix(record.Delivery.ID, "night-digest-") {
			digestCount++
		}
	}
	if calls[0].Load() != 2 || calls[1].Load() != 1 || digestCount != 2 {
		t.Fatal("restart repeated a completed model call or minted another digest")
	}
	for i, user := range f.Users {
		items, err := host.ListAssignments(ctx, f.Auth[user])
		m32Require(t, err)
		if gitHead(user) != heads[i] || len(items) != 1 {
			t.Fatal("completed-day replay changed the vault or duplicated its intent")
		}
	}
	h.communicationOnline.Store(true)
	m32Require(t, host.Tick(ctx, now.Add(2*time.Minute)))
	for _, record := range h.deliveryRecords(t) {
		if strings.HasPrefix(record.Delivery.ID, "night-digest-") && !record.Sent {
			t.Fatal("recovered communication did not accept the durable digest")
		}
	}
	// The actual producer pump sends only to the local channel fixture.
	pumpCtx, stopPump := context.WithCancel(ctx)
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- h.gateway.Run(pumpCtx) }()
	defer func() {
		stopPump()
		select {
		case err := <-pumpDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("dreaming delivery producer did not stop")
		}
	}()
	seen := map[int64]bool{}
	for range f.Users {
		select {
		case chat := <-h.channelSends:
			if seen[chat] || chat != 11 && chat != 12 {
				t.Fatal("digest duplicated or crossed its channel")
			}
			seen[chat] = true
		case <-ctx.Done():
			t.Fatal("digest did not reach the local channel fixture")
		}
	}
	host = restart()
	m32Require(t, host.Tick(ctx, now.Add(3*time.Minute)))
	select {
	case <-h.channelSends:
		t.Fatal("restart replayed an acknowledged digest")
	case <-time.After(500 * time.Millisecond):
	}
}
