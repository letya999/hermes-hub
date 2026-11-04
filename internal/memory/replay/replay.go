// Package replay is the leaf-range replay lane of SPEC-0085 R11.5: the
// vault's prov.db audit spine streams into Provider.Capture with stable
// ids, so switching memory providers is a re-run of recorded captures —
// idempotent on retry and resumable after interruption via a cursor
// checkpoint the hub owns under runtime/memory/.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Replayer streams recorded entries into a provider. Scope minting is
// injected: the caller resolves each entry's audience to the opaque key
// minted from hub-held material — the replayer never sees raw identifiers.
type Replayer struct {
	p      memory.Provider
	mint   func(memory.Audience) memory.Scope
	cursor string
}

// Report is the resumable outcome.
type Report struct {
	Captured int    `json:"captured"`
	Skipped  int    `json:"skipped"`
	LastID   string `json:"last_id"`
	Done     bool   `json:"done"`
}

// New binds a replayer; cursorPath is the checkpoint file inside the
// vault's runtime sidecar zone.
func New(p memory.Provider, mint func(memory.Audience) memory.Scope, cursorPath string) *Replayer {
	return &Replayer{p: p, mint: mint, cursor: cursorPath}
}

// cursorState persists progress — a completed run records Done so a re-run
// over the same stream re-captures nothing.
type cursorState struct {
	LastID string `json:"last_id"`
	Done   bool   `json:"done"`
}

// Run streams entries in prov.db order. Each entry's scope is re-minted for
// its target audience before capture — the provider only ever sees opaque
// keys.
func (r *Replayer) Run(ctx context.Context, entries []memory.Entry) (Report, error) {
	var rep Report
	state, err := r.load()
	if err != nil {
		return rep, err
	}
	rep.LastID = state.LastID
	if state.Done {
		rep.Done = true
		rep.Skipped = len(entries)
		return rep, nil
	}
	resuming := state.LastID != ""
	for _, e := range entries {
		if resuming {
			if e.ID == state.LastID {
				resuming = false
			}
			continue // captured before the interruption
		}
		e.Scope = r.mint(e.Audience)
		if _, err := r.p.Capture(ctx, e); err != nil {
			return rep, fmt.Errorf("replay capture %s: %w", e.ID, err)
		}
		rep.Captured++
		rep.LastID = e.ID
		if err := r.save(cursorState{LastID: e.ID}); err != nil {
			return rep, err
		}
	}
	rep.Done = true
	if err := r.save(cursorState{LastID: rep.LastID, Done: true}); err != nil {
		return rep, err
	}
	return rep, nil
}

func (r *Replayer) load() (cursorState, error) {
	var s cursorState
	body, err := os.ReadFile(r.cursor)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return s, fmt.Errorf("replay cursor corrupt: %w", err)
	}
	return s, nil
}

func (r *Replayer) save(s cursorState) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := r.cursor + ".tmp"
	if err := os.WriteFile(tmp, body, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, r.cursor)
}
