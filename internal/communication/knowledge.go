package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

const knowledgeLimit = 9 << 20

type KnowledgeItem struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type KnowledgeFragment struct {
	Source     string    `json:"source"`
	Line       int       `json:"line"`
	Text       string    `json:"text"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modified_at"`
}

// knowledgeContext accepts the enrolled token's context, never an untrusted
// header that asks a sibling runtime to read another context's sources.
func (g *Gateway) knowledgeContext(r *http.Request, caller identity.Envelope) (string, bool) {
	contextID := caller.PrincipalID
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token != g.config.ControlAuth && !slices.Contains(g.config.controlAuthExtra, token) && g.config.ControlTokensFile != "" {
		body, err := os.ReadFile(g.config.ControlTokensFile)
		var entries map[string]identity.Envelope
		if err != nil || len(body) > 1<<20 || json.Unmarshal(body, &entries) != nil {
			return "", false
		}
		bound, ok := entries[token]
		if !ok || bound.PrincipalID != caller.PrincipalID || bound.Validate(bound.PrincipalID, bound.ContextID, bound.RuntimeID, bound.PolicyVersion) != nil {
			return "", false
		}
		contextID = bound.ContextID
	}
	requested := r.Header.Get("X-Hub-Context")
	return contextID, requested == "" || requested == contextID
}

func knowledgeRead(root *os.Root, name string) ([]byte, fs.FileInfo, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`+"\x00") || strings.HasPrefix(name, ".") {
		return nil, nil, fmt.Errorf("invalid source name")
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("source is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > knowledgeLimit {
		return nil, nil, fmt.Errorf("source exceeds read limit")
	}
	body, err := io.ReadAll(io.LimitReader(file, knowledgeLimit+1))
	if err != nil || len(body) > knowledgeLimit {
		return nil, nil, fmt.Errorf("source exceeds read limit")
	}
	return body, info, nil
}

func (g *Gateway) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contextID, ok := g.knowledgeContext(r, caller)
	if !ok {
		http.Error(w, "context not owned", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	spool, err := os.OpenRoot(g.spool.root)
	if err != nil {
		http.Error(w, "knowledge unavailable", http.StatusInternalServerError)
		return
	}
	defer spool.Close()
	dir := "knowledge/" + caller.PrincipalID
	if info, err := spool.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		http.Error(w, "invalid knowledge owner directory", http.StatusForbidden)
		return
	}
	root, err := spool.OpenRoot(dir)
	if err == nil && contextID != caller.PrincipalID {
		parent := root
		root, err = parent.OpenRoot(contextID)
		_ = parent.Close()
	}
	if os.IsNotExist(err) {
		if r.URL.Path == "/v1/knowledge" {
			writeJSON(w, []KnowledgeItem{})
			return
		}
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "knowledge unavailable", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	name := strings.TrimPrefix(r.URL.Path, "/v1/knowledge/")
	if name == r.URL.Path {
		name = ""
	}
	if name == "" {
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			http.Error(w, "knowledge unavailable", http.StatusInternalServerError)
			return
		}
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if len(query) > 256 {
			http.Error(w, "query too long", http.StatusBadRequest)
			return
		}
		items := []KnowledgeItem{}
		hits := []KnowledgeFragment{}
		scanned, scannedBytes := 0, 0
		for _, entry := range entries {
			if r.Context().Err() != nil {
				return
			}
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > knowledgeLimit {
				continue
			}
			items = append(items, KnowledgeItem{Name: entry.Name(), Size: info.Size(), ModTime: info.ModTime().UTC()})
			if query == "" || len(hits) >= 8 || scanned >= 128 || scannedBytes >= 32<<20 {
				continue
			}
			scanned++
			body, info, err := knowledgeRead(root, entry.Name())
			if err != nil {
				continue
			}
			scannedBytes += len(body)
			sum := sha256.Sum256(body)
			for line, text := range strings.Split(string(body), "\n") {
				if strings.Contains(strings.ToLower(text), query) {
					hits = append(hits, KnowledgeFragment{Source: entry.Name(), Line: line + 1, Text: boundedWatchText(text), SHA256: hex.EncodeToString(sum[:]), ModifiedAt: info.ModTime().UTC()})
					if len(hits) >= 8 {
						break
					}
				}
			}
		}
		if query != "" {
			writeJSON(w, map[string]any{"fragments": hits, "scanned": scanned, "scan_limit": 128, "context_id": contextID})
			return
		}
		writeJSON(w, items)
		return
	}
	body, info, err := knowledgeRead(root, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(body)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Hub-Name", name)
	w.Header().Set("X-Hub-Context", contextID)
	w.Header().Set("X-Hub-SHA256", hex.EncodeToString(sum[:]))
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	_, _ = w.Write(body)
}
