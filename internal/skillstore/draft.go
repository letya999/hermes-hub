package skillstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/skillstore/scan"
)

// DraftMeta is the provenance envelope every draft carries (R4.4):
// author kind, session, reason, base revision, sticky taint and scan
// verdict. Values come from the hub caller — never from skill text.
type DraftMeta struct {
	Author       string         `json:"author"` // user|agent|review|import|git|operator
	SessionID    string         `json:"session_id,omitempty"`
	Reason       string         `json:"reason"`
	BaseRevision string         `json:"base_revision,omitempty"`
	Action       string         `json:"action,omitempty"` // ""|publish|archive|restore
	AbsorbedInto string         `json:"absorbed_into,omitempty"`
	RestoreStamp string         `json:"restore_stamp,omitempty"`
	Tainted      bool           `json:"tainted,omitempty"`
	TaintSources []string       `json:"taint_sources,omitempty"` // web|inbound|connector|git
	ScanVerdict  string         `json:"scan_verdict,omitempty"`
	ScanFindings []scan.Finding `json:"scan_findings,omitempty"`
}

// Draft is a pending unpublished change under .drafts/<id>/.
type Draft struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt string    `json:"created_at"`
	Meta      DraftMeta `json:"meta"`
}

// draftIDPattern bounds draft ids to the generated shape so a caller
// supplied id can never traverse outside .drafts/.
var draftIDPattern = regexp.MustCompile(`^[a-z0-9.-]+$`)

func (s *Store) draftDir(id string) string {
	return filepath.Join(s.root, draftsDir, id)
}

// CreateDraft stores a new draft tree; nothing becomes active.
func (s *Store) CreateDraft(name string, tree Tree, meta DraftMeta) (*Draft, error) {
	if s.scope != ScopePlatform && ReservedName(name) {
		return nil, fmt.Errorf("%w: %q", ErrReserved, name)
	}
	if err := validateTree(tree); err != nil {
		return nil, err
	}
	if n := s.draftsToday(); n >= MaxDraftsPerDay {
		return nil, fmt.Errorf("%w: %d drafts today (max %d)", ErrQuota, n, MaxDraftsPerDay)
	}
	report := scanTree(tree)
	meta.ScanVerdict = report.Verdict()
	meta.ScanFindings = boundFindings(report)
	d := &Draft{
		ID:   strings.ToLower(stampNow()) + "-" + randSuffix(),
		Name: name, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Meta: meta,
	}
	dir := s.draftDir(d.ID)
	if err := writeTree(dir, tree); err != nil {
		return nil, err
	}
	if execs := execPaths(tree); len(execs) > 0 {
		if err := writeJSONFile(filepath.Join(dir, ".exec"), mustJSON(execs)); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	if err := writeJSONFile(filepath.Join(dir, ".draft.json"), mustJSON(d)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	s.audit("draft_created", name)
	return d, nil
}

// DraftSidecar writes a hub-private file beside a draft's tree. The only
// admitted name is .origin.json — git-install provenance that must survive
// draft→publish without ever entering the agent-visible tree.
func (s *Store) DraftSidecar(id, name string, data []byte) error {
	if name != ".origin.json" || !draftIDPattern.MatchString(id) || len(data) > 16384 {
		return fmt.Errorf("%w: draft sidecar", ErrInvalid)
	}
	if _, err := os.Stat(s.draftDir(id)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.draftDir(id), name), data, 0o600)
}

// draftsToday counts drafts created today (draft ids embed the UTC stamp).
func (s *Store) draftsToday() int {
	entries, err := os.ReadDir(filepath.Join(s.root, draftsDir))
	if err != nil {
		return 0
	}
	today := strings.ToLower(stampNow())[:8] // yyyymmdd
	n := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), today) {
			n++
		}
	}
	return n
}

// Drafts lists pending drafts.
func (s *Store) Drafts() ([]*Draft, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, draftsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []*Draft{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, _, err := s.loadDraft(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Store) loadDraft(id string) (*Draft, Tree, error) {
	if !draftIDPattern.MatchString(id) {
		return nil, nil, fmt.Errorf("%w: bad draft id %q", ErrInvalid, id)
	}
	dir := s.draftDir(id)
	var d Draft
	body, err := os.ReadFile(filepath.Join(dir, ".draft.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if err := jsonUnmarshal(body, &d); err != nil {
		return nil, nil, err
	}
	tree, err := loadTree(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range readExecList(filepath.Join(dir, ".exec")) {
		if f, ok := tree[p]; ok {
			f.Exec = true
			tree[p] = f
		}
	}
	return &d, tree, nil
}

// LoadDraft returns the draft metadata and its tree.
func (s *Store) LoadDraft(id string) (*Draft, Tree, error) { return s.loadDraft(id) }

// PublishDraft applies a draft through the publish path and consumes it.
// The draft survives a failed publish so the owner can fix or reject it.
// A named approver is required — an empty approver is "auto-publish",
// which drafts never allow (R4.4). Critical scan findings refuse even
// with an approver (publish scans again).
func (s *Store) PublishDraft(id, approver string) (string, error) {
	if approver == "" {
		return "", fmt.Errorf("%w: approver required", ErrNeedsReview)
	}
	d, tree, err := s.loadDraft(id)
	if err != nil {
		return "", err
	}
	rev := ""
	switch d.Meta.Action {
	case "archive":
		err = s.Archive(d.Name, d.Meta.Reason, d.Meta.AbsorbedInto, approver)
	case "restore":
		err = s.RestoreAt(d.Name, d.Meta.RestoreStamp)
	default:
		rev, err = s.publish(d.Name, tree, IndexMeta{
			Author: approver, Source: "draft:" + d.Meta.Author,
			BaseRevision: d.Meta.BaseRevision, SessionID: d.Meta.SessionID,
		}, true)
	}
	if err != nil {
		return "", err
	}
	if sidecar, rerr := os.ReadFile(filepath.Join(s.draftDir(id), ".origin.json")); rerr == nil && d.Meta.Action == "" {
		_ = os.WriteFile(filepath.Join(s.dir(d.Name), ".origin.json"), sidecar, 0o600)
	}
	if err := os.RemoveAll(s.draftDir(id)); err != nil {
		return "", err
	}
	s.audit("draft_published", d.Name)
	return rev, nil
}

// RejectDraft removes a draft without touching the live skill.
func (s *Store) RejectDraft(id string) error {
	if !draftIDPattern.MatchString(id) {
		return fmt.Errorf("%w: bad draft id %q", ErrInvalid, id)
	}
	name := id
	if d, _, err := s.loadDraft(id); err == nil {
		name = d.Name
	}
	if err := os.RemoveAll(s.draftDir(id)); err != nil {
		return err
	}
	s.audit("draft_rejected", name)
	return nil
}
