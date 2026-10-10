package communication

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KnowledgeItem describes one read-only source staged for the calling user.
type KnowledgeItem struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// handleKnowledge serves the caller's own staged knowledge directory. Listing
// and reading only — writes are not part of the contract.
func (g *Gateway) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := filepath.Join(g.spool.root, "knowledge", caller.PrincipalID)
	name := strings.TrimPrefix(r.URL.Path, "/v1/knowledge/")
	if name == r.URL.Path {
		name = ""
	}
	if name == "" {
		entries, err := os.ReadDir(dir)
		items := []KnowledgeItem{}
		if err == nil {
			for _, e := range entries {
				info, err := e.Info()
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				items = append(items, KnowledgeItem{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime().UTC()})
			}
		}
		writeJSON(w, items)
		return
	}
	if strings.ContainsAny(name, `/\`) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Hub-Name", name)
	_, _ = w.Write(data)
}
