package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exec-pack streams exactly the admitted scope: regular files under the
// prefix, never symlinks or outside-root content.
func TestExecPackStreamsScopedInputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "sub", "b.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "docs", "escape.txt")
	if err := os.Symlink(filepath.Join(workspace, "secret.txt"), link); err != nil {
		t.Skip("symlinks unavailable")
	}
	t.Setenv("HUB_WORKSPACE", workspace)

	body, _ := json.Marshal(api.ScratchPackRequest{
		Path:   "docs",
		Scopes: []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}},
	})
	stdinFile := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	out := captureOutput(t, func() error { return run(ctx, []string{"exec-pack"}) })
	os.Stdin = old
	_ = in.Close()

	got := map[string]string{}
	reader := tar.NewReader(strings.NewReader(out))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("pack output is not a tar: %v", err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		got[header.Name] = string(body)
	}
	if got["a.txt"] != "alpha" || got["sub/b.txt"] != "beta" || len(got) != 2 {
		t.Fatalf("pack leaked or lost scoped files: %v", got)
	}
	for name := range got {
		if strings.Contains(name, "escape") || strings.Contains(name, "secret") {
			t.Fatalf("pack exported a symlink target: %v", got)
		}
	}
}

// exec-scratch runs the script on private scratch and exports only what it
// left in the export directory — nothing else escapes the workload.
func TestExecScratchRunsAndExports(t *testing.T) {
	ctx := context.Background()
	scratch := t.TempDir()
	outputs := t.TempDir()
	t.Setenv("HUB_SCRATCH_DIR", scratch)
	t.Setenv("HUB_SCRATCH_OUTPUTS", outputs)

	var inputs bytes.Buffer
	writer := tar.NewWriter(&inputs)
	_ = writer.WriteHeader(&tar.Header{Name: "seed.txt", Mode: 0o600, Size: 4, Typeflag: tar.TypeReg})
	_, _ = writer.Write([]byte("seed"))
	_ = writer.Close()

	request := api.ScratchExecRequest{
		Command:   "cat \"$INPUTS/seed.txt\" > \"$OUTPUTS/result.txt\"; echo done",
		InputsTar: base64.StdEncoding.EncodeToString(inputs.Bytes()),
	}
	body, _ := json.Marshal(request)
	stdinFile := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	out := captureOutput(t, func() error { return run(ctx, []string{"exec-scratch"}) })
	os.Stdin = old
	_ = in.Close()

	var result api.ScratchExecResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("no bounded JSON result: %q", out)
	}
	if result.ExitCode != 0 || !result.Stopped || !strings.Contains(result.Stdout, "done") {
		t.Fatalf("unexpected run result: %+v", result)
	}
	exported, err := base64.StdEncoding.DecodeString(result.ExportTar)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(exported))
	header, err := reader.Next()
	if err != nil || header.Name != "result.txt" {
		t.Fatalf("export tar missing result: %v %+v", err, header)
	}
	body2, _ := io.ReadAll(reader)
	if string(body2) != "seed" {
		t.Fatalf("export content wrong: %q", body2)
	}
}

// Skill credential leases arrive on the stdin request (R8.3): only env-shaped,
// non-reserved names join the sanitized workload env; control-plane and
// sandbox-owned names can never be overridden.
func TestExecScratchLeasesSkillEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HUB_SCRATCH_DIR", t.TempDir())
	t.Setenv("HUB_SCRATCH_OUTPUTS", t.TempDir())

	request := api.ScratchExecRequest{
		Command: "echo \"val=$SKILL_API_KEY val2=$LOWER hub=$HUB_TOKEN path=$PATH badname=$(printenv 'bad name' 2>/dev/null || echo MISSING) home=$HOME\"",
		Env: map[string]string{
			"SKILL_API_KEY": "s3cr3t",
			"HUB_TOKEN":     "forged",
			"PATH":          "/evil",
			"HOME":          "/evil",
			"bad name":      "x",
			"LOWER":         "ok",
		},
	}
	body, _ := json.Marshal(request)
	stdinFile := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	out := captureOutput(t, func() error { return run(ctx, []string{"exec-scratch"}) })
	os.Stdin = old
	_ = in.Close()

	var result api.ScratchExecResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("no bounded JSON result: %q", out)
	}
	if !strings.Contains(result.Stdout, "val=s3cr3t") || !strings.Contains(result.Stdout, "val2=ok") {
		t.Fatalf("leased env did not reach the workload: %q", result.Stdout)
	}
	if strings.Contains(result.Stdout, "hub=forged") || strings.Contains(result.Stdout, "path=/evil") ||
		!strings.Contains(result.Stdout, "badname=MISSING") || strings.Contains(result.Stdout, "home=/evil") {
		t.Fatalf("reserved env override leaked: %q", result.Stdout)
	}

	// A lease over the 16-name bound degrades to nothing rather than
	// silently trimming an attacker's extra payload.
	fat := api.ScratchExecRequest{Command: "echo \"k=$K1\"", Env: map[string]string{}}
	for i := 0; i < 17; i++ {
		fat.Env[fmt.Sprintf("K%d", i)] = "v"
	}
	body, _ = json.Marshal(fat)
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err = os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = in
	out = captureOutput(t, func() error { return run(ctx, []string{"exec-scratch"}) })
	os.Stdin = old
	_ = in.Close()
	result = api.ScratchExecResult{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("no bounded JSON result: %q", out)
	}
	if !strings.Contains(result.Stdout, "k=\n") && !strings.Contains(result.Stdout, "k= ") {
		t.Fatalf("oversized lease partially applied: %q", result.Stdout)
	}
}

// A file-level grant packs exactly the one file; a path outside the admitted
// scope is denied at the os.Root boundary.
func TestExecPackFileBoundaryAndScopeDenial(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "other.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)
	pack := func(t *testing.T, request api.ScratchPackRequest) (string, error) {
		t.Helper()
		body, _ := json.Marshal(request)
		stdinFile := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(stdinFile)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = in
		var runErr error
		out := captureOutput(t, func() error { runErr = run(ctx, []string{"exec-pack"}); return nil })
		os.Stdin = old
		_ = in.Close()
		return out, runErr
	}
	scope := []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs/a.txt"}}
	out, err := pack(t, api.ScratchPackRequest{Path: "docs/a.txt", Scopes: scope})
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(strings.NewReader(out))
	header, err := reader.Next()
	if err != nil || header.Name != "a.txt" {
		t.Fatalf("file-level pack failed: %v %v", header, err)
	}
	// A path outside the scope prefix resolves inside the scope boundary and
	// fails there — never reading the workspace at large.
	if _, err = pack(t, api.ScratchPackRequest{Path: "other.txt", Scopes: scope}); err == nil {
		t.Fatal("out-of-scope path packed")
	}
	// No scopes at all still packs (whole-domain grant).
	out, err = pack(t, api.ScratchPackRequest{Path: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("whole-domain pack lost files: %q", out)
	}
}

// Traversals and malformed requests never reach the workspace.
func TestExecPackRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HUB_WORKSPACE", t.TempDir())
	for _, request := range []api.ScratchPackRequest{
		{Path: "../escape"},
		{Path: "/abs"},
		{Path: "a/../b"},
		{Path: "does-not-exist"},
	} {
		body, _ := json.Marshal(request)
		stdinFile := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(stdinFile)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = in
		runErr := run(ctx, []string{"exec-pack"})
		os.Stdin = old
		_ = in.Close()
		if runErr == nil {
			t.Fatalf("exec-pack accepted %q", request.Path)
		}
	}
}

// Malformed requests and invalid commands never run the script.
func TestExecScratchRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HUB_SCRATCH_DIR", t.TempDir())
	t.Setenv("HUB_SCRATCH_OUTPUTS", t.TempDir())
	stdinFile := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(stdinFile, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	runErr := run(ctx, []string{"exec-scratch"})
	os.Stdin = old
	_ = in.Close()
	if runErr == nil {
		t.Fatal("exec-scratch accepted malformed JSON")
	}
	// An empty command reports a bounded result, never executes.
	body, _ := json.Marshal(api.ScratchExecRequest{Command: ""})
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err = os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = in
	out := captureOutput(t, func() error { return run(ctx, []string{"exec-scratch"}) })
	os.Stdin = old
	_ = in.Close()
	var result api.ScratchExecResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Stderr == "" {
		t.Fatalf("empty command produced no error result: %q %v", out, err)
	}
	// A corrupt input tar fails the unpack step, not silently.
	body, _ = json.Marshal(api.ScratchExecRequest{Command: "echo hi", InputsTar: "!!!not-base64!!!"})
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err = os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = in
	out = captureOutput(t, func() error { return run(ctx, []string{"exec-scratch"}) })
	os.Stdin = old
	_ = in.Close()
	result = api.ScratchExecResult{}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !strings.Contains(result.Stderr, "unpack") {
		t.Fatalf("corrupt tar produced no unpack error: %q %v", out, err)
	}
}
