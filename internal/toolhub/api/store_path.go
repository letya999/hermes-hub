package api

import (
	"os"
	"path/filepath"
)

// ResolveStorePath returns the authoritative store file in a ToolHub state
// directory. SPEC-0090 R5.1: the SQLite substrate is authoritative once the
// verified store.db exists and the legacy store.json was retired; otherwise
// the JSON path is returned, existing or not.
func ResolveStorePath(dir string) string {
	jsonPath := filepath.Join(dir, "store.json")
	dbPath := filepath.Join(dir, "store.db")
	if info, err := os.Stat(dbPath); err == nil && info.Mode().IsRegular() { // #nosec G703 -- dir is the operator's ToolHub state root.
		if _, err := os.Stat(jsonPath); os.IsNotExist(err) { // #nosec G703 -- same state root.
			return dbPath
		}
	}
	return jsonPath
}
