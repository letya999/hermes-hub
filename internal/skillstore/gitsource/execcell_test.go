package gitsource

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// cliExecStub stands in for the controller's /cli-exec: it records every
// request and answers from substring rules; an archive call materialises the
// prepared tar at the bound /work path on the host.
type cliExecStub struct {
	mirror  string
	reqs    []api.CliExecRequest
	tar     []byte
	outs    map[string]string
	codes   map[string]int
	respErr string
}

func (s *cliExecStub) run(_ context.Context, req api.CliExecRequest) (api.CliExecResponse, error) {
	s.reqs = append(s.reqs, req)
	joined := strings.Join(req.Args, " ")
	for i, arg := range req.Args {
		if arg == "-o" && i+1 < len(req.Args) && strings.HasPrefix(req.Args[i+1], "/work/") {
			if err := os.WriteFile(filepath.Join(s.mirror, strings.TrimPrefix(req.Args[i+1], "/work/")), s.tar, 0o644); err != nil { // #nosec G703 -- test path under t.TempDir
				return api.CliExecResponse{}, err
			}
		}
	}
	for sub, code := range s.codes {
		if strings.Contains(joined, sub) {
			return api.CliExecResponse{Output: "exit " + string(rune('0'+code)), ExitCode: code}, nil
		}
	}
	if s.respErr != "" {
		return api.CliExecResponse{Error: s.respErr}, nil
	}
	for sub, out := range s.outs {
		if strings.Contains(joined, sub) {
			return api.CliExecResponse{Output: out}, nil
		}
	}
	return api.CliExecResponse{}, nil
}

func tarOf(t *testing.T, files map[string][2]int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, spec := range files {
		mode, size := spec[0], spec[1]
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: int64(size), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(bytes.Repeat([]byte("x"), size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExecCellFetchesCommitAndExportsTree(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("ab", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{
		"skills/pdf/SKILL.md": {0o644, 4},
		"skills/pdf/run.sh":   {0o755, 3},
	}), outs: map[string]string{"ls-remote": commit + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{
		Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main", Path: "skills/pdf",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != commit {
		t.Fatalf("commit %q", got.Commit)
	}
	if len(got.Tree) != 2 || got.Tree["skills/pdf/run.sh"].Exec != true || len(got.Tree["skills/pdf/SKILL.md"].Data) != 4 {
		t.Fatalf("exported tree %+v", got.Tree)
	}
	// Every request is the skillgit plan running hardened git in the
	// skill-source cell with the repo host as its only egress.
	var subcommands []string
	for _, req := range stub.reqs {
		plan := req.Plan
		if plan.WorkloadID != "skillgit" || plan.DefinitionID != "skillgit" || plan.DefinitionVersion != "1.0.0" || plan.Command != "git" {
			t.Fatalf("plan %+v", plan)
		}
		if req.Command != "git" || req.Principal != "alice" || req.ContextID != "alice" || req.BindingID != "skillgit" {
			t.Fatalf("request identity %+v", req)
		}
		egress := plan.Execution.Egress
		if len(egress) != 1 || egress[0] != "github.com" {
			t.Fatalf("egress %v", egress)
		}
		if req.Workspace.Scope != "skill-source" || req.Workspace.Path != "97393e7e6b5ab8df.git" || req.Workspace.Access != "rw" {
			t.Fatalf("workspace %+v", req.Workspace)
		}
		for _, pair := range [][2]string{
			{"-c", "core.hooksPath=/dev/null"},
			{"-c", "core.fsmonitor=false"},
			{"-c", "protocol.file.allow=never"},
			{"-c", "protocol.allow=never"},
			{"-c", "protocol.https.allow=always"},
			{"-c", "submodule.recurse=false"},
			{"-c", "filter.lfs.required=false"},
			{"-c", "filter.lfs.smudge="},
			{"-c", "filter.lfs.clean="},
			{"-c", "safe.directory=" + cellWorkDir},
		} {
			found := false
			for i := 0; i+1 < len(req.Args); i++ {
				if req.Args[i] == pair[0] && req.Args[i+1] == pair[1] {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s %s missing from %v", pair[0], pair[1], req.Args)
			}
		}
		for _, arg := range req.Args {
			if strings.HasPrefix(arg, "-") && arg != "-c" && arg != "-C" && arg != "--bare" && arg != "--shared=everybody" &&
				arg != "--depth=1" && arg != "--filter=blob:none" && arg != "--deepen=512" && arg != "--format=tar" &&
				arg != "--name-only" && arg != "--is-ancestor" && arg != "--symref" && arg != "-o" && arg != "-e" && arg != "-r" {
				t.Fatalf("unapproved git flag %q in %v", arg, req.Args)
			}
		}
		i := 0
		for i+1 < len(req.Args) && (req.Args[i] == "-c" || req.Args[i] == "-C") {
			i += 2
		}
		if i >= len(req.Args) {
			t.Fatalf("no git subcommand in %v", req.Args)
		}
		subcommands = append(subcommands, req.Args[i])
	}
	joined := strings.Join(subcommands, " ")
	for _, want := range []string{"init", "ls-remote", "fetch", "archive"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in request sequence %v", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(mirror, ".export.tar")); !os.IsNotExist(err) {
		t.Fatalf("export tar must be cleaned up")
	}
}

func TestExecCellResolvesDefaultBranchViaSymref(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("cd", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs: map[string]string{"ls-remote": "ref: refs/heads/trunk\tHEAD\n" + commit + "\tHEAD\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != commit {
		t.Fatalf("commit %q", got.Commit)
	}
	var saw bool
	for _, req := range stub.reqs {
		for i, arg := range req.Args {
			if arg == "--symref" && i+2 < len(req.Args) && req.Args[i+1] == "https://github.com/o/r" && req.Args[i+2] == "HEAD" {
				saw = true
			}
		}
	}
	if !saw {
		t.Fatalf("default ref must resolve via ls-remote --symref HEAD: %v", stub.reqs)
	}
}

func TestExecCellNamesOnlyUsesLsTree(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("ab", 20)
	stub := &cliExecStub{mirror: mirror, outs: map[string]string{
		"ls-remote": commit + "\trefs/heads/main\n",
		"ls-tree":   "skills/pdf/SKILL.md\nskills/docx/SKILL.md\nREADME.md\n",
	}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{
		Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main", Names: true,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Tree) != 3 || got.Tree["skills/pdf/SKILL.md"].Data != nil {
		t.Fatalf("names-only tree %+v", got.Tree)
	}
	for _, req := range stub.reqs {
		joined := strings.Join(req.Args, " ")
		if strings.Contains(joined, "archive") {
			t.Fatalf("names-only fetch must not archive: %v", req.Args)
		}
	}
}

func TestExecCellHeadOnlyResolvesWithoutMirror(t *testing.T) {
	commit := strings.Repeat("ef", 20)
	stub := &cliExecStub{outs: map[string]string{"ls-remote": commit + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{
		Mirror: filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git"),
		Repo:   "https://github.com/o/r", Ref: "main", HeadOnly: true,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != commit {
		t.Fatalf("commit %q", got.Commit)
	}
	for _, req := range stub.reqs {
		joined := strings.Join(req.Args, " ")
		if strings.Contains(joined, "fetch ") || strings.Contains(joined, "archive") || strings.Contains(joined, "init") {
			t.Fatalf("head-only fetch must only ls-remote: %v", req.Args)
		}
		if req.Workspace.Scope != "" && req.Workspace.Scope != "none" {
			t.Fatalf("head-only fetch must not bind a workspace: %+v", req.Workspace)
		}
	}
}

func TestExecCellDetectsForcePush(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("ab", 20)
	prev := strings.Repeat("cd", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs:  map[string]string{"ls-remote": commit + "\trefs/heads/main\n"},
		codes: map[string]int{"merge-base": 1}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{
		Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main", PrevCommit: prev,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !got.ForcePush {
		t.Fatalf("rewritten history must flag force_push")
	}
	var sawDeepen bool
	for _, req := range stub.reqs {
		joined := strings.Join(req.Args, " ")
		if strings.Contains(joined, "merge-base") && !strings.Contains(joined, "--is-ancestor") {
			t.Fatalf("ancestry probe must use --is-ancestor: %v", req.Args)
		}
		if strings.Contains(joined, "--deepen=512") {
			if !strings.Contains(joined, " origin ") {
				t.Fatalf("deepen must use the configured origin remote (promisor filter): %v", req.Args)
			}
			if strings.Contains(joined, commit+":refs/remotes/source/head") {
				sawDeepen = true
			}
		}
	}
	if !sawDeepen {
		t.Fatalf("not-ancestor must deepen the shallow history once before flagging")
	}
}

func TestExecCellRefetchesMissingPrevObject(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("ab", 20)
	prev := strings.Repeat("cd", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs:  map[string]string{"ls-remote": commit + "\trefs/heads/main\n"},
		codes: map[string]int{"cat-file": 128}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{
		Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main", PrevCommit: prev,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ForcePush {
		t.Fatalf("reachable prev commit is not a force push")
	}
	var sawPrevFetch bool
	for _, req := range stub.reqs {
		joined := strings.Join(req.Args, " ")
		if strings.Contains(joined, prev+":refs/remotes/source/prev") {
			if !strings.Contains(joined, " origin ") {
				t.Fatalf("prev fetch must use the configured origin remote: %v", req.Args)
			}
			sawPrevFetch = true
		}
	}
	if !sawPrevFetch {
		t.Fatalf("missing prev object must be refetched before ancestry: %v", stub.reqs)
	}
}

func TestExecCellTokenTravelsAsEnvOnly(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	const token = "ghp_verysecretvalue"
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs: map[string]string{"ls-remote": strings.Repeat("ab", 20) + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice", Token: token}
	if _, err := cell.Fetch(context.Background(), FetchRequest{
		Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main",
	}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(stub.reqs) == 0 {
		t.Fatal("no requests")
	}
	var sawHelper bool
	for _, req := range stub.reqs {
		found := false
		for _, pair := range req.Env {
			if pair == "GIT_TOKEN="+token {
				found = true
			}
			if strings.Contains(pair, token) && !strings.HasPrefix(pair, "GIT_TOKEN=") {
				t.Fatalf("token must travel only as GIT_TOKEN env, got %q", pair)
			}
		}
		if !found {
			t.Fatalf("GIT_TOKEN env missing on %v", req.Args)
		}
		for _, arg := range req.Args {
			if strings.Contains(arg, token) {
				t.Fatalf("token must never enter argv: %v", req.Args)
			}
			if strings.HasPrefix(arg, "credential.helper=!f()") {
				sawHelper = true
			}
		}
	}
	if !sawHelper {
		t.Fatal("credential.helper reading GIT_TOKEN missing from argv")
	}
}

func TestExecCellRejectsSymlinkAndGitlinkEntries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typeflag byte
		mode     int
	}{{name: "evil-link", typeflag: tar.TypeSymlink, mode: 0o777}, {name: "sub", typeflag: tar.TypeReg, mode: 0o160000}} {
		t.Run(tc.name, func(t *testing.T) {
			mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
			if err := os.MkdirAll(mirror, 0o755); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			w := tar.NewWriter(&buf)
			if err := w.WriteHeader(&tar.Header{Name: "SKILL.md", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := w.WriteHeader(&tar.Header{Name: tc.name, Mode: int64(tc.mode), Typeflag: tc.typeflag}); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			stub := &cliExecStub{mirror: mirror, tar: buf.Bytes(),
				outs: map[string]string{"ls-remote": strings.Repeat("ab", 20) + "\trefs/heads/main\n"}}
			cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
			if _, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"}); err == nil {
				t.Fatalf("%s entry must be rejected", tc.name)
			}
		})
	}
}

func TestExecCellSkipsTarMetadataEntries(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	longName := "skills/pdf/references/" + strings.Repeat("deep", 30) + "/SKILL.md"
	for _, hdr := range []*tar.Header{
		{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader},
		{Name: "skills/pdf/", Mode: 0o755, Typeflag: tar.TypeDir},
		{Name: longName, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		{Name: "skills/pdf/SKILL.md", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
	} {
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, tar: buf.Bytes(),
		outs: map[string]string{"ls-remote": strings.Repeat("ab", 20) + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main", Path: "skills/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tree) != 2 || string(got.Tree["skills/pdf/SKILL.md"].Data) != "x" || string(got.Tree[longName].Data) != "x" {
		t.Fatalf("tree %v", got.Tree)
	}
}

// Vendored node_modules can outweigh the real skill payload — the
// transport filters it while streaming the archive so a vendored repo
// stays installable, and names the dropped paths for the import audit.
func TestExecCellFiltersVendoredDuringExport(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, name := range []string{
		"skills/html-book-bundler/SKILL.md",
		"skills/html-book-bundler/node_modules/pkg/index.js",
		"skills/html-book-bundler/node_modules/dep/dist/x.js",
	} {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, tar: buf.Bytes(),
		outs: map[string]string{"ls-remote": strings.Repeat("ab", 20) + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main", Path: "skills/html-book-bundler"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tree) != 1 || got.Tree["skills/html-book-bundler/SKILL.md"].Data == nil {
		t.Fatalf("vendored entries must not reach the tree: %v", got.Tree)
	}
	if len(got.Dropped) != 2 {
		t.Fatalf("dropped entries must be named for the audit: %v", got.Dropped)
	}
	for _, d := range got.Dropped {
		if strings.HasPrefix(d, "skills/") {
			t.Fatalf("dropped names must be relative to the fetched path: %q", d)
		}
	}
}

// The archive is only a transport — vendored trees can outweigh the skill
// quota — but when it exceeds its own bound the caller must see a quota
// explanation, not a bare "invalid skill source" from a truncated parse.
func TestExecCellOversizeArchiveReportsQuota(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, tar: make([]byte, exportTransportMax+1),
		outs: map[string]string{"ls-remote": strings.Repeat("ab", 20) + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	_, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"})
	if err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("oversize archive must report the transport bound, got %v", err)
	}
}

func TestExecCellPropagatesGitFailure(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, codes: map[string]int{"ls-remote": 128}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	_, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"})
	if err == nil {
		t.Fatal("a non-zero git exit must surface as an error")
	}
}

func TestExecCellRejectsBadMirrorID(t *testing.T) {
	stub := &cliExecStub{}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	for _, mirror := range []string{
		filepath.Join(t.TempDir(), "not-a-source-id.git"),
		filepath.Join(t.TempDir(), "..", "x.git"),
	} {
		if _, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"}); err == nil {
			t.Fatalf("mirror %q must be rejected", mirror)
		}
	}
	if len(stub.reqs) != 0 {
		t.Fatalf("no cli-exec request may leave on a bad mirror: %v", stub.reqs)
	}
}

func TestExecCellPinsConfigForBloblessRemote(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("ab", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs: map[string]string{"ls-remote": commit + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	if _, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// A blob:none clone lazily fetches objects through its promisor remote:
	// without url/promisor/filter config, archive and cat-file fail live.
	want := []string{
		"config remote.origin.url https://github.com/o/r",
		"config remote.origin.promisor true",
		"config remote.origin.partialclonefilter blob:none",
	}
	joined := ""
	for _, req := range stub.reqs {
		joined += strings.Join(req.Args, " ") + "\n"
	}
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("missing %q in requests", w)
		}
	}
	if !strings.Contains(joined, "fetch --depth=1 --filter=blob:none origin ") {
		t.Fatalf("fetch must use the configured origin remote")
	}
}

func TestExecCellPinsSHARefWithoutLsRemote(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("f0", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}})}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: commit})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != commit {
		t.Fatalf("commit %q", got.Commit)
	}
	for _, req := range stub.reqs {
		if strings.Contains(strings.Join(req.Args, " "), "ls-remote") {
			t.Fatalf("a 40-hex ref is already pinned and needs no resolve: %v", req.Args)
		}
	}
}

func TestExecCellPrefersPeeledTagCommit(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	tagObj := strings.Repeat("aa", 20)
	commit := strings.Repeat("bb", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs: map[string]string{"ls-remote": tagObj + "\trefs/tags/v1\n" + commit + "\trefs/tags/v1^{}\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "v1"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != commit {
		t.Fatalf("annotated tag must pin the peeled commit, got %q", got.Commit)
	}
}

func TestExecCellResolvesLightweightTag(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("bb", 20)
	stub := &cliExecStub{mirror: mirror, tar: tarOf(t, map[string][2]int{"SKILL.md": {0o644, 1}}),
		outs: map[string]string{"ls-remote": commit + "\trefs/tags/v2\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	got, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "v2"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Commit != commit {
		t.Fatalf("lightweight tag must resolve, got %q", got.Commit)
	}
}

func TestExecCellErrorsOnUnresolvableRef(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, outs: map[string]string{"ls-remote": ""}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	_, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "gone"})
	if err == nil {
		t.Fatal("an upstream-missing ref must fail")
	}
	for _, req := range stub.reqs {
		if strings.Contains(strings.Join(req.Args, " "), "fetch ") {
			t.Fatalf("nothing may be fetched for an unresolved ref: %v", req.Args)
		}
	}
}

func TestExecCellFailsClosedWithoutRunner(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("must fail closed, not panic: %v", r)
		}
	}()
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	cell := ExecCell{Principal: "alice", ContextID: "alice"}
	if _, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"}); err == nil {
		t.Fatal("a missing cli-exec runner must fail closed")
	}
}

func TestExecCellSurfacesControllerError(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, respErr: "output-limit"}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	_, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"})
	if err == nil || !strings.Contains(err.Error(), "output-limit") {
		t.Fatalf("a flagged controller response must surface as an error, got %v", err)
	}
}

func TestExecCellRejectsArgvInjection(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	for _, req := range []FetchRequest{
		{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "--upload-pack=rm"},
		{Mirror: mirror, Repo: "https://github.com/o/r", Path: "--output=/etc/x"},
		{Mirror: mirror, Repo: "https://github.com/o/r", PrevCommit: "--exec-path=/x"},
		{Mirror: mirror, Repo: "file:///etc/passwd", Ref: "main"},
	} {
		if _, err := cell.Fetch(context.Background(), req); err == nil {
			t.Fatalf("request %+v must be rejected", req)
		}
	}
	if len(stub.reqs) != 0 {
		t.Fatalf("no request may leave on argv-shaped input: %v", stub.reqs)
	}
}

func TestExecCellRejectsTraversalTarName(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "97393e7e6b5ab8df.git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stub := &cliExecStub{mirror: mirror, tar: buf.Bytes(),
		outs: map[string]string{"ls-remote": strings.Repeat("ab", 20) + "\trefs/heads/main\n"}}
	cell := ExecCell{Exec: stub.run, Principal: "alice", ContextID: "alice"}
	if _, err := cell.Fetch(context.Background(), FetchRequest{Mirror: mirror, Repo: "https://github.com/o/r", Ref: "main"}); err == nil {
		t.Fatal("a traversal archive path must be rejected")
	}
}
