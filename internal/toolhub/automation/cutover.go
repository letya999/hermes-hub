package automation

import (
	"encoding/json"
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
	if _, err := os.Lstat(filepath.Join(root, "records.activated")); err == nil {
		return errors.New("automation: unsupported activated legacy spool")
	} else if !os.IsNotExist(err) {
		return err
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
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("automation: unsafe legacy automation records")
		}
		entries, err := os.ReadDir(path)
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
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("automation: unsafe legacy job directory")
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			jobPath := filepath.Join(path, entry.Name())
			info, err := os.Lstat(jobPath)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
				return errors.New("automation: unsafe legacy job record")
			}
			body, err := os.ReadFile(jobPath) // #nosec G703 -- bounded read-only legacy spool preflight
			var job commodel.Job
			if err != nil || json.Unmarshal(body, &job) != nil {
				return errors.New("automation: unsafe legacy job record")
			}
			if job.Trigger == "cron" || job.AssignmentID != "" {
				return errors.New("automation: pending legacy automation job")
			}
		}
	}
	return nil
}
