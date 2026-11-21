package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"gopkg.in/yaml.v3"
)

// writeServicesProjection lays down the render's model-services JSON — the
// effective models doc's service map the init companion mounts (S7d).
func writeServicesProjection(t *testing.T, gen, modelsYAML string) {
	t.Helper()
	var doc spawnv1.ModelsDoc
	if err := yaml.Unmarshal([]byte(modelsYAML), &doc); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(doc.Services)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, "model-services.prod.json"), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// cliproxyMaterialize delivers the oauth session files and proxy_access
// keys into the instance's tmpfs views: /cliproxy/auths holds one <ref>.json
// per enrolled session, /run/cliproxy-cfg holds the operator config plus
// api-keys.
func TestCliproxyMaterializeWritesSessionAndRenderedConfig(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	writeServicesProjection(t, gen, "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n")
	grants := `{"google-pro-1": {"google-one-1": {"init": "g-sess"}, "cliproxy-google-1": {"init": "g-key", "relay": "g-key-r"}}}`
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(grants), 0600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "base")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config.yaml"), []byte("host: \"\"\nport: 8317\nauth-dir: \"/cliproxy/auths\"\napi-keys: [\"STALE-BIND-KEY\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auths := filepath.Join(dir, "auths")
	cfg := filepath.Join(dir, "cfg")
	resolver := &fakeCliproxyPlane{files: map[string][]byte{"g-sess": []byte(`{"token":"SENTINEL"}`)}, envs: map[string]string{"g-key/MODEL_API_KEY": "SENTINEL-KEY"}}
	err := cliproxyMaterialize(context.Background(), resolver, cliproxyMaterializeArgs{
		Instance: "cliproxy-google-1", Owner: "alice",
		GeneratedDir: gen, BaseDir: base, AuthsDir: auths, CfgDir: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := os.ReadFile(filepath.Join(auths, "google-one-1.json"))
	if err != nil || !strings.Contains(string(session), "SENTINEL") {
		t.Fatalf("session file not delivered: %v", err)
	}
	rendered, err := os.ReadFile(filepath.Join(cfg, "config.yaml"))
	if err != nil || !strings.Contains(string(rendered), "SENTINEL-KEY") || !strings.Contains(string(rendered), "api-keys") {
		t.Fatalf("rendered config missing api-keys: %v %s", err, rendered)
	}
	// The bind's plaintext api-keys are retired material: the rendered config
	// must carry only the Broker-materialized key, not a duplicate section.
	if strings.Contains(string(rendered), "STALE-BIND-KEY") || strings.Count(string(rendered), "api-keys") != 1 {
		t.Fatalf("stale bind api-keys must not leak into the rendered config: %s", rendered)
	}
	if resolver.calls["file"] != 1 || resolver.calls["env"] != 1 {
		t.Fatalf("expected one session + one key resolution, got %v", resolver.calls)
	}
}

// One instance hosts every OAuth session its owner's services point at:
// two services sharing an endpoint yield two <ref>.json files and two
// api-keys, each resolved under the ref's own workload slot.
func TestCliproxyMaterializeDeliversAllInstanceSessions(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	writeServicesProjection(t, gen, "kind: models\nservices:\n"+
		"  chat:\n    via: cliproxy\n    endpoint: http://cliproxy-alice:8317/v1\n"+
		"    auth: {upstream: {kind: oauth-session, ref: cliproxy-alice}, proxy_access: {kind: broker-key, ref: cliproxy}}\n"+
		"  images:\n    via: cliproxy\n    endpoint: http://cliproxy-alice:8317/v1\n"+
		"    auth: {upstream: {kind: oauth-session, ref: google-ag}, proxy_access: {kind: broker-key, ref: cliproxy-google}}\n")
	grants := `{"chat": {"cliproxy-alice": {"init": "g-sess-a"}, "cliproxy": {"init": "g-key-a", "relay": "g-key-a-r"}},` +
		`"images": {"google-ag": {"init": "g-sess-b"}, "cliproxy-google": {"init": "g-key-b", "relay": "g-key-b-r"}}}`
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(grants), 0600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "base")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config.yaml"), []byte("port: 8317\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auths, cfg := filepath.Join(dir, "auths"), filepath.Join(dir, "cfg")
	resolver := &fakeCliproxyPlane{
		files: map[string][]byte{"g-sess-a": []byte(`{"token":"A"}`), "g-sess-b": []byte(`{"token":"B"}`)},
		envs:  map[string]string{"g-key-a/MODEL_API_KEY": "KEY-A", "g-key-b/MODEL_API_KEY": "KEY-B"},
	}
	err := cliproxyMaterialize(context.Background(), resolver, cliproxyMaterializeArgs{
		Instance: "cliproxy-alice", Owner: "alice",
		GeneratedDir: gen, BaseDir: base, AuthsDir: auths, CfgDir: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cliproxy-alice.json", "google-ag.json"} {
		if _, err := os.ReadFile(filepath.Join(auths, name)); err != nil {
			t.Fatalf("session %s not delivered: %v", name, err)
		}
	}
	rendered, _ := os.ReadFile(filepath.Join(cfg, "config.yaml"))
	if !strings.Contains(string(rendered), "KEY-A") || !strings.Contains(string(rendered), "KEY-B") {
		t.Fatalf("rendered config must carry both api-keys: %s", rendered)
	}
	want := []string{"cliproxy-alice", "cliproxy-alice-google-ag", "cliproxy-alice-key", "cliproxy-alice-key-cliproxy-google"}
	if len(resolver.bindings) != len(want) {
		t.Fatalf("bindings: want %v, got %v", want, resolver.bindings)
	}
	for i, b := range want {
		if resolver.bindings[i] != b {
			t.Fatalf("bindings: want %v, got %v", want, resolver.bindings)
		}
	}
}

// The init companion of an instance with no declared cliproxy service must
// fail: it can never materialize a session it was not granted for.
func TestCliproxyMaterializeRejectsUndeclaredInstance(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	writeServicesProjection(t, gen, "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n")
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(`{"google-pro-1": {"google-one-1": {"init": "g-sess"}, "cliproxy-google-1": {"init": "g-key", "relay": "g-key-r"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeCliproxyPlane{}
	err := cliproxyMaterialize(context.Background(), resolver, cliproxyMaterializeArgs{
		Instance: "cliproxy-google-2", Owner: "alice",
		GeneratedDir: gen, BaseDir: t.TempDir(), AuthsDir: t.TempDir(), CfgDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "cliproxy-google-2") {
		t.Fatalf("undeclared instance must fail: %v", err)
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("no lease may be touched for a foreign instance: %v", resolver.calls)
	}
}

// A mounted view without the render's services projection is a broken
// publish: the init companion refuses rather than materialize off a
// partial contract.
func TestCliproxyMaterializeRejectsIncompleteRevision(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(`{"google-pro-1": {"google-one-1": {"init": "g-sess"}, "cliproxy-google-1": {"init": "g-key", "relay": "g-key-r"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeCliproxyPlane{files: map[string][]byte{"g-sess": []byte(`{}`)}, envs: map[string]string{"g-key/MODEL_API_KEY": "k"}}
	err := cliproxyMaterialize(context.Background(), resolver, cliproxyMaterializeArgs{
		Instance: "cliproxy-google-1", Owner: "alice",
		GeneratedDir: gen, BaseDir: t.TempDir(), AuthsDir: t.TempDir(), CfgDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "cliproxy materialize") {
		t.Fatalf("incomplete revision must fail the load: %v", err)
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("no lease may be touched off a partial contract: %v", resolver.calls)
	}
}

// A cliproxy service without its auth contract (a stale pre-S7 revision)
// must fail with a named error — never panic the init companion.
func TestCliproxyMaterializeRejectsMissingAuthRefs(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	writeServicesProjection(t, gen, "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n")
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeCliproxyPlane{}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		err = cliproxyMaterialize(context.Background(), resolver, cliproxyMaterializeArgs{
			Instance: "cliproxy-google-1", Owner: "alice",
			GeneratedDir: gen, BaseDir: t.TempDir(), AuthsDir: t.TempDir(), CfgDir: t.TempDir(),
		})
	}()
	if err == nil || !strings.Contains(err.Error(), "google-pro-1") {
		t.Fatalf("missing auth refs must fail by name: %v", err)
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("no lease may be touched without auth refs: %v", resolver.calls)
	}
}

// A base config that is not a mapping document cannot take an api-keys
// entry — the init companion fails by name instead of panicking.
func TestCliproxyMaterializeRejectsNonMapBaseConfig(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0700); err != nil {
		t.Fatal(err)
	}
	writeServicesProjection(t, gen, "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n")
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(`{"google-pro-1": {"google-one-1": {"init": "g-sess"}, "cliproxy-google-1": {"init": "g-key", "relay": "g-key-r"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "base")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config.yaml"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeCliproxyPlane{files: map[string][]byte{"g-sess": []byte(`{}`)}, envs: map[string]string{"g-key/MODEL_API_KEY": "k"}}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		err = cliproxyMaterialize(context.Background(), resolver, cliproxyMaterializeArgs{
			Instance: "cliproxy-google-1", Owner: "alice",
			GeneratedDir: gen, BaseDir: base, AuthsDir: t.TempDir(), CfgDir: t.TempDir(),
		})
	}()
	if err == nil || !strings.Contains(err.Error(), "cliproxy materialize") {
		t.Fatalf("non-mapping base config must fail by name: %v", err)
	}
}

type fakeCliproxyPlane struct {
	files    map[string][]byte
	envs     map[string]string
	calls    map[string]int
	bindings []string
}

func (f *fakeCliproxyPlane) File(_ context.Context, binding, grant string) ([]byte, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls["file"]++
	f.bindings = append(f.bindings, binding)
	b, ok := f.files[grant]
	if !ok {
		return nil, fmt.Errorf("no file for grant %s", grant)
	}
	return b, nil
}

func (f *fakeCliproxyPlane) Env(_ context.Context, binding, grant, target string) (string, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls["env"]++
	f.bindings = append(f.bindings, binding)
	v, ok := f.envs[grant+"/"+target]
	if !ok {
		return "", fmt.Errorf("no env for grant %s", grant)
	}
	return v, nil
}
