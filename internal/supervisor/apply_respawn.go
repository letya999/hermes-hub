package supervisor

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/passportview"
)

// An operator apply must finish its runtime effect while the journal blocks
// ToolHub's ordinary rights decision. The shared bearer is still required by
// serveHTTP; this narrow exception is bound to the on-disk apply transaction.
func (m *Manager) applyRespawnAuthorized(r *http.Request, principal, contextID, runtimeID string) bool {
	revision := r.Header.Get("X-Hub-Apply-Revision")
	if revision == "" || principal != contextID || runtimeID != "" {
		return false
	}
	space := filepath.Join(m.cfg.SpacesRoot, principal)
	journalPath := filepath.Join(space, "generated", "passports", passportview.ApplyJournalFile)
	body, err := os.ReadFile(journalPath) // #nosec G304 -- validated principal under configured spaces root
	if err != nil || len(body) > 3<<20 {
		return false
	}
	var journal struct {
		Environment string `json:"environment"`
		Previous    string `json:"previous"`
		Revision    string `json:"revision"`
		Phase       string `json:"phase"`
		Actions     []struct {
			Kind   string `json:"kind"`
			Target string `json:"target"`
		} `json:"actions"`
	}
	if json.Unmarshal(body, &journal) != nil || journal.Phase != "effects" || journal.Environment != m.environment() || journal.Revision != revision || len(journal.Actions) > 256 {
		return false
	}
	respawn := false
	for _, action := range journal.Actions {
		respawn = respawn || ((action.Kind == "respawn" || action.Kind == "recreate") && action.Target == "hermes-runtime")
	}
	if !respawn {
		return false
	}
	for _, revision := range []string{journal.Previous, journal.Revision} {
		if revision == "" {
			continue
		}
		effective, err := passportview.LoadRevision(space, revision)
		if err == nil && effective.Header.Metadata.Space == principal && effective.Header.Metadata.Environment == journal.Environment {
			return true
		}
	}
	return false
}
