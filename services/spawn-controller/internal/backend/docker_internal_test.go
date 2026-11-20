package backend

// Internal docker-backend tests: the argv builder and the CLI child
// environment are what keep secrets and hostile env out of the docker
// CLI itself — these land where the code lives so the mutation probes
// die here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecArgsNeverCarryEnvValues kills the M3 mutant: argv carries -e
// NAME pairs only — a value anywhere in argv means the secret channel
// leaked (X4).
func TestExecArgsNeverCarryEnvValues(t *testing.T) {
	d := &dockerBackend{cfg: DockerConfig{ContainerPrefix: "hermes-hub-wk-"}}
	secret := "ghp_" + strings.Repeat("x", 32)
	args, err := d.execArgs("wk-abc123", ExecSpec{
		Argv: []string{"sh", "-c", "echo hi"},
		Env:  []string{"GH_TOKEN=" + secret, "PATH=/usr/bin:/bin", "EMPTY="},
		Dir:  "/work",
	})
	if err != nil {
		t.Fatalf("execArgs: %v", err)
	}
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, secret) || strings.Contains(joined, "GH_TOKEN="+secret) {
		t.Fatalf("env value reached argv: %q", args)
	}
	// Names ride -e flags; values never appear at all — not even
	// NAME=value forms.
	for i, a := range args {
		if a == "-e" {
			if i+1 >= len(args) || strings.Contains(args[i+1], "=") {
				t.Fatalf("-e carries a value: %v", args)
			}
		}
	}
	if !strings.Contains(joined, "-e\x00GH_TOKEN") || !strings.Contains(joined, "-e\x00PATH") {
		t.Fatalf("env names missing from argv: %v", args)
	}
	if _, err := d.execArgs("wk-abc123", ExecSpec{Argv: []string{"true"}, Env: []string{"no-equals"}}); err == nil {
		t.Fatal("malformed env entry accepted")
	}
}

// TestDockerCLIEnvIsControllerOwned: the CLI child sees an allowlist of
// the controller's environment plus spec env — a stray host variable and
// spec-supplied DOCKER_* names cannot redirect the CLI (spec DOCKER_* is
// refused upstream; the allowlist is the last belt).
func TestDockerCLIEnvIsControllerOwned(t *testing.T) {
	t.Setenv("HUB_STRAY_CONTROLLER_VAR", "should-not-pass")
	t.Setenv("PATH", os.Getenv("PATH"))
	d := &dockerBackend{cfg: DockerConfig{}}
	env := d.dockerCLIEnv([]string{"TOOL_VAR=present"})
	has := func(name string) (string, bool) {
		for _, entry := range env {
			k, v, _ := strings.Cut(entry, "=")
			if k == name {
				return v, true
			}
		}
		return "", false
	}
	if _, ok := has("HUB_STRAY_CONTROLLER_VAR"); ok {
		t.Fatal("stray controller env reached the docker CLI child")
	}
	if v, ok := has("TOOL_VAR"); !ok || v != "present" {
		t.Fatal("spec env value missing from the CLI child environment")
	}
	if _, ok := has("PATH"); !ok {
		t.Fatal("operator PATH missing from the CLI child environment")
	}
}

// TestDockerCLIEnvPinsDockerConfig: spec env appends to the CLI's own
// environment for -e forwarding, so a spec HOME (or a spec DOCKER_CONFIG)
// would redirect where the CLI reads its client config — auth and
// contexts. The controller pins DOCKER_CONFIG to its own directory after
// spec env, exactly once.
func TestDockerCLIEnvPinsDockerConfig(t *testing.T) {
	d := &dockerBackend{cfg: DockerConfig{StateRoot: t.TempDir()}}
	env := d.dockerCLIEnv([]string{"HOME=/attacker/home", "DOCKER_CONFIG=/attacker/.docker"})
	want := filepath.Join(d.cfg.StateRoot, "docker-config")
	var pins []string
	for _, entry := range env {
		if k, v, _ := strings.Cut(entry, "="); k == "DOCKER_CONFIG" {
			pins = append(pins, v)
		}
	}
	if len(pins) != 1 || pins[0] != want {
		t.Fatalf("DOCKER_CONFIG must be pinned once to %q, env %v", want, env)
	}
}

// TestDockerListOwnsOnlyItsPrefix: the managed label is shared, so the
// reconciler's inventory must exclude foreign-prefix containers and
// sidecars — a worker's proxy listed as an orphan would be stopped
// mid-exec.
func TestDockerListOwnsOnlyItsPrefix(t *testing.T) {
	d := &dockerBackend{cfg: DockerConfig{ContainerPrefix: "hermes-hub-wk-"}}
	dump := `{"Names":"hermes-hub-wk-aaa","State":"running","Labels":"hermes-hub.managed=spawn-controller,hermes-hub.space=alice"}
{"Names":"hermes-hub-rt-bbb","State":"running","Labels":"hermes-hub.managed=spawn-controller,hermes-hub.space=alice"}
{"Names":"hermes-hub-wk-aaa-proxy","State":"running","Labels":"hermes-hub.managed=spawn-controller,hermes-hub.sidecar=proxy"}
{"Names":"hermes-hub-wk-aaa-cred","State":"running","Labels":"hermes-hub.managed=spawn-controller,hermes-hub.sidecar=cred"}
`
	list := d.parseList(dump)
	if len(list) != 1 || list[0].ID != "aaa" {
		t.Fatalf("parseList=%+v, want only this backend's aaa container", list)
	}
}
