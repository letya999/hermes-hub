// Package hindsight_test pins the adapter against a fixture server speaking
// the Hindsight HTTP API verified at upstream pin 869228c (retain upsert by
// document_id, documents list+detail, recall, /health). Fixture verification
// is contract-level only — live protocol acceptance stays pending per
// CHG-0239 until a real deployment exists.
package hindsight_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/memorytest"
	"github.com/letya999/hermes-hub/internal/memory/providers/hindsight"
)

func open(t *testing.T) (*memorytest.HindsightFixture, *httptest.Server, memory.Provider) {
	t.Helper()
	fx, srv := memorytest.NewHindsightFixture(t)
	p, err := hindsight.New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return fx, srv, p
}

// The shared seam conformance suite must pass against the wire contract:
// scope isolation by bank_id, document_id idempotence and a provenance
// round-trip through document metadata (ADR-0086 D15, SPEC-0085 R11.3).
func TestConformanceHindsight(t *testing.T) {
	memorytest.Conformance(t, func(t *testing.T) memorytest.Provider {
		_, _, p := open(t)
		return p
	})
}

// A down or erroring endpoint reports degradation honestly — no fake empty
// results, no silent fallback (SPEC-0085 R11.4).
func TestHindsightDegradedHonest(t *testing.T) {
	fx, _, p := open(t)
	fx.SetDown(true)
	if err := p.Health(context.Background()); err == nil {
		t.Fatal("health of a down endpoint must fail")
	}
	fx.SetDown(false)
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("health of a live endpoint: %v", err)
	}
	// An empty bank is empty, not degraded: search on it returns no hits
	// and no error (empty result != unavailable).
	scope := memory.MintScope([]byte("m"), "prod", "alice", string(memory.AudiencePrivate))
	hits, err := p.Search(context.Background(), scope, memory.Query{Text: "anything", Limit: 5})
	if err != nil {
		t.Fatalf("empty-bank search must not error: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty bank returned %d hits", len(hits))
	}
	// Unsupported lanes stay honest.
	if _, err := p.Recall(context.Background(), scope); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Recall must report ErrUnsupported, got %v", err)
	}
	if _, err := p.Write(context.Background(), memory.WriteOp{}); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Write must report ErrUnsupported, got %v", err)
	}
	caps := p.Caps(context.Background())
	if !caps.Capture || !caps.Search || !caps.Export {
		t.Fatalf("caps %+v must advertise capture/search/export", caps)
	}
	if caps.Recall || caps.Write {
		t.Fatalf("caps %+v must not advertise recall/write", caps)
	}
}

// bank_id is the opaque scope key verbatim — the adapter never mints,
// derives or translates it, and two scopes can never see each other
// (SPEC-0085 R11.2).
func TestHindsightScopeIsolation(t *testing.T) {
	fx, _, p := open(t)
	material := []byte("isolation-material")
	scopeA := memory.MintScope(material, "prod", "alice", string(memory.AudiencePrivate))
	scopeB := memory.MintScope(material, "prod", "bob", string(memory.AudiencePrivate))
	e := memory.Entry{
		ID: "iso-1", Scope: scopeA, Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "alice private fact", ObservedAt: time.Now().UTC(),
	}
	if _, err := p.Capture(context.Background(), e); err != nil {
		t.Fatalf("capture: %v", err)
	}
	// The retain must have gone to bank scopeA.Key — assert on the wire.
	if len(fx.Docs(scopeA.Key)) != 1 {
		t.Fatalf("scope A bank holds %d docs, want 1", len(fx.Docs(scopeA.Key)))
	}
	if len(fx.Docs(scopeB.Key)) != 0 {
		t.Fatalf("scope B bank holds %d docs, want 0", len(fx.Docs(scopeB.Key)))
	}
	// A populated bank returns its facts as hits.
	hitsA, err := p.Search(context.Background(), scopeA, memory.Query{Text: "alice", Limit: 5})
	if err != nil {
		t.Fatalf("search A: %v", err)
	}
	if len(hitsA) != 1 || hitsA[0].Snippet != "alice private fact" {
		t.Fatalf("scope A hits: %+v", hitsA)
	}
	// Scope B can never see them.
	hitsB, err := p.Search(context.Background(), scopeB, memory.Query{Text: "alice", Limit: 5})
	if err != nil {
		t.Fatalf("search B: %v", err)
	}
	if len(hitsB) != 0 {
		t.Fatalf("scope B read %d hits of scope A content", len(hitsB))
	}
	// document_id upsert: the entry ID must arrive verbatim on the wire.
	retained := fx.Retained()
	last := retained[len(retained)-1]
	items := last["items"].([]any)
	if items[0].(map[string]any)["document_id"] != "iso-1" {
		t.Fatalf("document_id not the entry id: %v", items[0])
	}
}

// Hub-owned fields ride item metadata verbatim — provenance, kind, audience
// and source_ref survive a capture/export round trip untouched
// (SPEC-0085 R11.3).
func TestHindsightProvenanceMetadata(t *testing.T) {
	fx, _, p := open(t)
	scope := memory.MintScope([]byte("m"), "prod", "alice", string(memory.AudiencePrivate))
	e := memory.Entry{
		ID: "prov-1", Scope: scope, Kind: memory.KindRoutine,
		Provenance: memory.ProvenanceUntrusted, Audience: memory.AudiencePrivate,
		SourceRef: "src-9", Body: "untrusted body", ObservedAt: time.Now().UTC(),
	}
	if _, err := p.Capture(context.Background(), e); err != nil {
		t.Fatalf("capture: %v", err)
	}
	md := fx.Docs(scope.Key)["prov-1"]["document_metadata"].(map[string]string)
	for _, k := range []string{"hub_provenance", "hub_kind", "hub_audience", "hub_source_ref"} {
		if md[k] == "" {
			t.Fatalf("metadata key %s missing from retain payload: %v", k, md)
		}
	}
	exported, err := p.Export(context.Background(), scope)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported) != 1 {
		t.Fatalf("export returned %d entries, want 1", len(exported))
	}
	got := exported[0]
	if got.ID != e.ID || got.Body != e.Body || got.Provenance != e.Provenance ||
		got.Kind != e.Kind || got.Audience != e.Audience || got.SourceRef != e.SourceRef {
		t.Fatalf("export lost hub fields: %+v", got)
	}
	if got.Scope.Key != scope.Key {
		t.Fatalf("export stamped scope %q, want %q", got.Scope.Key, scope.Key)
	}
}

// A scope key with path separators must never reach the URL — MintScope
// emits hex today, but the adapter enforces the contract regardless of
// caller so a future key shape can never traverse the bank path.
func TestHindsightScopeKeySanitized(t *testing.T) {
	_, _, p := open(t)
	for _, bad := range []string{"../../admin", "a/b", "a..b", "", "x%2f.."} {
		if _, err := p.Search(context.Background(), memory.Scope{Key: bad}, memory.Query{Text: "x"}); err == nil {
			t.Fatalf("scope key %q must be refused", bad)
		}
		if _, err := p.Export(context.Background(), memory.Scope{Key: bad}); err == nil {
			t.Fatalf("export scope key %q must be refused", bad)
		}
		if _, err := p.Capture(context.Background(), memory.Entry{ID: "e", Scope: memory.Scope{Key: bad}}); err == nil {
			t.Fatalf("capture scope key %q must be refused", bad)
		}
	}
}

// Export must page the documents list — a bank larger than one page still
// round-trips complete (SPEC-0085 R11.5 replay parity).
func TestHindsightExportPagination(t *testing.T) {
	fx, _, p := open(t)
	scope := memory.MintScope([]byte("m"), "prod", "alice", string(memory.AudiencePrivate))
	bank := map[string]map[string]any{}
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("doc-%04d", i)
		bank[id] = map[string]any{
			"id": id, "original_text": "body " + id,
			"document_metadata": map[string]string{"hub_entry_id": id},
			"created_at":        time.Now().UTC().Format(time.RFC3339),
		}
	}
	fx.SetDocs(scope.Key, bank)
	exported, err := p.Export(context.Background(), scope)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported) != 250 {
		t.Fatalf("export returned %d entries, want 250 (paged)", len(exported))
	}
}

// The endpoint must be an absolute http(s) URL — a mistyped config fails
// at construction, not at first call (SPEC-0085 R11.4 honest degradation).
func TestHindsightNewRejectsBadEndpoint(t *testing.T) {
	for _, bad := range []string{"", "not a url", "ftp://x", "/relative/path"} {
		if _, err := hindsight.New(bad); err == nil {
			t.Fatalf("endpoint %q must be rejected", bad)
		}
	}
	if _, err := hindsight.New("http://hindsight.local:8888/"); err != nil {
		t.Fatalf("valid endpoint rejected: %v", err)
	}
}

// An explicitly granted cloud endpoint carries the bearer credential on the
// wire — resolved by the hub from secrets, never in rendered config.
func TestHindsightTokenSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	p, err := hindsight.New(srv.URL, hindsight.WithToken("tok-1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("authorization header %q, want Bearer tok-1", gotAuth)
	}
}

// Query.Limit trims the hit list — the wire API has no result-count cap, so
// the seam's contract is enforced client-side.
func TestHindsightSearchLimit(t *testing.T) {
	_, _, p := open(t)
	scope := memory.MintScope([]byte("m"), "prod", "alice", string(memory.AudiencePrivate))
	for i := 0; i < 3; i++ {
		_, err := p.Capture(context.Background(), memory.Entry{
			ID: fmt.Sprintf("lim-%d", i), Scope: scope, Kind: memory.KindInteractive,
			Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
			Body: fmt.Sprintf("fact %d", i), ObservedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("capture: %v", err)
		}
	}
	hits, err := p.Search(context.Background(), scope, memory.Query{Text: "fact", Limit: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("limit 2 returned %d hits", len(hits))
	}
}
