package stack

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// R10.3: the skillgit plan is admitted as exact tuples — one per allowed git
// host — so git never lands on UserCommands where owner-registered CLI specs
// could run it without the hardened argv the gitsource runner emits.
func TestSkillGitApprovalsAreTupleScoped(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	cfg := cliControllerSection(s, "")
	if slices.Contains(cfg.UserCommands, "git") {
		t.Fatal("git must stay out of UserCommands — owner specs carry no hardened argv")
	}
	want := api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 256 << 10, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64}
	var hosts []string
	for _, approval := range cfg.Approved {
		if approval.DefinitionID != "skillgit" {
			continue
		}
		if approval.DefinitionVersion != "1.0.0" || approval.Command != "git" {
			t.Fatalf("skillgit approval identity drifted: %+v", approval)
		}
		got := approval.Execution
		if got.TimeoutSeconds != want.TimeoutSeconds || got.OutputBytes != want.OutputBytes || got.CPUMillis != want.CPUMillis ||
			got.MemoryMiB != want.MemoryMiB || got.MaxPIDs != want.MaxPIDs || len(got.Egress) != 1 {
			t.Fatalf("skillgit approval policy drifted: %+v", got)
		}
		hosts = append(hosts, got.Egress[0])
	}
	for _, host := range []string{"github.com", "gitlab.com"} {
		if !slices.Contains(hosts, host) {
			t.Fatalf("no skillgit tuple for %s: %v", host, hosts)
		}
		if !slices.Contains(cfg.AllowedEgress, host) {
			t.Fatalf("egress host %s missing from allowlist", host)
		}
	}
}

func TestSkillGitApprovalsCoverGitLabHost(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		GitLabHost: "gitlab.example.io"}
	cfg := cliControllerSection(s, "")
	var found bool
	for _, approval := range cfg.Approved {
		if approval.DefinitionID == "skillgit" && slices.Contains(approval.Execution.Egress, "gitlab.example.io") {
			found = true
		}
	}
	if !found {
		t.Fatal("a configured gitlab_host needs its own skillgit egress tuple")
	}
	if !slices.Contains(cfg.AllowedEgress, "gitlab.example.io") {
		t.Fatal("gitlab_host missing from CLI egress allowlist")
	}
}

// R10.3: communication-hub runs the skillgit ExecCell, so it needs the
// controller /admit endpoint (CLIExecFromEnv derives /cli-exec from it) and
// the toolhub.auth env file that carries HUB_TOOLHIVE_ADMISSION_TOKEN.
func TestGatewayReachesCliExec(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.Ingress = []string{"telegram"}
	services := Compose(s, "", dir)["services"].(M)
	gateway, ok := services["communication-hub"].(M)
	if !ok {
		t.Fatal("communication-hub service missing")
	}
	env := gateway["environment"].(M)
	if env["HUB_TOOLHIVE_ADMISSION_ENDPOINT"] != "http://workload-controller:8545/admit" {
		t.Fatalf("gateway cannot derive /cli-exec: %v", env["HUB_TOOLHIVE_ADMISSION_ENDPOINT"])
	}
	var authed bool
	for _, raw := range gateway["env_file"].([]any) {
		if raw.(M)["path"] == filepath.ToSlash(filepath.Join(dir, "toolhub.auth")) {
			authed = true
		}
	}
	if !authed {
		t.Fatal("gateway env_file missing toolhub.auth — no admission token")
	}
}

func TestGatewaySeesGitLabHost(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.Ingress = []string{"telegram"}
	s.GitLabHost = "gitlab.example.io"
	services := Compose(s, "", dir)["services"].(M)
	env := services["communication-hub"].(M)["environment"].(M)
	if env["HUB_GITLAB_HOST"] != "gitlab.example.io" {
		t.Fatal("gitlab_host not rendered to the gateway")
	}
}
