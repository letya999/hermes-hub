package gitsource

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// SourceInfo is one tracked mirror under skills/.sources/.
type SourceInfo struct {
	ID     string   `json:"id"`
	URL    string   `json:"url"`
	Skills []string `json:"skills"` // installed skills tracking this mirror
}

// Sources enumerates the owner store's git mirrors (R10.7): every mirror
// under .sources/ cross-referenced with the skills' origin records.
func Sources(st *skillstore.Store) ([]SourceInfo, error) {
	var out []SourceInfo
	entries, err := os.ReadDir(filepath.Join(st.Root(), ".sources")) // #nosec G703 -- store root is hub-owned
	if err != nil {
		return out, nil
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".git")
		if !e.IsDir() || !ok {
			continue
		}
		info := SourceInfo{ID: id}
		names, err := st.Names()
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if origin, err := ReadOrigin(st, name); err == nil && origin.SourceID == id {
				info.URL = origin.URL
				info.Skills = append(info.Skills, name)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

var sourceIDPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// RemoveSource deletes the mirror and returns whether one existed; skills
// keep their revision but lose update tracking (R10.7).
func RemoveSource(st *skillstore.Store, id string) (bool, error) {
	if !sourceIDPattern.MatchString(id) {
		return false, ErrInvalid
	}
	mirror := filepath.Join(st.Root(), ".sources", id+".git")
	if _, err := os.Stat(mirror); err != nil {
		return false, nil
	}
	return true, os.RemoveAll(mirror)
}
