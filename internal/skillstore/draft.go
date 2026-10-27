package skillstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DraftMeta is the provenance envelope every draft carries (R4.4):
// author kind, session, reason, base revision, sticky taint and scan
// verdict. Values come from the hub caller — never from skill text.
type DraftMeta struct {
	Author       string   `json:"author"` // user|agent|review|import|git|operator
	SessionID    string   `json:"session_id,omitempty"`
	Reason       string   `json:"reason"`
	BaseRevision string   `json:"base_revision,omitempty"`
	Tainted      bool     `json:"tainted,omitempty"`
	TaintSources []string `json:"taint_sources,omitempty"` // web|inbound|connector|git
	ScanVerdict  string   `json:"scan_verdict,omitempty"`
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
	d := &Draft{
		ID:   strings.ToLower(stampNow()) + "-" + randSuffix(),
		Name: name, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Meta: meta,
	}
	dir := s.draftDir(d.ID)
	if err := writeTree(dir, tree); err != nil {
		return nil, err
	}
	if err := writeJSONFile(filepath.Join(dir, ".draft.json"), mustJSON(d)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return d, nil
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
	return &d, tree, nil
}

// LoadDraft returns the draft metadata and its tree.
func (s *Store) LoadDraft(id string) (*Draft, Tree, error) { return s.loadDraft(id) }

// PublishDraft applies a draft through the publish path and consumes it.
// The draft survives a failed publish so the owner can fix or reject it.
func (s *Store) PublishDraft(id, approver string) (string, error) {
	d, tree, err := s.loadDraft(id)
	if err != nil {
		return "", err
	}
	rev, err := s.Publish(d.Name, tree, IndexMeta{
		Author: approver, Source: "draft:" + d.Meta.Author,
		BaseRevision: d.Meta.BaseRevision, SessionID: d.Meta.SessionID,
	})
	if err != nil {
		return "", err
	}
	return rev, os.RemoveAll(s.draftDir(id))
}

// RejectDraft removes a draft without touching the live skill.
func (s *Store) RejectDraft(id string) error {
	if !draftIDPattern.MatchString(id) {
		return fmt.Errorf("%w: bad draft id %q", ErrInvalid, id)
	}
	return os.RemoveAll(s.draftDir(id))
}
