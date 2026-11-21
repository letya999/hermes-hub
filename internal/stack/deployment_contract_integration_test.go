//go:build integration

package stack_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"

	"gopkg.in/yaml.v3"
)

// publishDocs lays down the generated/passports layout the fixture reader
// serves — the on-disk stand-in for a submitted+activated revision.
func publishDocs(t *testing.T, dir string, rev passport.Revision) {
	t.Helper()
	root := filepath.Join(dir, "generated", "passports")
	revDir := filepath.Join(root, rev.ID)
	if err := os.MkdirAll(revDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range rev.Docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "current"), []byte(rev.ID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixtureReader serves the contract read path from the fixture's generated
// tree — the same shim effective_shim_test.go carries for internal tests.
type fixtureReader struct{ dir string }

func (r fixtureReader) spec(space string) (spawnv1.EffectiveSpec, error) {
	if space == "" {
		space = filepath.Base(r.dir)
	}
	dir := filepath.Join(filepath.Dir(r.dir), space)
	cur, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current")) // #nosec G703 -- test fixture path
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
	docs := map[string]json.RawMessage{}
	h := sha256.New()
	for _, name := range spawnv1.Names {
		body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml")) // #nosec G703 -- test fixture path
		if err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		var v any
		if err := yaml.Unmarshal(body, &v); err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		docs[name] = raw
		h.Write(raw)
	}
	return spawnv1.EffectiveSpec{Space: space, Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Documents: docs}, nil
}

func (r fixtureReader) Effective(_ context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return r.spec(space)
}

func (r fixtureReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.spec(space)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}

// Uses the actual Compose loader, without starting containers or using accounts.
func TestDeploymentConfigurationContract(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"dev", "prod"} {
		for _, mode := range []string{"static", "supervisor"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "spaces", "contract")
				if err := stack.InitEnvironment(dir, "contract", environment); err != nil {
					t.Fatal(err)
				}
				models := "default: synthetic\nroutes: {synthetic: {ladder: [upstream]}}\nservices:\n  upstream: {via: cliproxy, endpoint: 'http://cliproxy:8317/v1', auth: {proxy_access: {kind: broker-key, ref: cliproxy-default}}, models: [{id: synthetic}]}\n"
				if err := os.WriteFile(filepath.Join(dir, "passports", "models.yaml"), []byte(models), 0600); err != nil {
					t.Fatal(err)
				}
				stackBody := "identity: {user: contract, infra: false}\nservices:\n" +
					"  model-relay: {mode: per-space}\n  toolhub-relay: {mode: per-space}\n" +
					"  toolhub: {mode: shared}\n  cliproxy: {mode: shared}\n  credential-broker: {mode: shared}\n"
				if mode == "supervisor" {
					stackBody = strings.Replace(stackBody, "infra: false", "infra: true", 1)
					stackBody += "  hermes-runtime: {mode: supervised}\n"
					stackBody += "  communication-hub: {mode: shared}\n  supervisor: {mode: shared}\n"
					configBody := "runtime: {timezone: UTC}\nmemory: {enabled: false}\nchannels:\n  telegram: {bot: {ref: telegram-bot-contract}}\n"
					if err := os.WriteFile(filepath.Join(dir, "passports", "config.yaml"), []byte(configBody), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, "passports", "stack.yaml"), []byte(stackBody), 0600); err != nil {
					t.Fatal(err)
				}
				catalogDocs, err := catalog.Generate()
				if err != nil {
					t.Fatal(err)
				}
				revision, err := passport.CompileAuthored(dir, environment, time.Now().UTC(), catalogDocs, stack.ReservedSiblingPorts(dir), map[string][]string{})
				if err != nil {
					t.Fatal(err)
				}
				publishDocs(t, dir, revision)
				if err := os.WriteFile(filepath.Join(dir, "secrets."+environment+".env"), []byte("OPENAI_API_KEY=synthetic-contract-only\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "supervisor" {
					selection := stack.ExecutionSelection{Schema: 1, User: "contract", Environment: environment, Mode: mode, SupervisorURL: "http://supervisor:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
					data, err := json.Marshal(selection)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, stack.ExecutionPath(environment)), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				renderErr := stack.RenderEnvironment(context.Background(), fixtureReader{dir}, dir, root, environment)
				if mode == "static" {
					if renderErr == nil || !strings.Contains(renderErr.Error(), "managed runtime requires supervisor") {
						t.Fatalf("unproven static managed runtime accepted: %v", renderErr)
					}
					return
				}
				if err := renderErr; err != nil {
					t.Fatal(err)
				}
				s, err := stack.ReadEffective(context.Background(), fixtureReader{dir}, dir, environment)
				if err != nil {
					t.Fatal(err)
				}
				if s.ExecutionMode != "supervisor" || s.SupervisorURL != "http://supervisor:8876" {
					t.Fatalf("rendered deployment lost published managed selection: execution=%q supervisor=%q", s.ExecutionMode, s.SupervisorURL)
				}
				body, err := yaml.Marshal(stack.Compose(s, root, dir))
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "contract.yaml")
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				validate := func(auth string) ([]byte, error) {
					cmd := exec.Command("docker", "compose", "-f", path, "config", "--quiet")
					// Do not inherit account credentials or auth from the developer.
					for _, entry := range os.Environ() {
						if !strings.HasPrefix(entry, "HUB_") && !strings.HasPrefix(entry, "COMPOSE_") {
							cmd.Env = append(cmd.Env, entry)
						}
					}
					cmd.Env = append(cmd.Env, "HUB_SUPERVISOR_AUTH="+auth)
					return cmd.CombinedOutput()
				}
				if output, err := validate("synthetic-control-only"); err != nil {
					t.Fatalf("Compose contract invalid: %s (%v)", output, err)
				}
				output, err := validate("")
				if err == nil || !strings.Contains(string(output), "HUB_SUPERVISOR_AUTH") {
					t.Fatalf("missing auth did not fail with actionable diagnostic: %s (%v)", output, err)
				}
			})
		}
	}
}
