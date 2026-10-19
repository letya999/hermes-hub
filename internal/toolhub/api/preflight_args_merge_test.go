package api

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestTransportArgRewrites(t *testing.T) {
	if NetworkTransportValue([]string{"--transport", "sse"}) != "sse" ||
		NetworkTransportValue([]string{"-t", "http"}) != "streamable-http" ||
		NetworkTransportValue([]string{"-t", "streamable_http"}) != "streamable-http" ||
		NetworkTransportValue([]string{"--transport"}) != "" ||
		NetworkTransportValue([]string{"--other", "x"}) != "" {
		t.Fatal("transport detection wrong")
	}
	if got := StdioTransportArgs([]string{"serve", "--transport", "sse"}); !slices.Equal(got, []string{"serve", "--transport", "stdio"}) {
		t.Fatalf("rewrite: %v", got)
	}
	same := []string{"serve", "--stdio"}
	if got := StdioTransportArgs(same); &got[0] != &same[0] && !slices.Equal(got, same) {
		t.Fatalf("unchanged argv: %v", got)
	}
	imported := ImportedArtifact{Definition: ToolDefinition{
		Source: DefinitionSource{Args: []string{"--transport", "sse"}},
	}}
	if got := PreflightCommandArgs(imported); !slices.Equal(got, []string{"--transport", "stdio"}) {
		t.Fatalf("command args: %v", got)
	}
	imported.Definition.Source.Args = []string{"--stdio"}
	if got := PreflightCommandArgs(imported); got != nil {
		t.Fatalf("stdio argv rewritten: %v", got)
	}
	// recipe entrypoint tail as fallback
	imported.Definition.Source.Args = nil
	imported.Recipe.Entrypoint = []string{"server", "-t", "http"}
	if got := PreflightCommandArgs(imported); !slices.Equal(got, []string{"-t", "stdio"}) {
		t.Fatalf("entrypoint args: %v", got)
	}
	got := WithoutNetworkNone([]string{"run", "--network", "none", "--name", "x"})
	if slices.Contains(got, "none") || !slices.Contains(got, "--name") {
		t.Fatalf("network none not stripped: %v", got)
	}
}

func TestPreflightDockerRunArgsContract(t *testing.T) {
	imported := ImportedArtifact{Definition: ToolDefinition{
		Source:             DefinitionSource{Image: "img:1", Digest: "sha256:" + strings.Repeat("a", 64)},
		RuntimeEnvironment: map[string]string{"LOG_LEVEL": "warn"},
		Execution: ExecutionPolicy{Mounts: []Mount{
			{Target: "/data"}, {Target: "../escape"}, {Target: "relative"},
		}},
	}}
	args := PreflightDockerRunArgs(imported, "pf-1", []string{"--rm"}, []string{"--flag"}, "--extra")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm", "--name pf-1", "hermes-hub.role=artifact-preflight",
		"--read-only", "--cap-drop ALL", "--network none", "--user 10001:10001",
		"--pids-limit 64", "--env LOG_LEVEL=warn", "--extra",
		"img:1@sha256:" + strings.Repeat("a", 64), "--flag", "/data",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "escape") {
		t.Fatal("unsafe mount leaked")
	}
	// archive images carry no @digest suffix
	imported.Definition.Source.ArchiveDigest = "sha256:" + strings.Repeat("b", 64)
	args = PreflightDockerRunArgs(imported, "pf-2", nil, nil)
	if slices.Contains(args, "img:1@sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("archive image got digest suffix")
	}
	idx := slices.Index(args, "img:1")
	if idx < 0 {
		t.Fatalf("archive image missing: %v", args)
	}
	// entrypoint-covered mount target is not repeated as a trailing arg
	imported.Recipe.Entrypoint = []string{"run", "/data"}
	args = PreflightDockerRunArgs(imported, "pf-3", nil, nil)
	count := 0
	for _, a := range args {
		if a == "/data" {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("mount target repeated: %v", args)
	}
}

func TestPreflightCredentialArgsContract(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	ctx := WithProbeOptions(context.Background(), ProbeOptions{Values: map[string]string{
		"API_TOKEN": "real-secret",
		"EMPTY_ONE": "  ",
	}})
	imported := ImportedArtifact{Definition: ToolDefinition{
		Source:      DefinitionSource{Repository: "https://github.com/nonexistent-owner/nonexistent-repo"},
		Credentials: []CredentialInput{{Name: "API_TOKEN", Required: true}, {Name: "EMPTY_ONE"}},
	}}
	args, cleanup, err := PreflightCredentialArgs(ctx, imported)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	idx := slices.Index(args, "--env-file")
	if idx < 0 {
		t.Fatalf("env file arg missing: %v", args)
	}
	body, err := os.ReadFile(args[idx+1])
	if err != nil || !strings.Contains(string(body), "API_TOKEN=real-secret") || strings.Contains(string(body), "EMPTY_ONE") {
		t.Fatalf("env file: %v %q", err, body)
	}
	// CRLF in submitted secret fails closed
	bad := WithProbeOptions(context.Background(), ProbeOptions{Values: map[string]string{"API_TOKEN": "a\nb"}})
	if _, _, err := PreflightCredentialArgs(bad, imported); err == nil {
		t.Fatal("CRLF credential accepted")
	}
	// unauthenticated probe: required credentials get placeholder env
	args, cleanup, err = PreflightCredentialArgs(context.Background(), imported)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !slices.Contains(args, "API_TOKEN=preflight") {
		t.Fatalf("placeholder env missing: %v", args)
	}
	if slices.Contains(args, "EMPTY_ONE=preflight") {
		t.Fatal("optional credential got placeholder")
	}
	if ProbeOptionsFrom(context.Background()).Values != nil {
		t.Fatal("ambient probe options leaked")
	}
	if ProbeOptionsFrom(context.TODO()).Values != nil {
		t.Fatal("unscoped ctx probe options")
	}
}
