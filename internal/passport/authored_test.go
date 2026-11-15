package passport_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

func TestReadAuthoredRequiresSixRegularFiles(t *testing.T) {
	space := t.TempDir()
	dir := filepath.Join(space, "passports")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range passport.Names {
		if name == "grants" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte("x: yes\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := passport.ReadAuthored(space); err == nil || !strings.Contains(err.Error(), "grants.yaml") {
		t.Fatalf("missing grants did not fail by name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grants.yaml"), []byte("allow:\n  memory: [search]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docs, err := passport.ReadAuthored(space)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 6 || docs["grants"]["allow"] == nil {
		t.Fatalf("six passports or authored grants lost: %+v", docs)
	}
	if err := os.WriteFile(filepath.Join(dir, "user.yaml"), []byte("grant_all: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.ReadAuthored(space); err == nil || !strings.Contains(err.Error(), "user.yaml") {
		t.Fatalf("extra config file admitted: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "user.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools.yaml"), []byte("x: yes\n---\ny: no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.ReadAuthored(space); err == nil {
		t.Fatal("multiple YAML documents admitted")
	}
}

func TestReadAuthoredRejectsPassportSymlink(t *testing.T) {
	space := t.TempDir()
	dir := filepath.Join(space, "passports")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "stack.yaml")
	if err := os.WriteFile(outside, []byte("identity: {user: alice}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "stack.yaml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := passport.ReadAuthored(space); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("symlinked passport admitted: %v", err)
	}
}

func TestReadAuthoredRejectsPassportDirectorySymlink(t *testing.T) {
	space := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(space, "passports")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := passport.ReadAuthored(space); err == nil || !strings.Contains(err.Error(), "regular directory") {
		t.Fatalf("symlinked passport directory admitted: %v", err)
	}
}

// The retired settings.yaml context policy (compaction engines, goals,
// compression leaves, auxiliary models) must be authored under
// config.runtime — invalid choices fail authored compilation rather than
// silently dropping the operator's declaration.
func TestCompileAuthoredValidatesContextPolicy(t *testing.T) {
	space := filepath.Join(t.TempDir(), "alice")
	dir := filepath.Join(space, "passports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rev := compileFixture(t)
	var configBody map[string]any
	for _, name := range passport.Names {
		var body map[string]any
		if err := yaml.Unmarshal(rev.Docs[name], &body); err != nil {
			t.Fatal(err)
		}
		delete(body, "apiVersion")
		delete(body, "kind")
		delete(body, "metadata")
		if name == "workspace" {
			delete(body, "tree")
		}
		if name == "stack" {
			body["identity"].(map[string]any)["infra"] = true
		}
		if name == "config" {
			body["memory"].(map[string]any)["enabled"] = false
			configBody = body
		}
		if name == "tools" {
			body["binding"] = map[string]any{"profile_id": "alice-default", "generation": 2}
			for _, area := range []string{"core", "extended"} {
				if sets, ok := body[area].(map[string]any); ok {
					delete(sets, "platform")
				}
			}
		}
		b, err := yaml.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	configPath := filepath.Join(dir, "config.yaml")
	valid, err := yaml.Marshal(configBody)
	if err != nil {
		t.Fatal(err)
	}
	// The honest declaration compiles.
	var base map[string]any
	if err := yaml.Unmarshal(valid, &base); err != nil {
		t.Fatal(err)
	}
	base["runtime"] = map[string]any{
		"timezone":    "UTC",
		"context":     map[string]any{"compaction": "hub", "engines": []any{"hub", "native"}},
		"goals":       map[string]any{"engine": "native", "judge": "self", "max_turns": 12},
		"compression": map[string]any{"hub_sweep": map[string]any{"enabled": true}},
		"auxiliary":   map[string]any{"summary_model": "m", "judge_model": "m"},
	}
	body, err := yaml.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err != nil {
		t.Fatalf("valid context policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(runtime map[string]any){
		"context.engines":    func(r map[string]any) { r["context"] = map[string]any{"compaction": "hub", "engines": []any{"bogus"}} },
		"engine not granted": func(r map[string]any) { r["context"] = map[string]any{"compaction": "hub", "engines": []any{"native"}} },
		"goals.engine":       func(r map[string]any) { r["goals"] = map[string]any{"engine": "bogus"} },
		"goals.judge":        func(r map[string]any) { r["goals"] = map[string]any{"judge": "bogus"} },
		"goals.max_turns":    func(r map[string]any) { r["goals"] = map[string]any{"max_turns": 99} },
		"compression leaf":   func(r map[string]any) { r["compression"] = map[string]any{"evil_key": 1} },
		"auxiliary model":    func(r map[string]any) { r["auxiliary"] = map[string]any{"summary_model": "bad model!"} },
	} {
		var cfg map[string]any
		if err := yaml.Unmarshal(valid, &cfg); err != nil {
			t.Fatal(err)
		}
		runtime := map[string]any{"timezone": "UTC"}
		mutate(runtime)
		cfg["runtime"] = runtime
		body, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil {
			t.Fatalf("invalid context policy %q compiled", name)
		}
	}
}

func TestCompileAuthoredKeepsToolsAndGrantsIndependent(t *testing.T) {
	space := filepath.Join(t.TempDir(), "alice")
	dir := filepath.Join(space, "passports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rev := compileFixture(t)
	for _, name := range passport.Names {
		var body map[string]any
		if err := yaml.Unmarshal(rev.Docs[name], &body); err != nil {
			t.Fatal(err)
		}
		delete(body, "apiVersion")
		delete(body, "kind")
		delete(body, "metadata")
		if name == "workspace" {
			delete(body, "tree")
		}
		if name == "stack" {
			body["identity"].(map[string]any)["infra"] = true
		}
		if name == "config" {
			body["memory"].(map[string]any)["enabled"] = false
		}
		if name == "tools" {
			body["binding"] = map[string]any{"profile_id": "alice-default", "generation": 2}
			for _, area := range []string{"core", "extended"} {
				if sets, ok := body[area].(map[string]any); ok {
					delete(sets, "platform")
				}
			}
		}
		b, err := yaml.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	compiled, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants)
	if err != nil {
		t.Fatal(err)
	}
	if infra, err := compiled.Value("stack", "identity.infra"); err != nil || infra != true {
		t.Fatalf("authored infra flag lost: %v, %v", infra, err)
	}
	if profile, err := compiled.Value("tools", "binding.profile_id"); err != nil || profile != "alice-default" {
		t.Fatalf("authored profile binding lost: %v, %v", profile, err)
	}
	before, err := compiled.GrantAllow()
	if err != nil {
		t.Fatal(err)
	}
	toolsPath := filepath.Join(dir, "tools.yaml")
	toolBytes, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolsPath, append(toolBytes, []byte("\n# selection comment\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants)
	if err != nil {
		t.Fatal(err)
	}
	after, err := changed.GrantAllow()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) || len(before["tools"]) != len(after["tools"]) {
		t.Fatalf("tool edit changed grants: before=%v after=%v", before, after)
	}
	if err := os.WriteFile(filepath.Join(dir, "grants.yaml"), []byte("allow:\n  memory: [search]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("grant outside trusted ceiling admitted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grants.yaml"), []byte("allow: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	withoutRights, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants)
	if err != nil {
		t.Fatal(err)
	}
	if allow, err := withoutRights.GrantAllow(); err != nil || len(allow) != 0 {
		t.Fatalf("empty authored grant set widened: %v, %v", allow, err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var configDoc map[string]any
	if err := yaml.Unmarshal(configBytes, &configDoc); err != nil {
		t.Fatal(err)
	}
	delete(configDoc["memory"].(map[string]any), "enabled")
	missingMemory, err := yaml.Marshal(configDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, missingMemory, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "config.memory.enabled") {
		t.Fatalf("missing memory enablement compiled: %v", err)
	}
	configDoc["memory"].(map[string]any)["enabled"] = "false"
	invalidMemory, err := yaml.Marshal(configDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, invalidMemory, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "config.memory.enabled") {
		t.Fatalf("string memory enablement compiled: %v", err)
	}
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(configBytes, []byte("unknown_field: true\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("unknown authored field compiled: %v", err)
	}
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var toolsDoc map[string]any
	if err := yaml.Unmarshal(toolBytes, &toolsDoc); err != nil {
		t.Fatal(err)
	}
	delete(toolsDoc, "binding")
	missingBinding, err := yaml.Marshal(toolsDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolsPath, missingBinding, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "tools.binding") {
		t.Fatalf("managed space without tool binding compiled: %v", err)
	}
	toolsDoc["binding"] = map[string]any{"profile_id": "alice-default", "generation": 0}
	invalidBinding, err := yaml.Marshal(toolsDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolsPath, invalidBinding, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "tools.binding.generation") {
		t.Fatalf("invalid tool binding generation compiled: %v", err)
	}
}

// identity.kind carries the retired scope.yaml's role: only user and group
// scopes exist (SPEC-0091 R11.1 keeps group spaces usable), anything else is
// a malformed passport, never a silent personal downgrade.
func TestCompileAuthoredRejectsUnknownScopeKind(t *testing.T) {
	space := filepath.Join(t.TempDir(), "team-a")
	dir := filepath.Join(space, "passports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rev := compileFixture(t)
	for _, name := range passport.Names {
		var body map[string]any
		if err := yaml.Unmarshal(rev.Docs[name], &body); err != nil {
			t.Fatal(err)
		}
		delete(body, "apiVersion")
		delete(body, "kind")
		delete(body, "metadata")
		if name == "workspace" {
			delete(body, "tree")
		}
		if name == "stack" {
			body["identity"].(map[string]any)["user"] = "team-a"
			body["identity"].(map[string]any)["infra"] = false
			body["identity"].(map[string]any)["kind"] = "alien"
		}
		if name == "config" {
			body["memory"].(map[string]any)["enabled"] = false
		}
		if name == "tools" {
			body["binding"] = map[string]any{"profile_id": "team-a-default", "generation": 2}
			for _, area := range []string{"core", "extended"} {
				if sets, ok := body[area].(map[string]any); ok {
					delete(sets, "platform")
				}
			}
		}
		b, err := yaml.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if _, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("alien scope kind compiled: %v", err)
	}
	stackPath := filepath.Join(dir, "stack.yaml")
	b, err := os.ReadFile(stackPath)
	if err != nil {
		t.Fatal(err)
	}
	var stackDoc map[string]any
	if err := yaml.Unmarshal(b, &stackDoc); err != nil {
		t.Fatal(err)
	}
	stackDoc["identity"].(map[string]any)["kind"] = "group"
	if b, err = yaml.Marshal(stackDoc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stackPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	compiled, err := passport.CompileAuthored(space, "dev", now, docs, nil, fixtureInputs(t).Grants)
	if err != nil {
		t.Fatalf("group scope kind rejected: %v", err)
	}
	if kind, err := compiled.Value("stack", "identity.kind"); err != nil || kind != "group" {
		t.Fatalf("group scope kind lost: %v, %v", kind, err)
	}
}

func TestCompileAllowsNoGrants(t *testing.T) {
	in := fixtureInputs(t)
	in.Grants = map[string][]string{}
	rev, err := passport.Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	allow, err := rev.GrantAllow()
	if err != nil {
		t.Fatal(err)
	}
	if len(allow) != 0 {
		t.Fatalf("empty grant set widened: %v", allow)
	}
}
