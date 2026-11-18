package passport

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/catalog"
)

// Direct calls to the v2 validators: Compile() exercises the happy path;
// these cases pin every rejection branch.
func TestCheckIAMBranches(t *testing.T) {
	withIDH := map[string]instInfo{
		"identity-hub": {id: "identity-hub", mode: "shared"},
		"hub-postgres": {id: "hub-postgres", mode: "shared"},
	}
	perSpaceIDH := map[string]instInfo{
		"identity-hub": {id: "identity-hub", mode: "per-space"},
		"hub-postgres": {id: "hub-postgres", mode: "per-space"},
	}

	cases := []struct {
		name      string
		raw       any
		instances map[string]instInfo
		wantErr   string
	}{
		{"not-map", "x", nil, "must be a map"},
		{"bad-mode", map[string]any{"mode": "none"}, nil, "external or bundled"},
		{"external-with-bundled-svcs", map[string]any{"mode": "external", "issuer": "https://idp.example", "client_id": "c"}, withIDH, "cannot run the bundled"},
		{"external-missing-issuer", map[string]any{"mode": "external", "client_id": "c"}, nil, "issuer is required"},
		{"external-missing-client", map[string]any{"mode": "external", "issuer": "https://idp.example"}, nil, "client_id is required"},
		{"external-ok", map[string]any{"mode": "external", "issuer": "https://idp.example/tenant", "client_id": "c"}, nil, ""},
		{"issuer-not-url", map[string]any{"mode": "external", "issuer": "noscheme", "client_id": "c"}, nil, "absolute http(s)"},
		{"bundled-issuer-with-path", map[string]any{"mode": "bundled", "issuer": "https://hub.example/tenant"}, withIDH, "no path"},
		{"bundled-missing-services", map[string]any{"mode": "bundled"}, nil, "requires stack services"},
		{"bundled-non-shared", map[string]any{"mode": "bundled"}, perSpaceIDH, "mode shared"},
		{"bundled-ok", map[string]any{"mode": "bundled"}, withIDH, ""},
	}
	for _, tc := range cases {
		err := checkIAM(tc.raw, tc.instances)
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: unexpected %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: got %v want ~%q", tc.name, err, tc.wantErr)
		}
	}
}

func TestCheckModelsBranches(t *testing.T) {
	instances := map[string]instInfo{"cliproxy-gw": {id: "cliproxy"}}
	svc := func(m map[string]any) map[string]map[string]any {
		return map[string]map[string]any{"models": {"services": map[string]any{"s1": m}}}
	}

	cases := []struct {
		name    string
		bodies  map[string]map[string]any
		wantErr string
	}{
		{"default-not-route", map[string]map[string]any{"models": {"default": "r9", "routes": map[string]any{"r1": map[string]any{}}}}, "not a route"},
		{"ladder-unknown-service", map[string]map[string]any{"models": {"routes": map[string]any{"r1": map[string]any{"ladder": []any{"ghost"}}}}}, "unknown service"},
		{"bad-via", svc(map[string]any{"via": "magic"}), "must be cliproxy|openai-compatible"},
		{"cliproxy-bad-endpoint", svc(map[string]any{"via": "cliproxy", "endpoint": "ftp://x"}), "not an http(s) URL"},
		{"cliproxy-non-cliproxy-host", svc(map[string]any{"via": "cliproxy", "endpoint": "http://other:1"}), "not a cliproxy stack instance"},
		{"cliproxy-missing-proxy-access", svc(map[string]any{"via": "cliproxy", "endpoint": "http://cliproxy-gw:1"}), "requires auth.proxy_access"},
		{"proxy-access-not-map", svc(map[string]any{"via": "cliproxy", "endpoint": "http://cliproxy-gw:1", "auth": map[string]any{"proxy_access": "literal"}}), "contract reference"},
		{"proxy-access-on-openai", svc(map[string]any{"via": "openai-compatible", "auth": map[string]any{"proxy_access": map[string]any{}}}), "only applies to cliproxy"},
		{"upstream-bad-kind", svc(map[string]any{"via": "openai-compatible", "auth": map[string]any{"upstream": map[string]any{"kind": "raw"}}}), "oauth-session|broker-key"},
		{"limit-bad-of", svc(map[string]any{"via": "openai-compatible", "limits": []any{map[string]any{"of": "gold", "per": "day"}}}), "requests|tokens|provider_quota|spend_usd"},
		{"limit-missing-per", svc(map[string]any{"via": "openai-compatible", "limits": []any{map[string]any{"of": "tokens"}}}), "requires per"},
		{"bad-reasoning", svc(map[string]any{"via": "openai-compatible", "models": []any{map[string]any{"reasoning": "galaxy"}}}), "none|low|medium|high"},
		{"cliproxy-ok", svc(map[string]any{"via": "cliproxy", "endpoint": "http://cliproxy-gw:1", "auth": map[string]any{"proxy_access": map[string]any{"kind": "k", "ref": "r"}}}), ""},
		{"openai-ok", svc(map[string]any{"via": "openai-compatible", "auth": map[string]any{"upstream": map[string]any{"kind": "broker-key"}}, "limits": []any{map[string]any{"of": "requests", "per": "1m"}}, "models": []any{map[string]any{"reasoning": "low"}}}), ""},
	}
	for _, tc := range cases {
		err := checkModels(tc.bodies, instances)
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: unexpected %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: got %v want ~%q", tc.name, err, tc.wantErr)
		}
	}
}

func TestCheckCompleteBranches(t *testing.T) {
	managed := map[string]instInfo{"hermes-runtime": {id: "hermes-runtime"}}
	withRelay := map[string]instInfo{"hermes-runtime": {id: "hermes-runtime"}, "model-relay": {id: "model-relay"}}

	// No managed runtime: checkComplete is a no-op.
	if err := checkComplete(map[string]map[string]any{}, nil); err != nil {
		t.Fatalf("non-managed: %v", err)
	}

	mk := func(stack, config, models, tools map[string]any) map[string]map[string]any {
		return map[string]map[string]any{"stack": stack, "config": config, "models": models, "tools": tools}
	}
	goodTools := map[string]any{"core": map[string]any{"files": map[string]any{}}}
	goodModels := map[string]any{"services": map[string]any{"s": map[string]any{}}, "routes": map[string]any{"r": map[string]any{}}}
	goodIdentity := map[string]any{"identity": map[string]any{"user": "alice"}}
	cfg := map[string]any{"memory": map[string]any{}}

	cases := []struct {
		name      string
		bodies    map[string]map[string]any
		instances map[string]instInfo
		wantErr   string
	}{
		{"no-identity-user", mk(map[string]any{}, cfg, goodModels, goodTools), managed, "identity.user"},
		{"empty-config", mk(goodIdentity, map[string]any{}, goodModels, goodTools), managed, "empty config"},
		{"relay-no-services", mk(goodIdentity, cfg, map[string]any{"routes": map[string]any{"r": map[string]any{}}}, goodTools), withRelay, "models.services is empty"},
		{"relay-no-routes", mk(goodIdentity, cfg, map[string]any{"services": map[string]any{"s": map[string]any{}}}, goodTools), withRelay, "models.routes is empty"},
		{"no-toolsets", mk(goodIdentity, cfg, goodModels, map[string]any{"core": map[string]any{"platform": map[string]any{}}}), managed, "no toolsets beyond"},
		{"ok", mk(goodIdentity, cfg, goodModels, goodTools), withRelay, ""},
		{"ok-platform-plus-one", mk(goodIdentity, cfg, goodModels, map[string]any{"extended": map[string]any{"extra": map[string]any{}}}), managed, ""},
	}
	for _, tc := range cases {
		err := checkComplete(tc.bodies, tc.instances)
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: unexpected %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("%s: got %v want ~%q", tc.name, err, tc.wantErr)
		}
	}
}

func TestIntPortAndResolveInstance(t *testing.T) {
	if intPort(8080) != 8080 || intPort(int64(80)) != 80 || intPort(443.0) != 443 || intPort(" 9090 ") != 9090 {
		t.Fatal("intPort numeric coercion wrong")
	}
	if intPort("abc") != 0 || intPort(nil) != 0 || intPort(true) != 0 {
		t.Fatal("intPort non-numeric should be 0")
	}

	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if info, ok := resolveInstance(docs, "cliproxy"); !ok || info.id != "cliproxy" {
		t.Fatalf("exact instance: %+v %v", info, ok)
	}
	if info, ok := resolveInstance(docs, "cliproxy-google-1"); !ok || info.id != "cliproxy" {
		t.Fatalf("prefix instance: %+v %v", info, ok)
	}
	if _, ok := resolveInstance(docs, "totally-unknown"); ok {
		t.Fatal("unknown instance resolved")
	}
}

func TestCheckCompressionLeaves(t *testing.T) {
	// Unknown leaf path fails; invalid key chars fail; nested maps recurse.
	if err := checkCompressionLeaves(map[string]any{"unknown.leaf": 1}, "compression"); err == nil {
		t.Fatal("unknown leaf accepted")
	}
	if err := checkCompressionLeaves(map[string]any{"bad.key": 1}, "compression"); err == nil {
		t.Fatal("dotted key accepted")
	}
	if err := checkCompressionLeaves(map[string]any{"nested": map[string]any{"unknown": 1}}, "compression"); err == nil {
		t.Fatal("nested unknown leaf accepted")
	}
}
