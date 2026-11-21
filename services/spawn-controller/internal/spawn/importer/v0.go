// v0 file readers for the one-time importer. Each type mirrors the v0
// writer's persisted shape — supervisor.go persistLocked, backups.go
// BackupRecord, rollout.go RolloutPins, status.go statusTransition and
// incidents.go's signal line — and nothing more: the readers never open a
// file the writers did not publish and never write anything back.
package importer

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// v0Manager is the runtime part of supervisor.json the importer reads:
// the persisted generation counter and one row per v0 runtime. Pins,
// holds, jobs, leases and orphan records stay in the file — the report
// counts what the service does not own instead of reading them.
type v0Manager struct {
	Generation uint64   `json:"generation"`
	Items      []v0Item `json:"items"`
	Jobs       int      `json:"-"`
	Holds      int      `json:"-"`
	Pins       int      `json:"-"`
	Orphans    int      `json:"-"`
	LeaseSeq   uint64   `json:"lease_seq"`
}

// UnmarshalJSON counts the unsupported sections without decoding their
// payload — a pin or hold may embed request fields the report never reads.
func (m *v0Manager) UnmarshalJSON(b []byte) error {
	var raw struct {
		Generation uint64                     `json:"generation"`
		LeaseSeq   uint64                     `json:"lease_seq"`
		Items      []v0Item                   `json:"items"`
		Jobs       map[string]json.RawMessage `json:"jobs"`
		Holds      map[string]json.RawMessage `json:"holds"`
		Pins       map[string]json.RawMessage `json:"pins"`
		Orphans    []json.RawMessage          `json:"orphans"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Generation, m.LeaseSeq, m.Items = raw.Generation, raw.LeaseSeq, raw.Items
	m.Jobs, m.Holds, m.Pins, m.Orphans = len(raw.Jobs), len(raw.Holds), len(raw.Pins), len(raw.Orphans)
	return nil
}

type v0Item struct {
	Runtime v0Runtime `json:"runtime"`
}

// v0Runtime carries the (principal, context, generation) triple the v1
// generation baseline needs; everything else in the row is the v0
// container's own lifecycle and is dropped by design.
type v0Runtime struct {
	PrincipalID string `json:"principal_id"`
	ContextID   string `json:"context_id"`
	RuntimeMode string `json:"runtime_mode"`
	Generation  string `json:"generation"`
}

// generationNumber parses the v0 "gen-N" counter suffix; anything else is
// unparseable and counted, never guessed.
func generationNumber(gen string) (int64, bool) {
	suffix, ok := strings.CutPrefix(gen, "gen-")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// v0Backup is backups.go's persisted record (backups.json is a sorted
// []BackupRecord); times stay as decoded unix seconds in the v1 row.
type v0Backup struct {
	User          string    `json:"user"`
	LastSuccessAt time.Time `json:"last_success_at"`
	LastError     string    `json:"last_error"`
	LastArchive   string    `json:"last_archive"`
	Retained      int       `json:"retained"`
	LastDrillAt   time.Time `json:"last_drill_at"`
	DrillResult   string    `json:"drill_result"`
	DrillChecked  int       `json:"drill_checked"`
}

// v0RolloutPins is rollout.go's per-env pin file (image-pins.json); the
// canary steers one context, current/previous are the verified images.
type v0RolloutPins struct {
	Envs map[string]struct {
		Current struct {
			Image    string    `json:"image"`
			SmokedAt time.Time `json:"smoked_at"`
		} `json:"current"`
		Previous *struct {
			Image    string    `json:"image"`
			SmokedAt time.Time `json:"smoked_at"`
		} `json:"previous"`
		Canary *struct {
			Image     string    `json:"image"`
			ContextID string    `json:"context_id"`
			SetAt     time.Time `json:"set_at"`
		} `json:"canary"`
	} `json:"envs"`
}

// countLines returns the number of nonempty lines in a .jsonl journal;
// the lines' content is never kept — the report records only the count.
func countLines(path string) (int, error) {
	f, err := os.Open(path) // #nosec G304 -- importer reads the operator-pointed v0 dir
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	scanner := bufio.NewScanner(io.LimitReader(f, 64<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			n++
		}
	}
	return n, scanner.Err()
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path) // #nosec G304 -- importer reads the operator-pointed v0 dir
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func readManager(v0Dir string) (v0Manager, error) {
	var m v0Manager
	err := readJSON(filepath.Join(v0Dir, "supervisor.json"), &m)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	return m, err
}

func readBackups(v0Dir string) ([]v0Backup, error) {
	var out []v0Backup
	err := readJSON(filepath.Join(v0Dir, "backups.json"), &out)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return out, err
}
