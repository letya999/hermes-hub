package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/stack"
)

// The journal contains identifiers and affected service names, never passport
// bodies, grants or credentials. Its presence means an apply has not succeeded.
type applyJournal struct {
	Environment     string             `json:"environment"`
	Previous        string             `json:"previous"`
	Revision        string             `json:"revision"`
	PreviousProfile string             `json:"previous_profile"`
	NextProfile     string             `json:"next_profile"`
	Phase           string             `json:"phase"`
	Actions         []reconcile.Action `json:"actions"`
}

func applyJournalPath(dir string) string {
	return filepath.Join(dir, "generated", "passports", passportview.ApplyJournalFile)
}

func writeApplyJournal(dir string, record applyJournal) error {
	path := applyJournalPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".apply-journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readApplyJournal(dir string) (applyJournal, error) {
	var record applyJournal
	file, err := os.Open(applyJournalPath(dir))
	if err != nil {
		return record, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 64<<10 {
		return record, errors.New("invalid apply journal size")
	}
	dec := json.NewDecoder(io.LimitReader(file, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return record, err
	}
	if dec.Decode(new(any)) != io.EOF || (record.Environment != "dev" && record.Environment != "prod") ||
		!filepath.IsLocal(record.Revision) || record.Revision == "pending" ||
		(record.Previous != "" && (!filepath.IsLocal(record.Previous) || record.Previous == "pending")) ||
		!identity.ValidID(record.NextProfile) ||
		(record.Previous != "" && !identity.ValidID(record.PreviousProfile)) ||
		!slices.Contains([]string{"preparing", "prepared", "rendered", "rights", "activated", "binding", "effects"}, record.Phase) ||
		len(record.Actions) > 256 {
		return applyJournal{}, errors.New("invalid apply journal")
	}
	for _, action := range record.Actions {
		if !slices.Contains([]reconcile.Kind{reconcile.Rebuild, reconcile.Recreate, reconcile.Remove, reconcile.Respawn, reconcile.Live}, action.Kind) ||
			len(action.Target) == 0 || len(action.Target) > 128 || strings.ContainsAny(action.Target, "/\\\x00\r\n") {
			return applyJournal{}, errors.New("invalid apply journal action")
		}
	}
	return record, nil
}

func removeApplyJournal(dir string) error { return os.Remove(applyJournalPath(dir)) }

// The old immutable documents are read again during recovery. A concurrently
// revoked issued grant is intentionally not captured in the journal.
func previousApplyAuthority(dir, revisionID string) (string, map[string][]string, []byte, error) {
	if revisionID == "" {
		return "", map[string][]string{}, nil, nil
	}
	root := filepath.Join(dir, "generated", "passports", revisionID)
	grants, err := os.ReadFile(filepath.Join(root, "grants.yaml"))
	if err != nil {
		return "", nil, nil, err
	}
	tools, err := os.ReadFile(filepath.Join(root, "tools.yaml"))
	if err != nil {
		return "", nil, nil, err
	}
	allow, err := (passport.Revision{Docs: map[string][]byte{"grants": grants}}).GrantAllow()
	if err != nil {
		return "", nil, nil, err
	}
	bound, err := (passport.Revision{Docs: map[string][]byte{"tools": tools}}).Value("tools", "binding.profile_id")
	if err != nil {
		return "", nil, nil, err
	}
	profile, _ := bound.(string)
	if profile == "" {
		return "", nil, nil, errors.New("previous binding unavailable")
	}
	return profile, allow, tools, nil
}

func restoreApplyState(dir, root string, record applyJournal) error {
	if err := passport.RestoreCurrent(dir, record.Previous, record.Environment); err != nil {
		return err
	}
	if record.Previous != "" {
		if err := stack.RenderEnvironment(dir, root, record.Environment); err != nil {
			return err
		}
	}
	docs, err := catalog.LoadPinned()
	if err != nil {
		return err
	}
	oldProfile, allow, toolsBody, err := previousApplyAuthority(dir, record.Previous)
	if err != nil {
		return err
	}
	if oldProfile != record.PreviousProfile {
		return errors.New("previous binding changed during recovery")
	}
	now, user := time.Now().UTC(), filepath.Base(dir)
	if oldProfile != "" && oldProfile != record.NextProfile {
		if err := migration.SetSpaceProfileActive(dir, user, record.Environment, oldProfile, true, now); err != nil {
			return err
		}
	}
	if oldProfile != "" {
		if err := migration.RestoreSpaceRights(dir, user, record.Environment, oldProfile, record.Previous, allow, toolsBody, docs, now); err != nil {
			return err
		}
	}
	if record.NextProfile != oldProfile {
		if err := migration.RestoreSpaceRights(dir, user, record.Environment, record.NextProfile, "", map[string][]string{}, nil, docs, now); err != nil {
			return err
		}
		if err := migration.SetSpaceProfileActive(dir, user, record.Environment, record.NextProfile, false, now); err != nil {
			return err
		}
	}
	return nil
}

// recoverApply runs under the same per-space lock as apply. Readers refuse a
// pending pointer; a failed replay keeps the journal and restores that refusal.
func recoverApply(_ context.Context, dir, root, environment string, handlers reconcile.Handlers) error {
	record, err := readApplyJournal(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply recovery: %w", err)
	}
	if record.Environment != environment {
		return errors.New("apply recovery: different environment is pending")
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("apply recovery: current pointer: %w", err)
	}
	current := strings.TrimSpace(string(body))
	if current != "pending" && current != record.Previous && current != record.Revision {
		return errors.New("apply recovery: current pointer diverged")
	}
	if err := restoreApplyState(dir, root, record); err != nil {
		_ = passport.PointCurrent(dir, "pending")
		return fmt.Errorf("apply recovery: restore: %w", err)
	}
	if record.Phase == "effects" {
		if record.Previous == "" {
			_ = passport.PointCurrent(dir, "pending")
			return errors.New("apply recovery: first apply service effects need intervention")
		}
		for _, action := range record.Actions {
			if action.Kind == reconcile.Rebuild {
				_ = passport.PointCurrent(dir, "pending")
				return errors.New("apply recovery: interrupted image build needs intervention")
			}
		}
		if err := reconcile.Replay(record.Actions, handlers); err != nil {
			_ = passport.PointCurrent(dir, "pending")
			return fmt.Errorf("apply recovery: service replay: %w", err)
		}
	}
	if err := removeApplyJournal(dir); err != nil {
		_ = passport.PointCurrent(dir, "pending")
		return fmt.Errorf("apply recovery: journal removal: %w", err)
	}
	return nil
}
