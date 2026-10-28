package gitsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// FetchRequest is one bounded git operation against the per-owner mirror.
// HeadOnly resolves the ref's commit without exporting any content. Path
// limits the export to one repo subtree ("" is the repo root). Names asks
// for paths only — entries carry no Data; used for skill discovery so a
// large repository never streams its blobs (R10.5).
type FetchRequest struct {
	Mirror     string
	Repo       string
	Ref        string
	Path       string
	PrevCommit string
	HeadOnly   bool
	Names      bool
}

// FetchResult is the exported repo tree plus the resolved immutable commit.
type FetchResult struct {
	Commit    string
	ForcePush bool
	Tree      skillstore.Tree
}

// GitOps is the runner seam: production executes hardened git inside a
// bounded cell; tests substitute a fake.
type GitOps interface {
	Fetch(ctx context.Context, req FetchRequest) (FetchResult, error)
}

// Origin is the hub-owned provenance record stored as .origin.json inside
// the published skill directory — never agent-visible (R10.6).
type Origin struct {
	SourceID  string `json:"source_id"`
	URL       string `json:"url"`
	Ref       string `json:"ref,omitempty"`
	Commit    string `json:"commit"`
	Path      string `json:"path,omitempty"`
	TreeSHA   string `json:"tree_sha,omitempty"`
	Scan      string `json:"scan,omitempty"`
	FetchedAt string `json:"fetched_at"`
}

// OriginFile is the hub-private filename inside a skill directory.
const OriginFile = ".origin.json"

// InstallOptions carries URL plus explicit overrides.
type InstallOptions struct {
	URL        string
	Ref        string
	Path       string
	Name       string
	GitLabHost string
	Meta       skillstore.DraftMeta
}

// InstallResult reports the produced draft or the candidates a multi-skill
// repository requires choosing from.
type InstallResult struct {
	DraftID    string      `json:"draft_id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Commit     string      `json:"commit,omitempty"`
	SourceID   string      `json:"source_id,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// SourceID identifies one repository mirror per owner store.
func SourceID(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(h[:])[:16]
}

// Install fetches the source tree and creates a scanned draft pinned to the
// resolved commit (R10.6).
func Install(ctx context.Context, st *skillstore.Store, opts InstallOptions, git GitOps) (InstallResult, error) {
	src, err := ParseSource(opts.URL, opts.GitLabHost)
	if err != nil {
		return InstallResult{}, err
	}
	if opts.Ref != "" {
		src.Ref = opts.Ref
	}
	if opts.Path != "" {
		src.Path = opts.Path
	}
	id := SourceID(src.Repo)
	mirror := filepath.Join(st.Root(), ".sources", id+".git")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		return InstallResult{}, err
	}
	got, err := git.Fetch(ctx, FetchRequest{Mirror: mirror, Repo: src.Repo, Ref: src.Ref, Path: src.Path, Names: src.Path == ""})
	if err != nil {
		return InstallResult{}, err
	}
	var candidate Candidate
	if src.Path != "" {
		candidate = Candidate{Path: src.Path, Name: path.Base(src.Path)}
		if opts.Name != "" {
			candidate.Name = opts.Name
		}
	} else {
		return InstallResult{Commit: got.Commit, SourceID: id, Candidates: DiscoverSkills(got.Tree, "")}, nil
	}
	skill := skillstore.Tree{}
	prefix := ""
	if candidate.Path != "" {
		prefix = strings.TrimSuffix(candidate.Path, "/") + "/"
	}
	for file, f := range got.Tree {
		if rel, ok := strings.CutPrefix(file, prefix); ok && rel != "" {
			skill[rel] = f
		}
	}
	draft, err := st.CreateDraft(candidate.Name, skill, opts.Meta)
	if err != nil {
		return InstallResult{}, err
	}
	origin := Origin{SourceID: id, URL: src.Repo, Ref: src.Ref, Commit: got.Commit, Path: candidate.Path,
		TreeSHA: treeDigest(skill), Scan: draft.Meta.ScanVerdict, FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	body, err := json.Marshal(origin)
	if err != nil {
		return InstallResult{}, err
	}
	if err := st.DraftSidecar(draft.ID, OriginFile, body); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{DraftID: draft.ID, Name: candidate.Name, Commit: got.Commit, SourceID: id}, nil
}

// UpdateResult reports an update draft or that the ref did not move.
type UpdateResult struct {
	DraftID    string `json:"draft_id,omitempty"`
	Name       string `json:"name"`
	Commit     string `json:"commit"`
	PrevCommit string `json:"prev_commit"`
	ForcePush  bool   `json:"force_push,omitempty"`
	UpToDate   bool   `json:"up_to_date,omitempty"`
}

// Update refetches an installed skill's tracked ref; a moved ref produces a
// new scanned draft and a rewritten history sets ForcePush (R10.6).
func Update(ctx context.Context, st *skillstore.Store, name string, git GitOps, meta skillstore.DraftMeta) (UpdateResult, error) {
	origin, err := ReadOrigin(st, name)
	if err != nil {
		return UpdateResult{}, err
	}
	mirror := filepath.Join(st.Root(), ".sources", origin.SourceID+".git")
	got, err := git.Fetch(ctx, FetchRequest{Mirror: mirror, Repo: origin.URL, Ref: origin.Ref, Path: origin.Path, PrevCommit: origin.Commit})
	if err != nil {
		return UpdateResult{}, err
	}
	out := UpdateResult{Name: name, Commit: got.Commit, PrevCommit: origin.Commit, ForcePush: got.ForcePush}
	skill := skillstore.Tree{}
	prefix := ""
	if origin.Path != "" {
		prefix = strings.TrimSuffix(origin.Path, "/") + "/"
	}
	for file, f := range got.Tree {
		if rel, ok := strings.CutPrefix(file, prefix); ok && rel != "" {
			skill[rel] = f
		}
	}
	current, err := st.Tree(name)
	if err != nil {
		return UpdateResult{}, err
	}
	meta.BaseRevision = skillstore.Revision(current)
	draft, err := st.CreateDraft(name, skill, meta)
	if err != nil {
		return UpdateResult{}, err
	}
	origin.Commit = got.Commit
	origin.TreeSHA = treeDigest(skill)
	origin.Scan = draft.Meta.ScanVerdict
	origin.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	body, err := json.Marshal(origin)
	if err != nil {
		return UpdateResult{}, err
	}
	if err := st.DraftSidecar(draft.ID, OriginFile, body); err != nil {
		return UpdateResult{}, err
	}
	out.DraftID = draft.ID
	return out, nil
}

// treeDigest is the origin record's content hash of the exported tree.
func treeDigest(tree skillstore.Tree) string {
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write(tree[name].Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ReadOrigin loads the hub-private .origin.json of a published skill.
func ReadOrigin(st *skillstore.Store, name string) (Origin, error) {
	var origin Origin
	body, err := os.ReadFile(filepath.Join(st.Root(), name, OriginFile)) // #nosec G703 -- skill names are validated identifiers
	if err != nil {
		return origin, fmt.Errorf("%w: skill %q has no tracked source", ErrInvalid, name)
	}
	if err := json.Unmarshal(body, &origin); err != nil {
		return origin, ErrInvalid
	}
	return origin, nil
}

// UpdateReport is one tracked skill's upstream state for check_updates.
type UpdateReport struct {
	Name    string `json:"name"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Update  bool   `json:"update,omitempty"`
}

// CheckUpdates resolves each source-tracked skill's ref head and reports
// which moved — content is never fetched into the store (R10.8).
func CheckUpdates(ctx context.Context, st *skillstore.Store, git GitOps) ([]UpdateReport, error) {
	names, err := st.Names()
	if err != nil {
		return nil, err
	}
	var out []UpdateReport
	for _, name := range names {
		origin, err := ReadOrigin(st, name)
		if err != nil {
			continue
		}
		report := UpdateReport{Name: name, Current: origin.Commit}
		got, err := git.Fetch(ctx, FetchRequest{
			Mirror: filepath.Join(st.Root(), ".sources", origin.SourceID+".git"),
			Repo:   origin.URL, Ref: origin.Ref, HeadOnly: true,
		})
		if err != nil {
			return nil, err
		}
		if got.Commit != origin.Commit {
			report.Latest, report.Update = got.Commit, true
		}
		out = append(out, report)
	}
	return out, nil
}
