package toolhub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func browserDefinition() api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "browser", Version: "1.0.0", Transport: api.ContainerMCP,
		Source:    api.DefinitionSource{Image: "ghcr.io/o/browser", Digest: "sha256:" + strings.Repeat("a", 64)},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Stateful: true, Rationale: "browser profile", ToolHiveVersion: "v0.12.1", SidecarImages: []string{"ghcr.io/o/squid@sha256:" + strings.Repeat("b", 64)}},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 300, OutputBytes: 8192, CPUMillis: 1000, MemoryMiB: 2048, MaxPIDs: 256, EgressClass: api.EgressWeb},
		Tools:     []api.ToolSpec{{Name: "navigate", Effect: api.ReadEffect}},
		Health:    api.HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
	}
}

func browserConnection() api.Connection {
	return api.Connection{
		Schema: api.SchemaVersion, ConnectionID: "conn-browser", DefinitionID: "browser",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, Revision: 1, Status: api.ActiveStatus,
	}
}

// S8.3: the viewer link is one-time, owner-bound and short-TTL — mint by a
// non-owner or a non-browser connection fails closed, a spent or expired
// link reports gone, a wrong nonce is unauthorized.
func TestViewerLinkLifecycle(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nonce, expires, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-1", now, 10*time.Minute)
	if err != nil || nonce == "" || !expires.After(now) {
		t.Fatalf("mint: nonce=%q expires=%v err=%v", nonce, expires, err)
	}
	if _, _, err := store.MintViewerLink(bobAuth(), "conn-browser", "op-1", now, 10*time.Minute); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("non-owner mint: %v", err)
	}
	if _, err := store.SpendViewerLink("conn-browser", "wrong-nonce", now); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("wrong nonce: %v", err)
	}
	if _, err := store.SpendViewerLink("conn-browser", nonce, now); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if _, err := store.SpendViewerLink("conn-browser", nonce, now); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("second spend: %v", err)
	}
	nonce2, _, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-2", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SpendViewerLink("conn-browser", nonce2, now.Add(2*time.Minute)); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("expired spend: %v", err)
	}
	// A non-web connection can never carry a viewer link.
	other := browserConnection()
	other.ConnectionID = "conn-plain"
	other.DefinitionID = "generic-mcp"
	def := browserDefinition()
	def.DefinitionID = "generic-mcp"
	def.Execution.EgressClass = ""
	def.Execution.Egress = []string{"api.example.com"}
	if err := store.RegisterDefinition(def); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MintViewerLink(aliceAuth(), "conn-plain", "op-3", now, time.Minute); err == nil {
		t.Fatal("viewer minted for a bounded-egress connection")
	}
}

// S8.3: GET /viewer/<id>?t=<nonce> serves the owner viewer once — a repeat
// visit gets 410 gone, a wrong nonce 403.
func TestServeViewerLinkContract(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	nonce, _, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-1", time.Now(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/conn-browser?t="+nonce, nil)
	req.Host = "127.0.0.1"
	gateway.serveViewer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer status=%d body=%s", rec.Code, rec.Body.String())
	}
	again := httptest.NewRecorder()
	gateway.serveViewer(again, req)
	if again.Code != http.StatusGone {
		t.Fatalf("reused link status=%d", again.Code)
	}
	forged := httptest.NewRecorder()
	forgedReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/conn-browser?t=forged", nil)
	forgedReq.Host = "127.0.0.1"
	gateway.serveViewer(forged, forgedReq)
	if forged.Code != http.StatusForbidden {
		t.Fatalf("forged nonce status=%d", forged.Code)
	}
}

// S8.3: connector_status on a browser connection mints the one-time viewer
// link and posts it to the owner's channel — the tool result itself carries
// no link material.
func TestConnectorStatusMintsViewerLink(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOnboarding(api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-browser", PrincipalID: "alice",
		ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseEnabled,
		DefinitionID: "browser", DefinitionVersion: "1.0.0", ConnectionID: "conn-browser",
	}); err != nil {
		t.Fatal(err)
	}
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&posted)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "test-token")
	t.Setenv("HUB_PUBLIC_ORIGIN", "https://hub.example.com")
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	result, err := control.connectorStatus(t.Context(), aliceAuth(), "conn-browser", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Summary, "t=") || strings.Contains(result.Summary, "/viewer/") {
		t.Fatalf("link material leaked into tool result: %q", result.Summary)
	}
	viewerURL, _ := posted["viewer_url"].(string)
	if !strings.Contains(viewerURL, "/viewer/conn-browser?t=") {
		t.Fatalf("notice missing viewer url: %v", posted)
	}
	// The minted link is spendable exactly once.
	if _, err := store.SpendViewerLink("conn-browser", strings.Split(viewerURL, "t=")[1], time.Now()); err != nil {
		t.Fatalf("minted link not spendable: %v", err)
	}
}
