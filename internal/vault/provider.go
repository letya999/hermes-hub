package vault

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Provider is the vault behind the memory seam (ADR-0086 D15): it serves
// capture, governed writes and export; recall and search arrive in M20.3/M20.4
// and honestly report ErrUnsupported until then.
type Provider struct {
	root string
}

// NewProvider binds the memory seam to the vault at root.
func NewProvider(root string) *Provider { return &Provider{root: root} }

func (p *Provider) Caps(context.Context) memory.Caps {
	return memory.Caps{Capture: true, Write: true, Export: true}
}

// entriesFile is one JSONL journal per opaque scope under runtime/memory/ —
// the hub-owned sidecar zone (.okignore'd, never user-edited). Scope
// isolation is literal: entries for different scopes live in different files.
func (p *Provider) entriesFile(scope memory.Scope) string {
	return filepath.Join(p.root, "runtime", "memory", "entries", scope.Key+".jsonl")
}

// Capture appends the entry to its scope journal, deduplicating by ID so a
// retried capture stores one copy. The hub-stamped fields — scope, kind,
// provenance, audience — are persisted verbatim; the provider never rewrites
// them.
func (p *Provider) Capture(_ context.Context, entry memory.Entry) (string, error) {
	path := p.entriesFile(entry.Scope)
	existing, err := readEntries(path)
	if err != nil {
		return "", err
	}
	for _, e := range existing {
		if e.ID == entry.ID {
			return entry.ID, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		return "", err
	}
	return entry.ID, nil
}

func readEntries(path string) ([]memory.Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []memory.Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e memory.Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("vault: corrupt entry journal %s: %w", path, err)
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

func (p *Provider) Recall(context.Context, memory.Scope) (memory.Block, error) {
	return memory.Block{}, memory.ErrUnsupported
}

func (p *Provider) Search(context.Context, memory.Scope, memory.Query) ([]memory.Hit, error) {
	return nil, memory.ErrUnsupported
}

// Write delegates to the vault's governed writer — the only mutation path.
func (p *Provider) Write(_ context.Context, op memory.WriteOp) (memory.WriteResult, error) {
	return NewWriter(p.root).Apply(op)
}

// Export returns the scope's journal entries — the partition file plus a
// scope re-check, so nothing from a neighbouring scope can leak across.
func (p *Provider) Export(_ context.Context, scope memory.Scope) ([]memory.Entry, error) {
	entries, err := readEntries(p.entriesFile(scope))
	if err != nil {
		return nil, err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Scope == scope {
			out = append(out, e)
		}
	}
	return out, nil
}

func (p *Provider) Health(context.Context) error { return nil }
