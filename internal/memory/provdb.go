package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProvDB is the hub-owned provenance sidecar of SPEC-0085 R2.2: a newline-
// delimited JSON journal under runtime/memory/prov.db (inside the vault root,
// excluded from external indexing by .okignore). Entries are append-only,
// each fsync'd before the caller may acknowledge the write — provenance is
// stamped here by the hub, never by a provider or by content inside a body.
type ProvDB struct {
	path string
	seq  map[string]int  // date prefix -> entries minted for that date
	seen map[string]bool // ids already recorded — capture is idempotent
}

// OpenProvDB opens (creating if needed) the sidecar inside dir, creating the
// directory when absent — callers pass the vault's runtime/memory directory.
// The per-date sequence is recovered from the file so a restart continues
// where the previous process left off.
func OpenProvDB(dir string) (*ProvDB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := &ProvDB{path: filepath.Join(dir, "prov.db"), seq: map[string]int{}, seen: map[string]bool{}}
	entries, err := p.Entries()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		p.seen[e.ID] = true
		if d, n := entrySeq(e.ID); d != "" && n > p.seq[d] {
			p.seq[d] = n
		}
	}
	return p, nil
}

// entrySeq splits a stable id "<date>-<seq>" into its parts; ids not in that
// shape return an empty date.
func entrySeq(id string) (string, int) {
	if len(id) < len("2006-01-02")+2 || id[len("2006-01-02")] != '-' {
		return "", 0
	}
	d := id[:len("2006-01-02")]
	if strings.Count(d, "-") != 2 {
		return "", 0
	}
	var n int
	if _, err := fmt.Sscanf(id[len(d)+1:], "%d", &n); err != nil {
		return "", 0
	}
	return d, n
}

// Append assigns a stable <date>-<seq> id when the entry does not already
// carry one — replay preserves ids, so a migration re-appending recorded
// entries yields the identical set — then writes and fsyncs the record before
// returning: a successful Append means the record survives a kill.
func (p *ProvDB) Append(e *Entry) error {
	if e.ID != "" && p.seen[e.ID] {
		return nil // already recorded — a capture retry must not double-write
	}
	if e.ID == "" {
		date := e.ObservedAt.Format("2006-01-02")
		p.seq[date]++
		e.ID = fmt.Sprintf("%s-%04d", date, p.seq[date])
	} else if d, n := entrySeq(e.ID); d != "" && n > p.seq[d] {
		p.seq[d] = n
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	p.seen[e.ID] = true
	return f.Close()
}

// Entries replays the sidecar in append order — the journal is the audit
// spine that lets the same capture stream re-materialize inside another
// provider unchanged.
func (p *ProvDB) Entries() ([]Entry, error) {
	f, err := os.Open(p.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("prov.db corrupt: %w", err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
