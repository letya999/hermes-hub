package passport

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/passportview"
)

// RevisionKeepSlack bounds retained revision history: the live pointer chain
// plus this many newest superseded bundles survive each publication.
const RevisionKeepSlack = 3

// PruneRevisions sweeps generated/passports: revisions outside the live
// pointer chain and the slack window, plus orphaned staging/temp artifacts a
// crashed publish leaves behind. A publication in flight owns the tree — an
// apply journal or an unreadable/pending current pointer skips the sweep
// entirely. Deletion failures are collected, never fatal: pruning is
// hygiene, not a transaction.
func PruneRevisions(spaceDir, keepPrevious string) (removed []string, errs error) {
	root := filepath.Join(spaceDir, "generated", "passports")
	if _, err := os.Lstat(filepath.Join(root, passportview.ApplyJournalFile)); err == nil {
		return nil, nil
	}
	current, err := passportview.Current(spaceDir)
	if err != nil {
		return nil, nil
	}
	keep := map[string]bool{current: true}
	if keepPrevious != "" && keepPrevious != "pending" && filepath.IsLocal(keepPrevious) {
		keep[keepPrevious] = true
	}
	// The mounted config view pins the revision the runtime actually reads;
	// a divergent view must never lose its bundle.
	views, _ := filepath.Glob(filepath.Join(spaceDir, "generated", "config-view", "*", "revision"))
	for _, view := range views {
		body, err := os.ReadFile(view) // #nosec G304 -- globbed inside the operator-owned space
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(body)); filepath.IsLocal(id) && id != "pending" {
			keep[id] = true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	type historyEntry struct {
		name string
		at   time.Time
	}
	var history []historyEntry
	remove := func(name string) {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			errs = errors.Join(errs, err)
			return
		}
		removed = append(removed, name)
	}
	for _, e := range entries {
		name := e.Name()
		if !filepath.IsLocal(name) {
			continue
		}
		if strings.HasPrefix(name, ".staging-") || strings.HasPrefix(name, ".publish-") || strings.HasPrefix(name, ".apply-journal-") {
			remove(name)
			continue
		}
		if !e.IsDir() || !strings.HasPrefix(name, "r-") || keep[name] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		history = append(history, historyEntry{name, info.ModTime()})
	}
	slices.SortFunc(history, func(a, b historyEntry) int { return b.at.Compare(a.at) })
	for i, h := range history {
		if i < RevisionKeepSlack {
			continue
		}
		remove(h.name)
	}
	return removed, errs
}
