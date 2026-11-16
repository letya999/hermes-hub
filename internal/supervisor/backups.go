package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/contextlife"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Scheduled backups (issue #200): a backup nobody ran is not a backup, and
// a restore nobody rehearsed is a gamble. Once per sweep the supervisor
// checks each space's backup cadence, captures a contextlife archive into
// <space>/archive/backups/, prunes to a bounded retention, and on a slower
// cadence restores the newest archive into scratch space to prove the
// bytes actually come back. Results persist in <StateDir>/backups.json so
// the last-success timestamp is auditable and surfaces on /v1/status —
// never on the public path.
//
// Errors record a bounded class word, not the error text: operator detail
// stays in logs, the status surface stays legible.

type BackupRecord struct {
	User          string    `json:"user"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"` // bounded class word
	LastArchive   string    `json:"last_archive,omitempty"`
	Retained      int       `json:"retained,omitempty"`
	LastDrillAt   time.Time `json:"last_drill_at,omitempty"`
	DrillResult   string    `json:"drill_result,omitempty"` // ok | verify_failed | restore_failed | hash_mismatch
	DrillChecked  int       `json:"drill_checked,omitempty"`
}

const backupsStateFile = "backups.json"

// backupDurationEnv honors "0"/"off" as an explicit disable — the plain
// durationEnv helper can only return positive values or the fallback.
func backupDurationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if raw == "0" || strings.EqualFold(raw, "off") {
		return -1
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return fallback
}

func backupIntEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return fallback
}

func (m *Manager) backupsPath() string {
	return filepath.Join(m.cfg.StateDir, backupsStateFile)
}

func (m *Manager) loadBackups() {
	m.backups = map[string]BackupRecord{}
	body, err := os.ReadFile(m.backupsPath())
	if err != nil {
		return
	}
	var records []BackupRecord
	if json.Unmarshal(body, &records) != nil {
		return
	}
	for _, r := range records {
		if identity.ValidID(r.User) {
			m.backups[r.User] = r
		}
	}
}

func (m *Manager) persistBackupsLocked() error {
	records := make([]BackupRecord, 0, len(m.backups))
	for _, r := range m.backups {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].User < records[j].User })
	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.cfg.StateDir, ".backups-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, m.backupsPath())
	}
	return err
}

func backupErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, os.ErrNotExist):
		return "missing_source"
	case errors.Is(err, os.ErrPermission):
		return "unwritable_target"
	default:
		return "backup_failed"
	}
}

func drillErrorClass(err error) string {
	if err == nil {
		return "ok"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "hash mismatch"):
		return "hash_mismatch"
	case strings.Contains(msg, "manifest") || strings.Contains(msg, "user mismatch"):
		return "verify_failed"
	default:
		return "restore_failed"
	}
}

// spaceBackupDir is the archive target inside the space itself: under the
// backup's own include list it never recurses (archive/ is not captured).
func spaceBackupDir(spaceDir string) string {
	return filepath.Join(spaceDir, "archive", "backups")
}

// backupSweep runs once per watchdog sweep. Capture work happens outside
// m.mu — archiving a space can take seconds and must not stall control.
func (m *Manager) backupSweep(ctx context.Context, now time.Time) {
	if m.cfg.BackupEvery <= 0 {
		return
	}
	entries, err := os.ReadDir(m.cfg.SpacesRoot)
	if err != nil {
		return
	}
	var users []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || !identity.ValidID(name) {
			continue
		}
		users = append(users, name)
	}
	for _, user := range users {
		if ctx.Err() != nil {
			return
		}
		m.backupSpace(user, now)
	}
}

func (m *Manager) backupSpace(user string, now time.Time) {
	m.mu.Lock()
	rec, seen := m.backups[user]
	m.mu.Unlock()

	due := !seen || now.Sub(rec.LastSuccessAt) >= m.cfg.BackupEvery || rec.LastError != ""
	if due {
		spaceDir := filepath.Join(m.cfg.SpacesRoot, user)
		outDir := spaceBackupDir(spaceDir)
		stamp := now.UTC().Format("20060102T150405Z")
		archive := filepath.Join(outDir, "backup-"+stamp+".zip")
		rec.User = user
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			rec.LastError = backupErrorClass(err)
		} else if err := contextlife.Backup(spaceDir, user, archive, ""); err != nil {
			rec.LastError = backupErrorClass(err)
			_ = os.Remove(archive)
		} else {
			rec.LastSuccessAt, rec.LastError, rec.LastArchive = now.UTC(), "", filepath.Base(archive)
		}
		rec.Retained = m.pruneBackups(outDir)
	}
	if m.cfg.BackupDrillEvery > 0 && (rec.LastDrillAt.IsZero() || now.Sub(rec.LastDrillAt) >= m.cfg.BackupDrillEvery) {
		m.drillSpace(user, &rec, now)
	}
	m.mu.Lock()
	if m.backups == nil {
		m.backups = map[string]BackupRecord{}
	}
	m.backups[user] = rec
	_ = m.persistBackupsLocked()
	m.mu.Unlock()
}

// pruneBackups keeps the newest cfg.BackupKeep archives; names sort
// chronologically, so the tail is the live set. Returns the retained
// count.
func (m *Manager) pruneBackups(dir string) int {
	keep := m.cfg.BackupKeep
	if keep <= 0 {
		keep = 7
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "backup-") && strings.HasSuffix(e.Name(), ".zip") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return len(names)
}

// drillSpace restores the newest archive into a scratch dir under
// StateDir and verifies restored bytes against the manifest. The drill
// result is a bounded class word — auditable without leaking content.
func (m *Manager) drillSpace(user string, rec *BackupRecord, now time.Time) {
	dir := spaceBackupDir(filepath.Join(m.cfg.SpacesRoot, user))
	entries, err := os.ReadDir(dir)
	rec.LastDrillAt = now.UTC()
	if err != nil {
		rec.DrillResult, rec.DrillChecked = "no_archive", 0
		return
	}
	var newest string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "backup-") && strings.HasSuffix(e.Name(), ".zip") && e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		rec.DrillResult, rec.DrillChecked = "no_archive", 0
		return
	}
	scratch := filepath.Join(m.cfg.StateDir, ".drill-"+user)
	_ = os.RemoveAll(scratch)
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		rec.DrillResult, rec.DrillChecked = "restore_failed", 0
		return
	}
	defer os.RemoveAll(scratch)
	report, err := contextlife.Drill(user, filepath.Join(dir, newest), scratch)
	if err != nil {
		rec.DrillResult, rec.DrillChecked = drillErrorClass(err), report.Checked
		return
	}
	rec.DrillResult, rec.DrillChecked = "ok", report.Checked
}
