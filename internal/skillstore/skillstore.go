// Package skillstore is the hub-owned skill store (ADR-0084 D1/D4/D5/D6,
// SPEC-0083 R2.*). One store per owner: spaces/<user>/skills (user),
// <org>/skills (org), config/skills (platform), plus read-only catalog
// sources. Published skills live at <store>/<name>/ with hub-private state
// under .drafts/, .revisions/, .archive/, .state/ and .sources/ — none of
// which is ever copied into the agent-visible effective view.
package skillstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	SkillFile    = "SKILL.md"
	draftsDir    = ".drafts"
	revisionsDir = ".revisions"
	archiveDir   = ".archive"
	stateDir     = ".state"
	sourcesDir   = ".sources"
	indexFile    = "index.json"
	locksDir     = "locks"
)

// Quotas (SPEC-0083 R4.5) enforced on every tree write.
const (
	MaxFiles        = 256
	MaxFileBytes    = 1 << 20
	MaxTreeBytes    = 8 << 20
	MaxSkillMDBytes = 64 << 10
	MaxSkills       = 500
	MaxDraftsPerDay = 50
	reservedPrefix  = "hermes-hub-"
	portablePathMax = 512
)

// Scope identifies which owner a store belongs to; reserved hermes-hub-*
// names are writable only in ScopePlatform.
type Scope string

const (
	ScopePlatform Scope = "platform"
	ScopeOrg      Scope = "org"
	ScopeUser     Scope = "user"
	ScopeCatalog  Scope = "hermes" // read-only bundled catalog source
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

	ErrNotFound     = errors.New("skill not found")
	ErrConflict     = errors.New("stale base revision")
	ErrReserved     = errors.New("name is reserved for platform skills")
	ErrInvalid      = errors.New("invalid skill")
	ErrQuota        = errors.New("skill quota exceeded")
	ErrDraftExists  = errors.New("draft already exists")
	ErrScanCritical = errors.New("scan found critical findings")
	ErrNeedsReview  = errors.New("skill requires draft review before publishing")
)

// ValidName reports whether name is an agentskills.io-compliant name:
// lowercase segments joined by single hyphens, max 64 chars.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// ReservedName reports whether name occupies the platform-reserved prefix.
func ReservedName(name string) bool { return strings.HasPrefix(name, reservedPrefix) }

// File is one entry of a skill tree. Keys are portable slash paths relative
// to the skill root (SKILL.md, references/x.md, scripts/run.py, ...).
type File struct {
	Data []byte `json:"-"`
	Exec bool   `json:"exec,omitempty"`
}

// Tree is a whole skill directory in memory (bounded by MaxTreeBytes).
type Tree map[string]File

// IndexMeta is the publish-time metadata recorded in .state/index.json.
type IndexMeta struct {
	Author       string  `json:"author"` // user|agent|review|import|git|operator
	Source       string  `json:"source"` // user|org|platform|import|git|learned|rollback
	BaseRevision string  `json:"base_revision,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	Origin       *Origin `json:"origin,omitempty"`
}

// Origin records tracked source metadata for git/import installs.
type Origin struct {
	SourceID    string `json:"source_id,omitempty"`
	URL         string `json:"url,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Path        string `json:"path,omitempty"`
	TreeHash    string `json:"tree_hash,omitempty"`
	FetchedAt   string `json:"fetched_at,omitempty"`
	ScanVerdict string `json:"scan_verdict,omitempty"`
}

// Store is one owner's on-disk store. All writes go through a per-skill
// lock + atomic directory swap; there is no other writer.
type Store struct {
	root  string
	scope Scope
}

// Open creates the store root on first use, rejecting a symlinked root so
// a store can never point outside the owner space.
func Open(root string, scope Scope) (*Store, error) {
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: symlinked store root %s", ErrInvalid, root)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Store{root: root, scope: scope}, nil
}

// Root returns the store root on disk.
func (s *Store) Root() string { return s.root }

// Scope returns the owner scope the store was opened for.
func (s *Store) Scope() Scope { return s.scope }

func (s *Store) dir(name string) string { return filepath.Join(s.root, name) }

// Names lists published skill names (top-level dirs containing SKILL.md).
func (s *Store) Names() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.dir(e.Name()), SkillFile)); err == nil {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// Tree loads the published tree of one skill, applying the persisted exec
// list (hub state — permission bits do not survive every host filesystem).
func (s *Store) Tree(name string) (Tree, error) {
	tree, err := loadTree(s.dir(name))
	if err != nil {
		return nil, err
	}
	for _, path := range s.readExec(name) {
		if f, ok := tree[path]; ok {
			f.Exec = true
			tree[path] = f
		}
	}
	return tree, nil
}

// Manifest leniently parses the published SKILL.md of name, returning the
// active content revision alongside.
func (s *Store) Manifest(name string) (*Manifest, string, error) {
	tree, err := s.Tree(name)
	if err != nil {
		return nil, "", err
	}
	m, _, err := ParseSkillMD(name, tree[SkillFile].Data, true)
	if err != nil {
		return nil, "", err
	}
	return m, Revision(tree), nil
}

// loadTree reads a skill directory into memory.
func loadTree(dir string) (Tree, error) {
	tree := Tree{}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrInvalid
	}
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Name() == ".draft.json" || d.Name() == ".exec" {
			return nil // draft metadata is hub-private
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s is not allowed in a skill", ErrInvalid, filepath.ToSlash(rel))
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path) // #nosec G122 G304 -- walked path inside a validated skill dir
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = File{Data: body, Exec: fi.Mode().Perm()&0o111 != 0}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tree, nil
}

// validateTree enforces path safety and quotas on an in-memory tree.
func validateTree(tree Tree) error {
	if len(tree) == 0 || tree[SkillFile].Data == nil {
		return fmt.Errorf("%w: SKILL.md is required", ErrInvalid)
	}
	if len(tree) > MaxFiles {
		return fmt.Errorf("%w: %d files (max %d)", ErrQuota, len(tree), MaxFiles)
	}
	var total int64
	for path, f := range tree {
		if err := validRelPath(path); err != nil {
			return err
		}
		size := int64(len(f.Data))
		if size > MaxFileBytes {
			return fmt.Errorf("%w: %s is %d bytes (max %d)", ErrQuota, path, len(f.Data), MaxFileBytes)
		}
		if path == SkillFile && size > MaxSkillMDBytes {
			return fmt.Errorf("%w: SKILL.md is %d bytes (max %d)", ErrQuota, size, MaxSkillMDBytes)
		}
		total += size
	}
	if total > MaxTreeBytes {
		return fmt.Errorf("%w: tree exceeds %d bytes", ErrQuota, MaxTreeBytes)
	}
	return lintRefs(tree)
}

// markdownLink matches [text](target) links; target is group 1.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)`)

// lintRefs enforces one-level references (D1/R7.1): SKILL.md links must
// resolve to existing tree files; other files may not chain to tree files.
// External and anchor links pass.
func lintRefs(tree Tree) error {
	for path, f := range tree {
		isSkillMD := path == SkillFile
		for _, m := range markdownLink.FindAllStringSubmatch(string(f.Data), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
				continue
			}
			if isSkillMD {
				if _, ok := tree[target]; !ok {
					return fmt.Errorf("%w: SKILL.md link %q does not resolve inside the skill", ErrInvalid, target)
				}
			} else {
				return fmt.Errorf("%w: %s links to %q — resource files may not chain", ErrInvalid, path, target)
			}
		}
	}
	return nil
}

// validRelPath enforces portable slash paths inside a skill tree.
func validRelPath(path string) error {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path {
		return fmt.Errorf("%w: non-clean path %q", ErrInvalid, path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || strings.HasPrefix(seg, ".") {
			return fmt.Errorf("%w: bad path segment in %q", ErrInvalid, path)
		}
	}
	return nil
}

// writeTree materializes a tree into dir (dir must not exist).
func writeTree(dir string, tree Tree) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for path, f := range tree {
		dest := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if f.Exec {
			mode = 0755
		}
		if err := os.WriteFile(dest, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

// Publish retains the outgoing revision and atomically swaps the live
// directory. An identical save is a no-op returning the current revision;
// a stale BaseRevision conflicts. Script-bearing trees are refused — they
// must pass the draft+approve path instead (R4.4).
func (s *Store) Publish(name string, tree Tree, meta IndexMeta) (string, error) {
	return s.publish(name, tree, meta, false)
}

func (s *Store) publish(name string, tree Tree, meta IndexMeta, viaDraft bool) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("%w: name %q must be lowercase segments joined by hyphens", ErrInvalid, name)
	}
	if s.scope != ScopePlatform && ReservedName(name) {
		return "", fmt.Errorf("%w: %q", ErrReserved, name)
	}
	if err := validateTree(tree); err != nil {
		return "", err
	}
	if _, _, err := ParseSkillMD(name, tree[SkillFile].Data, false); err != nil {
		return "", err
	}
	report := scanTree(tree)
	if report.Verdict() == "critical" {
		return "", scanError(report)
	}
	if !viaDraft && hasScripts(tree) {
		return "", fmt.Errorf("%w: scripts/ present — publish via draft approval", ErrNeedsReview)
	}
	unlock, err := s.lock(name)
	if err != nil {
		return "", err
	}
	defer unlock()
	var currentRev string
	current, err := s.Tree(name)
	switch {
	case errors.Is(err, ErrNotFound):
		if names, lerr := s.Names(); lerr == nil && len(names) >= MaxSkills {
			return "", fmt.Errorf("%w: %d skills (max %d)", ErrQuota, len(names), MaxSkills)
		}
	case err != nil:
		return "", err
	default:
		currentRev = Revision(current)
	}
	if meta.BaseRevision != "" && currentRev != "" && meta.BaseRevision != currentRev {
		return "", fmt.Errorf("%w: base %s, current %s", ErrConflict, shortHash(meta.BaseRevision), shortHash(currentRev))
	}
	rev := Revision(tree)
	if rev == currentRev {
		return rev, nil // identical save
	}
	if err := s.retainRevision(name, current); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(s.root, ".publish-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := writeTree(tmp, tree); err != nil {
		return "", err
	}
	if err := swapDir(tmp, s.dir(name)); err != nil {
		return "", err
	}
	if err := s.writeExec(name, execPaths(tree)); err != nil {
		return "", err
	}
	if err := s.record(name, rev, meta); err != nil {
		return "", err
	}
	s.audit("published", name)
	return rev, nil
}

// retainRevision snapshots the outgoing live tree into
// .revisions/<name>/<rev> so Rollback can restore it (R2.5).
func (s *Store) retainRevision(name string, tree Tree) error {
	if len(tree) == 0 {
		return nil
	}
	rev := Revision(tree)
	dir := filepath.Join(s.root, revisionsDir, name, rev)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := writeTree(dir, tree); err != nil {
			return err
		}
	}
	if execs := execPaths(tree); len(execs) > 0 {
		return writeJSONFile(dir+".exec", append(mustJSON(execs), '\n'))
	}
	return nil
}

// RevisionTree reads a retained revision without republishing it.
func (s *Store) RevisionTree(name, rev string) (Tree, error) {
	tree, err := loadTree(filepath.Join(s.root, revisionsDir, name, rev))
	if err != nil {
		return nil, err
	}
	for _, path := range readExecList(filepath.Join(s.root, revisionsDir, name, rev+".exec")) {
		if f, ok := tree[path]; ok {
			f.Exec = true
			tree[path] = f
		}
	}
	return tree, nil
}

// Rollback republishes a retained revision as a new revision, restoring
// the exec flags recorded beside that revision.
func (s *Store) Rollback(name, rev string) (string, error) {
	tree, err := s.RevisionTree(name, rev)
	if err != nil {
		return "", err
	}
	back, err := s.publish(name, tree, IndexMeta{Author: "user", Source: "rollback"}, true)
	if err == nil {
		s.audit("rolled_back", name)
	}
	return back, err
}

// Archive moves the live skill into .archive/<name>/<ts>/ and removes it
// from the live listing. Nothing is deleted (D10).
func (s *Store) Archive(name, reason, absorbedInto, actor string) error {
	unlock, err := s.lock(name)
	if err != nil {
		return err
	}
	defer unlock()
	src := s.dir(name)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	dest := filepath.Join(s.root, archiveDir, name, stampNow())
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err != nil {
		return err
	}
	s.audit("archived", name)
	return nil
}

// RestoreAt restores a specific archive stamp; empty stamp means newest.
// A live skill under the same name conflicts — archive it first.
func (s *Store) RestoreAt(name, stamp string) error {
	unlock, err := s.lock(name)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(s.dir(name)); err == nil {
		return fmt.Errorf("%w: live skill already exists", ErrConflict)
	}
	if stamp == "" {
		entries, err := os.ReadDir(filepath.Join(s.root, archiveDir, name))
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() > stamp {
				stamp = e.Name()
			}
		}
		if stamp == "" {
			return ErrNotFound
		}
	}
	if err := os.Rename(filepath.Join(s.root, archiveDir, name, stamp), s.dir(name)); err != nil {
		return err
	}
	s.audit("restored", name)
	return nil
}

// Restore moves the newest archive entry back to the live location.
func (s *Store) Restore(name string) error {
	return s.RestoreAt(name, "")
}

// ArchiveTree reads an archived entry without touching it; stamp "" selects
// the newest. Returns the tree and the stamp it came from.
func (s *Store) ArchiveTree(name, stamp string) (Tree, string, error) {
	base := filepath.Join(s.root, archiveDir, name)
	if stamp == "" {
		entries, err := os.ReadDir(base)
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() > stamp {
				stamp = e.Name()
			}
		}
		if stamp == "" {
			return nil, "", ErrNotFound
		}
	}
	tree, err := loadTree(filepath.Join(base, stamp))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	return tree, stamp, nil
}

// swapDir atomically replaces target with the prepared tmp dir. On Windows
// a rename over an existing non-empty dir fails, so the old dir is parked.
func swapDir(tmp, target string) error {
	trash := target + ".swap-" + randSuffix()
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, trash); err != nil {
			return fmt.Errorf("park existing %s: %w", filepath.Base(target), err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		// Best effort rollback so a failed publish never loses the live skill.
		_ = os.Rename(trash, target)
		return err
	}
	_ = os.RemoveAll(trash)
	return nil
}
