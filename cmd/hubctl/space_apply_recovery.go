package main

import (
	"bytes"
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

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/reconcile"
)

// Its presence means an apply has not succeeded. Settings edits also retain
// the bounded authored config before/after so crash recovery can restore it.
type applyJournal struct {
	Environment          string             `json:"environment"`
	Previous             string             `json:"previous"`
	Revision             string             `json:"revision"`
	PreviousPolicyDigest string             `json:"previous_policy_digest,omitempty"`
	PolicyDigest         string             `json:"policy_digest,omitempty"`
	PreviousProfile      string             `json:"previous_profile"`
	NextProfile          string             `json:"next_profile"`
	Phase                string             `json:"phase"`
	Actions              []reconcile.Action `json:"actions"`
	AuthoredConfigBefore []byte             `json:"authored_config_before,omitempty"`
	AuthoredConfigAfter  []byte             `json:"authored_config_after,omitempty"`
}

type authoredConfigUpdate struct{ Before, After []byte }

const maxApplyJournalBytes = 3 << 20

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
	if len(body) > maxApplyJournalBytes {
		return errors.New("apply journal exceeds size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".apply-journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	// The supervisor runs under the publisher's shared group and must read
	// this journal while the current passport pointer is fenced as pending.
	if err := file.Chmod(0640); err != nil {
		_ = file.Close()
		return err
	}
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
	if err != nil || info.Size() > maxApplyJournalBytes {
		return record, errors.New("invalid apply journal size")
	}
	dec := json.NewDecoder(io.LimitReader(file, maxApplyJournalBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return record, err
	}
	if dec.Decode(new(any)) != io.EOF || (record.Environment != "dev" && record.Environment != "prod") ||
		!filepath.IsLocal(record.Revision) || record.Revision == "pending" ||
		(record.Previous != "" && (!filepath.IsLocal(record.Previous) || record.Previous == "pending")) ||
		(record.PreviousPolicyDigest != "" && (!contract.ValidPolicyDigest(record.PreviousPolicyDigest) || record.PolicyDigest == "")) ||
		(record.PolicyDigest != "" && !contract.ValidPolicyDigest(record.PolicyDigest)) ||
		!identity.ValidID(record.NextProfile) ||
		(record.Previous != "" && !identity.ValidID(record.PreviousProfile)) ||
		!slices.Contains([]string{"preparing", "prepared", "rendered", "rights", "activated", "binding", "effects"}, record.Phase) ||
		len(record.Actions) > 256 ||
		(len(record.AuthoredConfigBefore) == 0) != (len(record.AuthoredConfigAfter) == 0) ||
		len(record.AuthoredConfigBefore) > 1<<20 || len(record.AuthoredConfigAfter) > 1<<20 {
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
	grants, err := os.ReadFile(filepath.Join(root, "grants.yaml")) // #nosec G703 -- fixed file under a validated immutable revision ID
	if err != nil {
		return "", nil, nil, err
	}
	tools, err := os.ReadFile(filepath.Join(root, "tools.yaml")) // #nosec G703 -- fixed file under a validated immutable revision ID
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

func restoreApplyState(ctx context.Context, dir, root string, record applyJournal) error {
	if err := restoreApplyPolicy(ctx, dir, record); err != nil {
		return err
	}
	if len(record.AuthoredConfigBefore) != 0 {
		if err := writeAuthoredConfig(dir, record.AuthoredConfigAfter, record.AuthoredConfigBefore); err != nil {
			return err
		}
	}
	if err := passport.RestoreCurrent(dir, record.Previous, record.Environment); err != nil {
		return err
	}
	if record.Previous != "" {
		if err := renderEnvironment(dir, root, record.Environment); err != nil {
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
	// Deactivate the candidate only once the journal proves apply actually
	// activated it. Validation failures before that journal point leave the
	// bound profile untouched so the next apply can derive against it instead
	// of stranding on `bound profile unavailable`.
	nextWentLive := slices.Contains([]string{"activated", "binding", "effects"}, record.Phase)
	if record.NextProfile != oldProfile && nextWentLive {
		if err := migration.RestoreSpaceRights(dir, user, record.Environment, record.NextProfile, "", map[string][]string{}, nil, docs, now); err != nil {
			return err
		}
		if err := migration.SetSpaceProfileActive(dir, user, record.Environment, record.NextProfile, false, now); err != nil {
			return err
		}
	}
	return refreshSharedRuntimeTokens(dir, record.Environment)
}

func writeAuthoredConfig(dir string, expected, next []byte) error {
	path := filepath.Join(dir, "passports", "config.yaml")
	info, err := os.Lstat(path) // #nosec G703 -- fixed authored config path in an operator-owned space
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("authored config is unavailable or not a regular file")
	}
	current, err := os.ReadFile(path) // #nosec G304 G703 -- fixed authored passport path
	if err != nil {
		return err
	}
	if bytes.Equal(current, next) {
		return nil
	}
	if !bytes.Equal(current, expected) {
		return errors.New("authored config diverged during apply")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-publish-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(next); err != nil {
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
	return os.Rename(file.Name(), path) // #nosec G703 -- temp and destination are in the same validated passport directory
}

// recoverApply runs under the same per-space lock as apply. Readers refuse a
// pending pointer; a failed replay keeps the journal and restores that refusal.
func recoverApply(ctx context.Context, dir, root, environment string, handlers reconcile.Handlers) error {
	return recoverApplyWithRebuiltImage(ctx, dir, root, environment, handlers, false)
}

// recoverApplyWithRebuiltImage requires an explicit operator assertion that
// the selected image tag was verified after an interrupted rebuild.
func recoverApplyWithRebuiltImage(ctx context.Context, dir, root, environment string, handlers reconcile.Handlers, rebuiltImageVerified bool) error {
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
	body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current")) // #nosec G703 -- fixed pointer filename under the operator's space dir.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("apply recovery: current pointer: %w", err)
	}
	current := strings.TrimSpace(string(body))
	previous := record.Previous
	if record.PreviousPolicyDigest != "" {
		previous = record.PreviousPolicyDigest
	}
	next := record.Revision
	if record.PolicyDigest != "" {
		next = record.PolicyDigest
	}
	if current != "pending" && current != previous && current != next {
		return errors.New("apply recovery: current pointer diverged")
	}
	if err := retryApplyRestore(func() error { return restoreApplyState(context.WithoutCancel(ctx), dir, root, record) }); err != nil {
		_ = passport.PointCurrent(dir, "pending")
		return fmt.Errorf("apply recovery: restore: %w", err)
	}
	if record.Phase == "effects" {
		if record.Previous == "" {
			_ = passport.PointCurrent(dir, "pending")
			return errors.New("apply recovery: first apply service effects need intervention")
		}
		for _, action := range record.Actions {
			if action.Kind == reconcile.Rebuild && !rebuiltImageVerified {
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

func retryApplyRestore(restore func() error) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := restore()
		if err == nil || !retryableStoreConflict(err) || attempt == 2 {
			return err
		}
	}
	return nil
}
