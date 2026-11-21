package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/conformance"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/api"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store/storetest"
	"github.com/letya999/hermes-hub/shared/testkit"
)

var contractTenant = spawnv1.Tenant{Org: "org-a", Owner: "alice"}

// stubBackend satisfies backend.Backend without touching the host: Start
// answers a ready workload whose address is keyed by the spec's content
// identity; Inspect/List serve the recorded specs.
type stubBackend struct{ specs map[string]backend.Spec }

func (s *stubBackend) Start(_ context.Context, spec backend.Spec) (backend.Runtime, error) {
	if s.specs == nil {
		s.specs = map[string]backend.Spec{}
	}
	s.specs[spec.Key] = spec
	return backend.Runtime{
		ID: spec.Key, Space: spec.Space, Agent: spec.Agent,
		State: "ready", Address: "stub://" + spec.Key + ":8080",
	}, nil
}

func (s *stubBackend) Stop(_ context.Context, id string) error {
	delete(s.specs, id)
	return nil
}

func (s *stubBackend) Inspect(_ context.Context, id string) (backend.Runtime, error) {
	spec, ok := s.specs[id]
	if !ok {
		return backend.Runtime{}, backend.ErrNotFound
	}
	return backend.Runtime{
		ID: spec.Key, Space: spec.Space, Agent: spec.Agent,
		State: "ready", Address: "stub://" + spec.Key + ":8080",
	}, nil
}

func (s *stubBackend) List(ctx context.Context) ([]backend.Runtime, error) {
	out := []backend.Runtime{}
	for id := range s.specs {
		rt, err := s.Inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, nil
}

func (s *stubBackend) Exec(context.Context, string, backend.ExecSpec) (backend.ExecResult, error) {
	return backend.ExecResult{}, nil
}

func (s *stubBackend) VerifyWorker(context.Context, string, backend.Spec) error { return nil }

func contractServer(t *testing.T, url string) api.Contract {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatalf("catalog generate: %v", err)
	}
	clock := time.Unix(1_800_000_000, 0)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }
	st := storetest.Open(t, url, migrations.FS)
	return api.Contract{
		// SpacesRoot points the transitional mirror and the settings
		// edit's authored-file write-back at a throwaway tree.
		Revisions: &spawn.Revisions{Store: st, Catalog: docs, Now: now, SpacesRoot: t.TempDir()},
		Runtimes: &spawn.Runtimes{
			Store: st, Backend: &stubBackend{}, Image: "harness-runtime",
			Now: now, OCIRuntime: "runc",
		},
	}
}

// activateFixture runs the full space lifecycle — create, submit the
// fixture layers, validate, activate — so the space's effective digest is
// stored and runtime claims resolve.
func activateFixture(t *testing.T, client spawnv1.Client, tenant spawnv1.Tenant, space, keyPrefix string) {
	t.Helper()
	ctx := context.Background()
	if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{
		Org: tenant.Org, Owner: tenant.Owner, ID: space, Environment: "dev", IdempotencyKey: keyPrefix + "-create",
	}); err != nil {
		t.Fatalf("create space: %v", err)
	}
	ids := map[string]string{}
	fresh := map[string]bool{}
	for _, layer := range spawnv1.Layers {
		docs, ok := fixtureLayers()[layer]
		if !ok {
			continue
		}
		rev, err := client.SubmitRevision(ctx, space, layer, spawnv1.SubmitRevisionRequest{
			Org: tenant.Org, Owner: tenant.Owner, Documents: docs, IdempotencyKey: keyPrefix + "-s-" + layer,
		})
		if err != nil {
			t.Fatalf("submit %s: %v", layer, err)
		}
		fresh[layer] = rev.State == "draft"
		ids[layer] = rev.ID
	}
	var all []string
	for _, layer := range spawnv1.Layers {
		id, ok := ids[layer]
		if !ok {
			continue
		}
		if fresh[layer] {
			if _, err := client.ValidateRevision(ctx, space, id, spawnv1.RevisionRequest{
				Org: tenant.Org, Owner: tenant.Owner, IdempotencyKey: keyPrefix + "-v-" + layer,
			}); err != nil {
				t.Fatalf("validate %s: %v", layer, err)
			}
		}
		all = append(all, id)
	}
	if _, err := client.ActivateRevisions(ctx, space, spawnv1.ActivationRequest{
		Org: tenant.Org, Owner: tenant.Owner, Revisions: all, IdempotencyKey: keyPrefix + "-a-all",
	}); err != nil {
		t.Fatalf("activate set: %v", err)
	}
}

// Tenant isolation on the real service: identical space ids and agent
// names under different tenants never resolve to the same runtime row,
// and a foreign tenant cannot touch the owner's lease.
func TestRuntimeClaimNeverCrossesTenants(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		server := contractServer(t, url)
		testkit.Transports(t, spawnv1.NewInProc(server), spawnv1.Handler(server, "tok"),
			func(u string, hc *http.Client) spawnv1.Client { return spawnv1.NewHTTPClient(u, "tok", hc) },
			func(t *testing.T, client spawnv1.Client) {
				ctx := context.Background()
				foreign := spawnv1.Tenant{Org: "org-b", Owner: "mallory"}
				activateFixture(t, client, contractTenant, "alice", "k1")
				activateFixture(t, client, foreign, "alice", "k2")
				mine, err := client.ClaimRuntime(ctx, "alice", spawnv1.ClaimRuntimeRequest{
					Org: contractTenant.Org, Owner: contractTenant.Owner, Agent: "main",
				})
				if err != nil {
					t.Fatalf("owner claim: %v", err)
				}
				theirs, err := client.ClaimRuntime(ctx, "alice", spawnv1.ClaimRuntimeRequest{
					Org: foreign.Org, Owner: foreign.Owner, Agent: "main",
				})
				if err != nil {
					t.Fatalf("foreign claim: %v", err)
				}
				if mine.Runtime.ID == theirs.Runtime.ID || mine.Lease == theirs.Lease {
					t.Fatalf("identical space+agent under different tenants must not share a runtime or lease: %s %s",
						mine.Runtime.ID, mine.Lease)
				}
				if _, err := client.RenewLease(ctx, mine.Lease, spawnv1.LeaseRequest(foreign)); !errors.Is(err, spawnv1.ErrNotFound) {
					t.Fatalf("foreign renew: want not_found, got %v", err)
				}
				if err := client.ReleaseLease(ctx, mine.Lease, spawnv1.LeaseRequest(foreign)); err != nil {
					t.Fatalf("foreign release must be a no-op: %v", err)
				}
				if _, err := client.RenewLease(ctx, mine.Lease, spawnv1.LeaseRequest(contractTenant)); err != nil {
					t.Fatalf("owner renew after foreign release: %v", err)
				}
			})
	})
}

// fixtureLayers mirrors the normative passport fixture: a managed space
// compiles once the four platform/org layers and the user layer are in.
func fixtureLayers() map[string]map[string]map[string]any {
	return map[string]map[string]map[string]any{
		"platform-ceiling": {
			"stack": {
				"identity": map[string]any{"user": "alice", "organization": "acme"},
				"services": map[string]any{
					"hermes-runtime":    map[string]any{"mode": "supervised"},
					"browser":           map[string]any{"mode": "per-space"},
					"toolhub":           map[string]any{"mode": "shared"},
					"model-relay":       map[string]any{"mode": "shared"},
					"communication-hub": map[string]any{"mode": "shared"},
					"credential-broker": map[string]any{"mode": "shared"},
					"supervisor":        map[string]any{"mode": "shared"},
					"cliproxy":          map[string]any{"mode": "per-space"},
					"hub-stt":           map[string]any{"mode": "shared"},
					"hub-tts":           map[string]any{"mode": "shared"},
				},
			},
			"config": {
				"memory": map[string]any{"backend": []any{"local", "honcho"}},
			},
			"tools": {"core": map[string]any{
				"native:file": map[string]any{"via": "native"},
				"files":       map[string]any{"via": "toolhub"},
				"terminal":    map[string]any{"via": "toolhub"},
			}},
			"workspace": {
				"writable":  []any{"/workspace", "/vault"},
				"read_only": []any{"/config", "/skills", "/shared"},
				"layout": []any{
					"/workspace/inbox/people", "/workspace/inbox/services",
					"/workspace/work", "/workspace/projects",
					"/workspace/artifacts/documents", "/workspace/artifacts/images",
					"/workspace/artifacts/videos", "/workspace/artifacts/archives",
					"/workspace/artifacts/services", "/workspace/outbox",
				},
			},
			"models": {
				"services": map[string]any{
					"cliproxy":   map[string]any{"via": "cliproxy"},
					"direct-key": map[string]any{"via": "openai-compatible"},
				},
				"routes": map[string]any{
					"everyday": map[string]any{"ladder": []any{"cliproxy"}},
				},
			},
			"grants": {"allow": []any{"workspace", "tools", "config"}},
		},
		"platform-template": {
			"config": {
				"memory":  map[string]any{"backend": "local"},
				"runtime": map[string]any{"timezone": "UTC"},
				"ports":   map[string]any{"browser-vnc": 6080},
				"supervision": map[string]any{
					"watchers": map[string]any{
						"nightly-rollup": map[string]any{"protected": true},
					},
				},
			},
			"tools":     {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"workspace": {"writable": []any{"/workspace"}, "read_only": []any{"/config", "/skills"}},
			"models": {
				"default": "everyday",
				"services": map[string]any{
					"cliproxy": map[string]any{
						"via":      "cliproxy",
						"endpoint": "http://cliproxy:8317/v1",
						"auth": map[string]any{
							"upstream":     map[string]any{"kind": "oauth-session", "ref": "cliproxy-main"},
							"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy"},
						},
					},
				},
			},
		},
		"org-ceiling": {
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": []any{"workspace", "tools"}},
		},
		"org-template": {
			"config":    {"memory": map[string]any{"backend": "honcho"}},
			"workspace": {"writable": []any{"/workspace"}},
		},
		"user": {
			"config": {"memory": map[string]any{"backend": "local"}, "runtime": map[string]any{"timezone": "Europe/Berlin"}},
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": map[string]any{
				"workspace": []any{"read", "write"},
				"tools":     []any{"list", "use"},
			}},
		},
	}
}

// The real service satisfies the spawn/v1 suite on both transports.
func TestContractConformanceInProcAndHTTP(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		server := contractServer(t, url)
		conformance.Both(t, server,
			func(token string) http.Handler { return spawnv1.Handler(server, token) },
			conformance.Fixture{
				Tenant:   contractTenant,
				Foreign:  spawnv1.Tenant{Org: "org-b", Owner: "mallory"},
				Space:    "alice",
				Consumer: "compose",
				Layers:   fixtureLayers(),
			})
	})
}

// The policy route serves immutable content: Cache-Control pins the
// response for a year, the ETag carries the digest, a foreign tenant's
// digest is not_found and an unknown consumer is not_found.
func TestPolicySliceByDigestImmutable(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		server := contractServer(t, url)
		const token = "test-token"
		srv := httptest.NewServer(spawnv1.Handler(server, token))
		defer srv.Close()
		client := spawnv1.NewHTTPClient(srv.URL, token, srv.Client())
		ctx := context.Background()

		if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{
			Org: contractTenant.Org, Owner: contractTenant.Owner, ID: "alice", Environment: "dev", IdempotencyKey: "k1",
		}); err != nil {
			t.Fatalf("create space: %v", err)
		}
		// Submit every layer before any validate: a layer compiles against
		// the newest-known docs of the others.
		ids := map[string]string{}
		for _, layer := range spawnv1.Layers {
			docs, ok := fixtureLayers()[layer]
			if !ok {
				continue
			}
			rev, err := client.SubmitRevision(ctx, "alice", layer, spawnv1.SubmitRevisionRequest{
				Org: contractTenant.Org, Owner: contractTenant.Owner, Documents: docs, IdempotencyKey: "ks-" + layer,
			})
			if err != nil {
				t.Fatalf("submit %s: %v", layer, err)
			}
			ids[layer] = rev.ID
		}
		var all []string
		for _, layer := range spawnv1.Layers {
			id, ok := ids[layer]
			if !ok {
				continue
			}
			if _, err := client.ValidateRevision(ctx, "alice", id, spawnv1.RevisionRequest{
				Org: contractTenant.Org, Owner: contractTenant.Owner, IdempotencyKey: "kv-" + layer,
			}); err != nil {
				t.Fatalf("validate %s: %v", layer, err)
			}
			all = append(all, id)
		}
		if _, err := client.ActivateRevisions(ctx, "alice", spawnv1.ActivationRequest{
			Org: contractTenant.Org, Owner: contractTenant.Owner, Revisions: all, IdempotencyKey: "ka-all",
		}); err != nil {
			t.Fatalf("activate set: %v", err)
		}
		eff, err := client.Effective(ctx, contractTenant, "alice")
		if err != nil {
			t.Fatalf("effective: %v", err)
		}

		get := func(path string) *http.Response {
			req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			return resp
		}
		q := fmt.Sprintf("org=%s&owner=%s", contractTenant.Org, contractTenant.Owner)
		resp := get("/v1/spaces/alice/policy/compose?" + q + "&digest=" + eff.Digest)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("policy slice: %d", resp.StatusCode)
		}
		if got := resp.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("Cache-Control = %q", got)
		}
		if got := resp.Header.Get("ETag"); got != eff.Digest {
			t.Errorf("ETag = %q, want %q", got, eff.Digest)
		}
		resp.Body.Close()

		// Error responses carry no cache signal.
		bad := get("/v1/spaces/alice/policy/compose?" + q + "&digest=sha256:" + strings.Repeat("0", 64))
		if bad.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown digest: %d", bad.StatusCode)
		}
		if got := bad.Header.Get("Cache-Control"); got != "" {
			t.Errorf("error response cached: %q", got)
		}
		bad.Body.Close()

		if _, err := client.PolicySlice(ctx, contractTenant, "alice", "nonesuch", eff.Digest); !errors.Is(err, spawnv1.ErrNotFound) {
			t.Fatalf("unknown service: want not_found, got %v", err)
		}
		foreign := spawnv1.Tenant{Org: "org-b", Owner: "mallory"}
		if _, err := client.PolicySlice(ctx, foreign, "alice", "compose", eff.Digest); !errors.Is(err, spawnv1.ErrNotFound) {
			t.Fatalf("foreign digest: want not_found, got %v", err)
		}
		// A raw foreign-tenant HTTP read of the same digest stays 404.
		resp = get("/v1/spaces/alice/policy/compose?org=org-b&owner=mallory&digest=" + eff.Digest)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign raw digest: %d", resp.StatusCode)
		}
		resp.Body.Close()

		var slice spawnv1.PolicySlice
		raw, err := client.PolicySlice(ctx, contractTenant, "alice", "compose", eff.Digest)
		if err != nil {
			t.Fatalf("slice via client: %v", err)
		}
		slice = raw
		if len(slice.Documents) == 0 {
			t.Fatal("compose slice is empty")
		}
		if _, ok := slice.Documents["stack"]; !ok {
			t.Error("compose slice lacks the stack document")
		}
		// The slice keeps document envelopes while carrying only the
		// consumer's attributed subtree.
		var stack map[string]any
		if err := json.Unmarshal(slice.Documents["stack"], &stack); err != nil {
			t.Fatalf("stack doc: %v", err)
		}
		if _, ok := stack["services"]; !ok {
			t.Error("compose stack doc lacks services")
		}
	})
}
