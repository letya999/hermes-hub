package commops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// ValidateLegacySpool refuses the shared gateway's old automation substrate
// before the new per-owner clock can start. It is read-only: no record is
// moved or rewritten during the owner switch.
func ValidateLegacySpool(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("automation: legacy spool path required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("automation: legacy spool unavailable or unsafe")
	}
	bindings := LegacySpoolReader{Root: root}
	db, err := bindings.Database()
	if err != nil {
		return err
	}
	activated := db != nil
	if db != nil {
		_ = db.Close()
	}
	if info, err := os.Lstat(filepath.Join(root, "events")); err == nil {
		if info.IsDir() {
			entries, readErr := os.ReadDir(filepath.Join(root, "events"))
			if readErr != nil || len(entries) != 0 {
				return errors.New("automation: populated legacy event layout")
			}
		} else if !info.Mode().IsRegular() {
			return errors.New("automation: unsafe legacy event layout")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, name := range []string{"schedules", "occurrences", "assignments", "watchers", "digests", "event-state"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && !activated {
			continue
		}
		if !activated && (err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("automation: unsafe legacy automation records")
		}
		entries, err := bindings.Entries(path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("automation: populated shared legacy automation records")
		}
	}
	for _, name := range []string{"pending", "running"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && !activated {
			continue
		}
		if !activated && (err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("automation: unsafe legacy job directory")
		}
		entries, err := bindings.Entries(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry, ".json") {
				continue
			}
			jobPath := filepath.Join(path, entry)
			var job commodel.Job
			if bindings.ReadJSON(jobPath, &job) != nil {
				return errors.New("automation: unsafe legacy job record")
			}
			if job.Trigger == "cron" || job.AssignmentID != "" {
				return errors.New("automation: pending legacy automation job")
			}
		}
	}
	return nil
}
