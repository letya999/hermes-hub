package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// CompactOccurrences moves only owner-confirmed settled records out of the
// hot scan. The callback runs under the engine lock and must not reenter it.
func (e *Engine) CompactOccurrences(now time.Time, retention time.Duration, receipt func(commodel.RoutineOccurrence) (any, bool, error)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	dir := filepath.Join(e.root, "occurrences")
	names, err := e.readDir(dir)
	if err != nil {
		return err
	}
	archive, receipts := filepath.Join(dir, "archive"), filepath.Join(dir, "receipts")
	ensureDir := func(path string) error {
		if err := e.mkdirOwned(path, true); err != nil {
			return err
		}
		info, err := e.lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe occurrence history directory")
		}
		return nil
	}
	if err := ensureDir(archive); err != nil {
		return err
	}
	if err := ensureDir(receipts); err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
			return errors.New("unsafe occurrence during compaction")
		}
		body, err := e.readFile(path)
		if err != nil {
			return err
		}
		var o commodel.RoutineOccurrence
		if json.Unmarshal(body, &o) != nil || name != occurrenceID(o)+".json" || o.Job.ID != occurrenceID(o) {
			continue
		}
		switch o.State {
		case "no_change", "missed", "blocked", "cancelled", "capped", "skipped", "failed", "dispatched":
		default:
			continue
		}
		summary, ok, err := receipt(o)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := e.atomicJSON(filepath.Join(receipts, name), summary); err != nil {
			return err
		}
		if err := e.renameOwned(path, filepath.Join(archive, name)); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		dir string
		age time.Duration
	}{{archive, 48 * time.Hour}, {receipts, retention}} {
		entries, err := e.readDir(item.dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, name := range entries {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			path := filepath.Join(item.dir, name)
			info, err := e.lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("unsafe occurrence history during compaction")
			}
			if info.ModTime().Before(now.Add(-item.age)) {
				if err := e.removeOwned(path); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range names {
		for _, suffix := range []string{".probe-result", ".digest-result", ".script-output"} {
			if !strings.HasSuffix(name, suffix) {
				continue
			}
			path := filepath.Join(dir, name)
			info, err := e.lstat(path)
			if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(now.Add(-retention)) {
				break
			}
			base := strings.TrimSuffix(name, suffix) + ".json"
			if _, err := e.lstat(filepath.Join(dir, base)); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return err
			}
			if _, err := e.lstat(filepath.Join(dir, "quarantine", base)); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := e.removeOwned(path); err != nil {
				return err
			}
			break
		}
	}
	return nil
}
