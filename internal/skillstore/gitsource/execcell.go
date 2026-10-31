package gitsource

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// ExecCell is the production GitOps: every git invocation runs inside a
// bounded /cli-exec sandbox cell on the controller (SPEC-0083 R10.3). The
// cell carries the owner mirror as its single rw workspace bind and the
// repository host as its only egress; credentials reach it only through the
// GIT_TOKEN environment variable, never argv, URLs or config.
type ExecCell struct {
	Exec      api.CLIExecFunc
	Principal string
	ContextID string
	Token     string
}

const (
	skillGitPlan    = "skillgit"
	skillGitVersion = "1.0.0"
	cellWorkDir     = "/work"
	exportTarName   = ".export.tar"
	sourceHeadRef   = "refs/remotes/source/head"
	sourcePrevRef   = "refs/remotes/source/prev"
	// ponytail: ancestry is proven within a 512-commit gap; deeper update
	// distances report force_push — a safe over-warning on giant jumps.
	ancestryDeepen = "512"
)

var (
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	mirrorIDPattern = regexp.MustCompile(`^[0-9a-f]{16}\.git$`)
)

// Fetch implements GitOps by driving hardened git argv through /cli-exec.
func (c ExecCell) Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	if c.Exec == nil {
		return FetchResult{}, fmt.Errorf("%w: cli-exec runner not configured", ErrInvalid)
	}
	if !mirrorIDPattern.MatchString(filepath.Base(req.Mirror)) {
		return FetchResult{}, fmt.Errorf("%w: mirror path", ErrInvalid)
	}
	// Ref, path and prev-commit land in argv — a leading dash would let a
	// crafted provenance record smuggle git flags into the cell.
	if strings.HasPrefix(req.Ref, "-") || strings.HasPrefix(req.Path, "-") || strings.HasPrefix(req.PrevCommit, "-") {
		return FetchResult{}, fmt.Errorf("%w: fetch arguments", ErrInvalid)
	}
	host, err := repoHost(req.Repo)
	if err != nil {
		return FetchResult{}, err
	}
	if req.HeadOnly {
		commit, err := c.resolve(ctx, req, host)
		if err != nil {
			return FetchResult{}, err
		}
		return FetchResult{Commit: commit}, nil
	}
	if _, err := c.git(ctx, req, host, "init", "--bare", "--shared=everybody"); err != nil {
		return FetchResult{}, err
	}
	for _, kv := range [][2]string{
		{"remote.origin.url", req.Repo},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
	} {
		if _, err := c.git(ctx, req, host, "config", kv[0], kv[1]); err != nil {
			return FetchResult{}, err
		}
	}
	commit, err := c.resolve(ctx, req, host)
	if err != nil {
		return FetchResult{}, err
	}
	result := FetchResult{Commit: commit}
	if _, err = c.git(ctx, req, host, "fetch", "--depth=1", "--filter=blob:none", "origin", commit+":"+sourceHeadRef); err != nil {
		return FetchResult{}, err
	}
	if req.PrevCommit != "" {
		forcePush, err := c.ancestry(ctx, req, host, commit)
		if err != nil {
			return FetchResult{}, err
		}
		result.ForcePush = forcePush
	}
	if req.Names {
		out, err := c.git(ctx, req, host, "ls-tree", "-r", "--name-only", sourceHeadRef)
		if err != nil {
			return FetchResult{}, err
		}
		result.Tree = skillstore.Tree{}
		for _, name := range strings.Split(out, "\n") {
			if name = strings.TrimSpace(name); name != "" {
				result.Tree[name] = skillstore.File{}
			}
		}
		return result, nil
	}
	archiveArgs := []string{"archive", "--format=tar", "-o", cellWorkDir + "/" + exportTarName, sourceHeadRef}
	if req.Path != "" {
		archiveArgs = append(archiveArgs, req.Path)
	}
	if _, err = c.git(ctx, req, host, archiveArgs...); err != nil {
		return FetchResult{}, err
	}
	if result.Tree, result.Dropped, err = parseExport(filepath.Join(req.Mirror, exportTarName), req.Path); err != nil {
		return FetchResult{}, err
	}
	return result, nil
}

// ancestry proves the previous commit still reaches the resolved head; a
// rewritten history reports force_push (R10.7). The shallow boundary hides
// long ranges, so ancestry is probed once, deepened, then probed again.
func (c ExecCell) ancestry(ctx context.Context, req FetchRequest, host, head string) (bool, error) {
	if _, code, err := c.run(ctx, req, host, "cat-file", "-e", req.PrevCommit+"^{commit}"); err != nil {
		return false, err
	} else if code != 0 {
		// The mirror lost the object (fresh dir, gc): refetch it so
		// merge-base can decide; an unreachable prev then fails ancestry.
		_, _ = c.git(ctx, req, host, "fetch", "--depth=1", "origin", req.PrevCommit+":"+sourcePrevRef)
	}
	ancestor, err := c.isAncestor(ctx, req, host, req.PrevCommit)
	if err != nil {
		return false, err
	}
	if ancestor {
		return false, nil
	}
	if _, err = c.git(ctx, req, host, "fetch", "--deepen="+ancestryDeepen, "origin", head+":"+sourceHeadRef); err != nil {
		return false, err
	}
	return c.isAncestorNegated(ctx, req, host, req.PrevCommit)
}

func (c ExecCell) isAncestor(ctx context.Context, req FetchRequest, host, commit string) (bool, error) {
	negated, err := c.isAncestorNegated(ctx, req, host, commit)
	return !negated, err
}

func (c ExecCell) isAncestorNegated(ctx context.Context, req FetchRequest, host, commit string) (bool, error) {
	_, code, err := c.run(ctx, req, host, "merge-base", "--is-ancestor", commit, sourceHeadRef)
	if err != nil {
		return false, err
	}
	return code != 0, nil
}

// resolve pins req.Ref to an immutable commit: a 40-hex ref is already a
// SHA, an empty ref asks the remote for its default HEAD, anything else is
// resolved through ls-remote.
func (c ExecCell) resolve(ctx context.Context, req FetchRequest, host string) (string, error) {
	if commitPattern.MatchString(req.Ref) {
		return req.Ref, nil
	}
	var out string
	var err error
	if req.Ref == "" {
		out, err = c.git(ctx, req, host, "ls-remote", "--symref", req.Repo, "HEAD")
	} else {
		out, err = c.git(ctx, req, host, "ls-remote", req.Repo, req.Ref)
	}
	if err != nil {
		return "", err
	}
	var commit, peeled string
	for _, line := range strings.Split(out, "\n") {
		sha, name, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if !commitPattern.MatchString(sha) {
			continue
		}
		if req.Ref == "" && name == "HEAD" {
			commit = sha
		}
		if req.Ref != "" && (name == "refs/heads/"+req.Ref || name == "refs/tags/"+req.Ref) {
			commit = sha
		}
		if name == "refs/tags/"+req.Ref+"^{}" {
			peeled = sha
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	if commit == "" {
		return "", fmt.Errorf("%w: ref %q not found upstream", ErrInvalid, req.Ref)
	}
	return commit, nil
}

// git runs one hardened invocation and returns captured output; a non-zero
// exit is an error carrying bounded output only.
func (c ExecCell) git(ctx context.Context, req FetchRequest, host string, args ...string) (string, error) {
	out, code, err := c.run(ctx, req, host, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%w: git %s: %s", ErrInvalid, args[0], strings.TrimSpace(out))
	}
	return out, nil
}

func (c ExecCell) run(ctx context.Context, req FetchRequest, host string, args ...string) (string, int, error) {
	resp, err := c.Exec(ctx, c.request(req, host, args))
	if err != nil {
		return "", -1, err
	}
	if resp.Error != "" {
		return "", -1, fmt.Errorf("%w: %s", ErrInvalid, resp.Error)
	}
	return resp.Output, resp.ExitCode, nil
}

// request builds the immutable /cli-exec request for one git invocation.
func (c ExecCell) request(req FetchRequest, host string, sub []string) api.CliExecRequest {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "submodule.recurse=false",
		"-c", "filter.lfs.required=false",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.clean=",
		// The mirror bind's mountpoint is owned by the cell host, not the
		// exec uid — git's dubious-ownership guard must be scoped to /work
		// or every repo-touching call fails "not in a git directory".
		"-c", "safe.directory=" + cellWorkDir,
		"-C", cellWorkDir,
	}
	var env []string
	if c.Token != "" {
		// The helper runs inside the cell with GIT_TOKEN from env; the
		// token never enters argv, the remote URL or git config.
		args = append(args,
			"-c", "credential.helper=",
			"-c", `credential.helper=!f(){ echo "username=oauth2"; echo "password=$GIT_TOKEN"; };f`)
		env = []string{"GIT_TOKEN=" + c.Token}
	}
	args = append(args, sub...)
	return api.CliExecRequest{
		Plan: api.ControllerPlan{
			WorkloadID:        skillGitPlan,
			DefinitionID:      skillGitPlan,
			DefinitionVersion: skillGitVersion,
			Command:           "git",
			Execution: api.ExecutionPolicy{
				TimeoutSeconds: 60, OutputBytes: 256 << 10, CPUMillis: 1000,
				MemoryMiB: 512, MaxPIDs: 64, Egress: []string{host},
			},
		},
		Command:   "git",
		Args:      args,
		Env:       env,
		Principal: c.Principal,
		ContextID: c.ContextID,
		BindingID: skillGitPlan,
		Workspace: c.workspace(req),
	}
}

// workspace binds the owner mirror rw at /work for content fetches; a
// head-only resolve binds nothing.
func (c ExecCell) workspace(req FetchRequest) api.CliWorkspace {
	if req.HeadOnly {
		return api.CliWorkspace{}
	}
	return api.CliWorkspace{Scope: "skill-source", Path: filepath.Base(req.Mirror), Access: "rw"}
}

// exportTransportMax bounds the archive the cell produces. It deliberately
// exceeds the skill-tree byte quota: vendored noise (node_modules) is
// filtered while streaming, so an archive may carry bytes the selected
// skill tree never keeps.
const exportTransportMax = 4 * skillstore.MaxTreeBytes

// parseExport turns the archive tar the cell wrote under the mirror into a
// Tree. Reserved/vendored paths are skipped while streaming — before the
// tree quota applies — and returned relative to prefix for the import
// audit.
func parseExport(name, prefix string) (skillstore.Tree, []string, error) {
	f, err := os.Open(name) // #nosec G703 -- mirror path is built from a validated source id
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(name)
	}()
	// The bound lives on the archive file, not a truncated reader — a
	// LimitReader cut mid-entry surfaces as a bare parse error, and a cut
	// inside tar padding silently yields an empty tree.
	if st, err := f.Stat(); err != nil {
		return nil, nil, err
	} else if st.Size() > exportTransportMax {
		return nil, nil, fmt.Errorf("%w: archive exceeds %d MiB transport bound", ErrInvalid, exportTransportMax>>20)
	}
	tr := tar.NewReader(f)
	tree := skillstore.Tree{}
	var dropped []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return tree, dropped, nil
		}
		if err != nil {
			return nil, nil, ErrInvalid
		}
		// git archive wraps the file payload in a global pax header and
		// directory entries; both carry no content and are skipped (the
		// reader already folds per-file pax/GNU long-name records into the
		// next header). Anything else that is not a regular file —
		// symlinks and gitlinks (mode 0160000) — is rejected (R10.5).
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Mode&0o170000 == 0o160000 {
			return nil, nil, fmt.Errorf("%w: archive entry %q", ErrInvalid, hdr.Name)
		}
		if strings.HasPrefix(hdr.Name, "../") {
			return nil, nil, fmt.Errorf("%w: archive path %q", ErrInvalid, hdr.Name)
		}
		rel := hdr.Name
		if prefix != "" {
			rel = strings.TrimPrefix(rel, strings.TrimSuffix(prefix, "/")+"/")
		}
		if hasDotSegment(rel) {
			dropped = append(dropped, rel)
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, ErrInvalid
		}
		tree[hdr.Name] = skillstore.File{Data: data, Exec: hdr.Mode&0o111 != 0}
	}
}

func repoHost(repo string) (string, error) {
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("%w: repository URL", ErrInvalid)
	}
	return strings.ToLower(u.Host), nil
}
