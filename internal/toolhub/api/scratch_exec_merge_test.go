package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// TestFakeDockerChild emulates the docker CLI surface DockerScratchExec
// needs: image inspect, exec-pack, the scratch run, rm, and a final inspect
// that reports the executor gone.
func TestFakeDockerChild(t *testing.T) {
	kind := ""
	for _, a := range os.Args {
		switch a {
		case "exec-pack", "exec-scratch", "inspect", "rm":
			if kind == "" {
				kind = a
			}
		}
	}
	if kind == "" {
		t.Skip("helper process")
	}
	switch kind {
	case "inspect":
		// confirmStopped's plain `inspect <name>` must fail: the executor is
		// gone. `inspect -f <fmt> <runtime>` answers the image question.
		withFormat := false
		for _, a := range os.Args {
			if a == "-f" {
				withFormat = true
			}
		}
		if withFormat {
			os.Stdout.WriteString("img:1\n")
			os.Exit(0)
		}
		os.Exit(1)
	case "exec-pack":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Stdout.WriteString("tarbytes")
		os.Exit(0)
	case "exec-scratch":
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = json.NewEncoder(os.Stdout).Encode(ScratchExecResult{ExitCode: 0, Stdout: "done", TreeStopped: true})
		os.Exit(0)
	case "rm":
		os.Exit(0)
	}
	os.Exit(0)
}

// TestFakeDockerEnvChild answers the docker verbs like TestFakeDockerChild
// but echoes the leased skill-env names (never values) back through stdout so
// the parent can verify the stdin delivery contract.
func TestFakeDockerEnvChild(t *testing.T) {
	kind := ""
	for _, a := range os.Args {
		switch a {
		case "exec-pack", "exec-scratch", "inspect", "rm":
			if kind == "" {
				kind = a
			}
		}
	}
	if kind == "" {
		t.Skip("helper process")
	}
	switch kind {
	case "inspect":
		withFormat := false
		for _, a := range os.Args {
			if a == "-f" {
				withFormat = true
			}
		}
		if withFormat {
			os.Stdout.WriteString("img:1\n")
			os.Exit(0)
		}
		os.Exit(1)
	case "exec-pack":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Stdout.WriteString("tarbytes")
		os.Exit(0)
	case "exec-scratch":
		var request ScratchExecRequest
		_ = json.NewDecoder(os.Stdin).Decode(&request)
		names := []string{}
		for name := range request.Env {
			names = append(names, name)
		}
		slices.Sort(names)
		_ = json.NewEncoder(os.Stdout).Encode(ScratchExecResult{ExitCode: 0, Stdout: "keys=" + strings.Join(names, ","), TreeStopped: true})
		os.Exit(0)
	case "rm":
		os.Exit(0)
	}
	os.Exit(0)
}

// R8.3: a `skill` argument leases the stored (principal, skill) credentials
// into the one-call sandbox env — intersected with the names the effective
// index declares, so undeclared store entries never travel.
func TestDockerScratchExecLeasesSkillEnv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	storeDir := t.TempDir()
	effDir := t.TempDir()
	if err := skillstore.WriteCredentials(storeDir, "demo", map[string]string{"API_KEY": "s3cr3t", "EXTRA": "nope"}); err != nil {
		t.Fatal(err)
	}
	index := `{"skills":[{"name":"demo","eligible":true,"requires":{"credentials":[{"name":"API_KEY"}]}}]}`
	if err := os.WriteFile(filepath.Join(effDir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_EFFECTIVE", effDir)

	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-scratch-env", PrincipalID: "alice"}}
	scratch := DockerScratchExec([]string{os.Args[0], "-test.run=TestFakeDockerEnvChild"}, FixedAgentContainer("rt-1"), nil)
	res, err := scratch(ctx, effective, AgentExecRequest{
		Tool:           "code_exec",
		Arguments:      map[string]any{"command": "env", "path": "ws", "skill": "demo"},
		Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatalf("scratch exec: %v", err)
	}
	summary, _ := res.Result.(map[string]any)
	if summary["stdout"] != "keys=API_KEY" {
		t.Fatalf("leased env names: %+v", summary)
	}

	// Revocation is a file delete: the next call for the same (principal,
	// skill) resolves an empty lease — credentials never outlive storage.
	if err := os.Remove(filepath.Join(storeDir, ".state", "credentials", "demo.json")); err != nil {
		t.Fatal(err)
	}
	res, err = scratch(ctx, effective, AgentExecRequest{
		Tool:           "code_exec",
		Arguments:      map[string]any{"command": "env", "path": "ws", "skill": "demo"},
		Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatalf("scratch exec after revoke: %v", err)
	}
	summary, _ = res.Result.(map[string]any)
	if summary["stdout"] != "keys=" {
		t.Fatalf("revoked credentials still leased: %+v", summary)
	}
}

// R8.3 negatives: no skill argument, an ineligible skill and a skill with no
// stored credentials all execute cleanly with an empty lease — the env
// channel never carries undeclared or unearned secrets.
func TestDockerScratchExecSkillEnvNegatives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	storeDir := t.TempDir()
	effDir := t.TempDir()
	if err := skillstore.WriteCredentials(storeDir, "demo", map[string]string{"API_KEY": "s3cr3t"}); err != nil {
		t.Fatal(err)
	}
	index := `{"skills":[{"name":"demo","eligible":false,"requires":{"credentials":[{"name":"API_KEY"}]}},{"name":"other","eligible":true,"requires":{"credentials":[{"name":"API_KEY"}]}}]}`
	if err := os.WriteFile(filepath.Join(effDir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_EFFECTIVE", effDir)

	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-scratch-neg", PrincipalID: "alice"}}
	scratch := DockerScratchExec([]string{os.Args[0], "-test.run=TestFakeDockerEnvChild"}, FixedAgentContainer("rt-1"), nil)
	call := func(args map[string]any) map[string]any {
		res, err := scratch(ctx, effective, AgentExecRequest{
			Tool:           "code_exec",
			Arguments:      args,
			Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
			TimeoutSeconds: 30,
		}, nil)
		if err != nil {
			t.Fatalf("scratch exec: %v", err)
		}
		summary, _ := res.Result.(map[string]any)
		return summary
	}
	if out := call(map[string]any{"command": "env", "path": "ws"}); out["stdout"] != "keys=" {
		t.Fatalf("no-skill call leased env: %+v", out)
	}
	if out := call(map[string]any{"command": "env", "path": "ws", "skill": "demo"}); out["stdout"] != "keys=" {
		t.Fatalf("ineligible skill leased env: %+v", out)
	}
	if out := call(map[string]any{"command": "env", "path": "ws", "skill": "other"}); out["stdout"] != "keys=" {
		t.Fatalf("skill without stored creds leased env: %+v", out)
	}
	if out := call(map[string]any{"command": "env", "path": "ws", "skill": "../escape"}); out["stdout"] != "keys=" {
		t.Fatalf("malformed skill name leased env: %+v", out)
	}
}

// R8.2: a credential ref leases the value of an already-configured self-env
// key under the declared name — the shared-PAT pattern without a per-skill
// duplicate. A stored per-skill value still wins.
func TestDockerScratchExecLeasesCredentialRef(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	storeDir := t.TempDir()
	effDir := t.TempDir()
	stateDir := t.TempDir()
	index := `{"skills":[{"name":"shared","eligible":true,"requires":{"credentials":[{"name":"GH_PAT","ref":"GITHUB_TOKEN"},{"name":"OWN","ref":"MISSING_KEY"}]}}]}`
	if err := os.WriteFile(filepath.Join(effDir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "self-env.json"), []byte(`{"GITHUB_TOKEN":"tok-shared","OPENAI_API_KEY":"sk"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SKILLS_STORE", storeDir)
	t.Setenv("HUB_SKILLS_EFFECTIVE", effDir)
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_SELF_ENV_KEYS", "GITHUB_TOKEN,OPENAI_API_KEY")
	t.Setenv("HUB_PROTECTED_ENV_KEYS", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skill-ref-lease" {
			t.Error("wrong lease endpoint")
		}
		var lease struct {
			PrincipalID string `json:"principal_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&lease); err != nil {
			t.Error(err)
		}
		if lease.PrincipalID != "alice" {
			t.Errorf("wrong lease principal: %s", lease.PrincipalID)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"GITHUB_TOKEN": "tok-shared"})
	}))
	defer server.Close()
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "test-auth")

	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-scratch-ref", PrincipalID: "alice"}}
	scratch := DockerScratchExec([]string{os.Args[0], "-test.run=TestFakeDockerEnvChild"}, FixedAgentContainer("rt-1"), nil)
	res, err := scratch(ctx, effective, AgentExecRequest{
		Tool:           "code_exec",
		Arguments:      map[string]any{"command": "env", "path": "ws", "skill": "shared"},
		Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatalf("scratch exec: %v", err)
	}
	summary, _ := res.Result.(map[string]any)
	// GH_PAT resolves through the ref; OWN's ref points at an unconfigured
	// key and leases nothing.
	if summary["stdout"] != "keys=GH_PAT" {
		t.Fatalf("ref lease: %+v", summary)
	}
}

func TestSkillRefNeverReadsForeignLocalState(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "self-env.json"), []byte(`{"GITHUB_TOKEN":"owner-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	storeRoot, indexRoot := t.TempDir(), t.TempDir()
	bobStore, bobIndex := filepath.Join(storeRoot, "bob"), filepath.Join(indexRoot, "bob")
	if err := os.MkdirAll(bobStore, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bobIndex, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bobIndex, "index.json"), []byte(`{"skills":[{"name":"shared","eligible":true,"requires":{"credentials":[{"name":"GH_PAT","ref":"GITHUB_TOKEN"}]}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SKILLS_BY_PRINCIPAL", indexRoot)
	t.Setenv("HUB_SKILLS_STORE_BY", storeRoot)
	env := skillLeaseEnv(t.Context(), EffectiveBinding{Binding: ToolBinding{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", PolicyVersion: "p"}}, AgentExecRequest{Arguments: map[string]any{"skill": "shared"}})
	if len(env) != 0 {
		t.Fatalf("foreign principal received a ref lease")
	}
}

// DockerScratchExec drives inspect → exec-pack → isolated run → confirmed
// removal, all over the fixed docker verbs; guards fail closed before docker.
func TestDockerScratchExecContract(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-scratch-1"}}
	scratch := DockerScratchExec([]string{os.Args[0], "-test.run=TestFakeDockerChild"}, FixedAgentContainer("rt-1"), nil)

	guards := []AgentExecRequest{
		{Arguments: map[string]any{}},
		{Arguments: map[string]any{"command": " "}},
		{Arguments: map[string]any{"command": "ls", "path": "../evil"}},
		{Arguments: map[string]any{"command": "ls", "path": "ok.txt"}, Scopes: []CapabilityScope{{PathArgument: "path", PathPrefix: "other"}}},
	}
	for i, req := range guards {
		if _, err := scratch(ctx, effective, req, nil); err == nil {
			t.Fatalf("guard %d accepted", i)
		}
	}
	badRuntime := DockerScratchExec(nil, func(EffectiveBinding) (string, error) { return "", errors.New("no runtime") }, nil)
	if _, err := badRuntime(ctx, effective, AgentExecRequest{Arguments: map[string]any{"command": "ls"}}, nil); err == nil {
		t.Fatal("runtime resolution failure must propagate")
	}

	res, err := scratch(ctx, effective, AgentExecRequest{
		Tool:           "code_exec",
		Arguments:      map[string]any{"command": "ls /scratch", "path": "ws"},
		Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatalf("scratch exec: %v", err)
	}
	summary, _ := res.Result.(map[string]any)
	if summary["exit_code"] != 0 || summary["stdout"] != "done" || summary["stopped"] != true || summary["tree_stopped"] != true {
		t.Fatalf("summary: %+v", summary)
	}
}

// R8.3 multi-principal: the lease resolves the CALLER's index+store — a
// sibling's credential names/values never come from the owner's mounts.
func TestDockerScratchExecSkillEnvPerPrincipal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ownerStore, ownerEff := t.TempDir(), t.TempDir()
	bobEff := filepath.Join(t.TempDir(), "bob")
	bobStore := filepath.Join(t.TempDir(), "bob")
	if err := skillstore.WriteCredentials(ownerStore, "demo", map[string]string{"API_KEY": "own3r"}); err != nil {
		t.Fatal(err)
	}
	if err := skillstore.WriteCredentials(bobStore, "demo", map[string]string{"BOB_KEY": "b0bv4l"}); err != nil {
		t.Fatal(err)
	}
	ownerIndex := `{"skills":[{"name":"demo","eligible":true,"requires":{"credentials":[{"name":"API_KEY"}]}}]}`
	bobIndex := `{"skills":[{"name":"demo","eligible":true,"requires":{"credentials":[{"name":"BOB_KEY"}]}}]}`
	for _, pair := range [][2]string{{filepath.Join(ownerEff, "index.json"), ownerIndex}, {filepath.Join(bobEff, "index.json"), bobIndex}} {
		if err := os.MkdirAll(filepath.Dir(pair[0]), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair[0], []byte(pair[1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HUB_SKILLS_STORE", ownerStore)
	t.Setenv("HUB_SKILLS_EFFECTIVE", ownerEff)
	t.Setenv("HUB_SKILLS_BY_PRINCIPAL", filepath.Dir(bobEff))
	t.Setenv("HUB_SKILLS_STORE_BY", filepath.Dir(bobStore))

	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-scratch-pp", PrincipalID: "bob"}}
	scratch := DockerScratchExec([]string{os.Args[0], "-test.run=TestFakeDockerEnvChild"}, FixedAgentContainer("rt-1"), nil)
	res, err := scratch(ctx, effective, AgentExecRequest{
		Tool:           "code_exec",
		Arguments:      map[string]any{"command": "env", "path": "ws", "skill": "demo"},
		Scopes:         []CapabilityScope{{PathArgument: "path", PathPrefix: "ws"}},
		TimeoutSeconds: 30,
	}, nil)
	if err != nil {
		t.Fatalf("scratch exec: %v", err)
	}
	summary, _ := res.Result.(map[string]any)
	if summary["stdout"] != "keys=BOB_KEY" {
		t.Fatalf("sibling lease resolved owner mounts: %+v", summary)
	}
}
