package skillstore

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Record is one skill's persisted publish metadata in .state/index.json.
type Record struct {
	Revision    string    `json:"revision"`
	Source      string    `json:"source"`
	Author      string    `json:"author"`
	SessionID   string    `json:"session_id,omitempty"`
	Origin      *Origin   `json:"origin,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

// Index is the persisted .state/index.json document.
type Index struct {
	Schema  int               `json:"schema"`
	Skills  map[string]Record `json:"skills"`
	Notices []Notice          `json:"notices,omitempty"`
}

func (s *Store) indexPath() string {
	return filepath.Join(s.root, stateDir, indexFile)
}

func (s *Store) readIndex() (*Index, error) {
	body, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return &Index{Schema: 1, Skills: map[string]Record{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := jsonUnmarshal(body, &idx); err != nil {
		return nil, err
	}
	if idx.Skills == nil {
		idx.Skills = map[string]Record{}
	}
	return &idx, nil
}

// Index returns the persisted store index.
func (s *Store) Index() (*Index, error) { return s.readIndex() }

// record persists the publish record for name.
func (s *Store) record(name, rev string, meta IndexMeta) error {
	idx, err := s.readIndex()
	if err != nil {
		return err
	}
	idx.Skills[name] = Record{
		Revision: rev, Source: meta.Source, Author: meta.Author,
		SessionID: meta.SessionID,
	}
	return writeJSONFile(s.indexPath(), append(mustJSON(idx), '\n'))
}

// Revisions lists retained revision ids for name.
func (s *Store) Revisions(name string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, revisionsDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	revs := []string{}
	for _, e := range entries {
		if e.IsDir() {
			revs = append(revs, e.Name())
		}
	}
	return revs, nil
}
