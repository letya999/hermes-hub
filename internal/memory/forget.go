package memory

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ForgetSet is the hub-owned soft-forget sidecar of SPEC-0085 R6.3/D16:
// runtime/memory/forget.jsonl records struck entry ids as append-only JSONL.
// A struck entry is permanently excluded from search, lane-1 recall and
// promotion; it stays visible only via history:true and git history.
type ForgetSet struct {
	path string
}

// OpenForgetSet binds the sidecar inside dir (the vault's runtime/memory).
func OpenForgetSet(dir string) *ForgetSet {
	return &ForgetSet{path: filepath.Join(dir, "forget.jsonl")}
}

// Strike records one struck entry id.
func (s *ForgetSet) Strike(id string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	row, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(row, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Set replays the sidecar into the struck-id set — the exclusion filter the
// index, recall and promotion lanes all share.
func (s *ForgetSet) Set() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open(s.path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(line), &r) == nil && r.ID != "" {
			out[r.ID] = true
		}
	}
	return out
}
