package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// toolhubDoctorWarnings reports managed capability profiles whose selections
// outrun their capability rules — the digest-split wedge publish leaves when
// it repins ahead of catalog enable. Each warning names the profile and the
// repair command; a missing store means an unmanaged space, not a failure.
func toolhubDoctorWarnings(dir string, lang locale.Lang) []string {
	root := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub")
	storePath := filepath.Join(root, "store.json")
	if _, err := os.Stat(filepath.Join(root, "store.db")); err == nil {
		if _, err := os.Stat(storePath); errors.Is(err, os.ErrNotExist) {
			storePath = filepath.Join(root, "store.db")
		}
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		return nil
	}
	defer func() { _ = store.CloseDB() }()
	gaps, err := store.SelectionGaps()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(gaps))
	for profileID := range gaps {
		ids = append(ids, profileID)
	}
	sort.Strings(ids)
	warnings := make([]string, 0, len(ids))
	for _, profileID := range ids {
		warnings = append(warnings, locale.T(lang, "doctor.capability-selection-gap", profileID, len(gaps[profileID])))
	}
	return warnings
}
