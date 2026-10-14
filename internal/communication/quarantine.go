package communication

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// quarantineCap bounds stored poison updates; the oldest are evicted beyond it.
const quarantineCap = 1000

// Quarantine records an update whose handling failed so the polling loop can
// move past it. The record carries identity and failure class only — never
// message text, captions or callback payloads, which can hold secrets.
func (s *Spool) Quarantine(update Update, cause error, now time.Time) error {
	dir := filepath.Join(s.root, "quarantine")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	record := struct {
		UpdateID   int       `json:"update_id"`
		ErrorClass string    `json:"error_class"`
		At         time.Time `json:"at"`
	}{UpdateID: update.UpdateID, ErrorClass: quarantineClass(cause), At: now.UTC()}
	if err := atomicJSON(filepath.Join(dir, strconv.Itoa(update.UpdateID)+".json"), record); err != nil {
		return err
	}
	// A poison storm must not fill the disk: evict the oldest beyond the cap.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= quarantineCap {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		ai, _ := entries[i].Info()
		aj, _ := entries[j].Info()
		return ai.ModTime().Before(aj.ModTime())
	})
	for _, entry := range entries[:len(entries)-quarantineCap] {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func quarantineClass(err error) string {
	if errors.Is(err, ErrUncertain) {
		return "uncertain"
	}
	return "handle_update_error"
}
