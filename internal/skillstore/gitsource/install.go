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
// Dropped names reserved/vendored archive paths the transport filtered out
// before the tree quota even applied.
type FetchResult struct {
	Commit    string
	ForcePush bool
	Tree      skillstore.Tree
	Dropped   []string
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
// repository requires choosing from. Dropped lists dot paths the export
// filtered out; Notes carries the draft's import audit lines.
type InstallResult struct {
	DraftID    string      `json:"draft_id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Commit     string      `json:"commit,omitempty"`
	SourceID   string      `json:"source_id,omitempty"`
	Dropped    []string    `json:"dropped_dot_paths,omitempty"`
	Notes      []string    `json:"import_notes,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// SourceID identifies one repository mirror per owner store.
func SourceID(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(h[:])[:16]
}

// clearStaleMirrorLock removes a shallow.lock a killed fetch cell left
// behind — it bricks every later depth-limited fetch ("Unable to create
// shallow.lock") while carrying no live state; the mirror is single-owner.
func clearStaleMirrorLock(mirror string) {
	_ = os.Remove(filepath.Join(mirror, "shallow.lock")) // #nosec G703 -- mirror path is built from a validated source id
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
	clearStaleMirrorLock(mirror)
	// "." selects the repository root itself: the whole tree is the skill,
	// so the fetch exports everything and no subtree filter applies.
	rootSkill := src.Path == "."
	reqPath := src.Path
	if rootSkill {
		reqPath = ""
	}
	got, err := git.Fetch(ctx, FetchRequest{Mirror: mirror, Repo: src.Repo, Ref: src.Ref, Path: reqPath, Names: src.Path == ""})
	if err != nil {
		return InstallResult{}, err
	}
	var candidate Candidate
	switch {
	case rootSkill:
		if opts.Name != "" {
			candidate.Name = opts.Name
		} else {
			candidate.Name = rootSkillName(src.Repo, got.Tree)
		}
	case src.Path != "":
		candidate = Candidate{Path: src.Path, Name: path.Base(src.Path)}
		if opts.Name != "" {
			candidate.Name = opts.Name
		}
	default:
		candidates := DiscoverSkills(got.Tree, "")
		// Discovery fetched names only, so the root candidate gets the
		// repo basename — "." alone tells the caller nothing.
		for i := range candidates {
			if candidates[i].Path == "." {
				candidates[i].Name = path.Base(src.Repo)
			}
		}
		// A registry slug resolves against the discovered candidates —
		// exactly one match installs; none or several still return the
		// list so the caller can pick explicitly.
		if src.Slug == "" {
			return InstallResult{Commit: got.Commit, SourceID: id, Candidates: candidates}, nil
		}
		var found []Candidate
		for _, c := range candidates {
			if c.Name == src.Slug {
				found = append(found, c)
			}
		}
		if len(found) != 1 {
			return InstallResult{Commit: got.Commit, SourceID: id, Candidates: candidates}, nil
		}
		candidate = found[0]
		if candidate.Path == "." {
			candidate.Path = ""
		}
		got, err = git.Fetch(ctx, FetchRequest{Mirror: mirror, Repo: src.Repo, Ref: src.Ref, Path: candidate.Path})
		if err != nil {
			return InstallResult{}, err
		}
		if candidate.Path == "" {
			candidate.Name = rootSkillName(src.Repo, got.Tree)
		}
	}
	skill, dropped := exportTree(got.Tree, candidate.Path)
	if opts.Name == "" {
		// The frontmatter name is authoritative — publish requires
		// name == dir, so a repo directory name is only a fallback.
		candidate.Name = skillstore.NameFromTree(skill, candidate.Name)
	}
	opts.Meta.Imported = true
	opts.Meta.ImportNotes = dropNotes(append(got.Dropped, dropped...))
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

// dropNotes summarizes the filtered dot paths for the draft card —
// hub-reserved names and repo plumbing never reach the skill tree.
func dropNotes(dropped []string) []string {
	return skillstore.DropNotes(dropped)
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
	clearStaleMirrorLock(mirror)
	got, err := git.Fetch(ctx, FetchRequest{Mirror: mirror, Repo: origin.URL, Ref: origin.Ref, Path: origin.Path, PrevCommit: origin.Commit})
	if err != nil {
		return UpdateResult{}, err
	}
	out := UpdateResult{Name: name, Commit: got.Commit, PrevCommit: origin.Commit, ForcePush: got.ForcePush}
	if got.Commit == origin.Commit {
		out.UpToDate = true
		return out, nil
	}
	skill, dropped := exportTree(got.Tree, origin.Path)
	current, err := st.Tree(name)
	if err != nil {
		return UpdateResult{}, err
	}
	meta.BaseRevision = skillstore.Revision(current)
	meta.Imported = true
	meta.ImportNotes = dropNotes(append(got.Dropped, dropped...))
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

// exportTree selects prefix's subtree out of an exported repo tree; "" is
// the whole repository. Dot-segment paths are dropped and reported —
// dot-space is hub-reserved (.origin.json, .state, .drafts) and repo
// plumbing like .github/ or .gitignore is never skill content.
func exportTree(tree skillstore.Tree, prefix string) (skillstore.Tree, []string) {
	out := skillstore.Tree{}
	var dropped []string
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
	}
	for file, f := range tree {
		rel, ok := strings.CutPrefix(file, prefix)
		if !ok || rel == "" {
			continue
		}
		if hasDotSegment(rel) {
			dropped = append(dropped, rel)
			continue
		}
		out[rel] = f
	}
	return out, dropped
}

func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		// Dot-space is hub-reserved and repo plumbing (.github, .origin.json)
		// is never skill content; node_modules is reproducible vendored
		// noise that would otherwise exhaust the file/byte quotas.
		if seg == ".." || strings.HasPrefix(seg, ".") || seg == "node_modules" {
			return true
		}
	}
	return false
}

// rootSkillName resolves a root install's skill name: the SKILL.md
// frontmatter is authoritative (publish requires name == dir); the repo
// basename is the fallback when frontmatter is absent or unreadable.
func rootSkillName(repo string, tree skillstore.Tree) string {
	if m, _, err := skillstore.ParseSkillMD("", tree[skillstore.SkillFile].Data, true); err == nil && m.Name != "" {
		return m.Name
	}
	return path.Base(repo)
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
