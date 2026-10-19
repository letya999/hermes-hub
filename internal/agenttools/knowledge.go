package agenttools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
)

const knowledgeLimit = 9 << 20

type knowledgeItem struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type knowledgeFragment struct {
	Source     string    `json:"source"`
	Line       int       `json:"line"`
	Text       string    `json:"text"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modified_at"`
}

// knowledgeRoot resolves the staged knowledge directory inside this
// runtime's state volume: knowledge/<principal>, narrowed to the
// context subdir when the operator staged one. Foreign principal
// directories are unreachable — the path derives from the runtime's own
// identity, never a parameter.
func (t *Tools) knowledgeRoot() (*os.Root, error) {
	if t.StateDir == "" {
		return nil, fmt.Errorf("knowledge unavailable")
	}
	envelope := runtimeEnvelope()
	base, err := os.OpenRoot(t.StateDir)
	if err != nil {
		return nil, fmt.Errorf("knowledge unavailable")
	}
	defer base.Close()
	dir := "knowledge/" + envelope.PrincipalID
	if info, err := base.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("invalid knowledge owner directory")
	}
	root, err := base.OpenRoot(dir)
	if err == nil && envelope.ContextID != envelope.PrincipalID {
		parent := root
		root, err = parent.OpenRoot(envelope.ContextID)
		_ = parent.Close()
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge unavailable")
	}
	return root, nil
}

func readKnowledgeSource(root *os.Root, name string) ([]byte, fs.FileInfo, error) {
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

// knowledgeList returns the caller's staged read-only knowledge sources.
func (t *Tools) knowledgeList(_ context.Context) (map[string]any, error) {
	root, err := t.knowledgeRoot()
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{"items": []any{}}, nil
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("knowledge unavailable")
	}
	items := []any{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > knowledgeLimit {
			continue
		}
		items = append(items, knowledgeItem{Name: entry.Name(), Size: info.Size(), ModTime: info.ModTime().UTC()})
	}
	return map[string]any{"items": items}, nil
}

// knowledgeRead returns one staged source as tool output — the content never
// lands in the workspace.
func (t *Tools) knowledgeRead(_ context.Context, r Input) (map[string]any, error) {
	root, err := t.knowledgeRoot()
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("knowledge unavailable")
	}
	defer root.Close()
	body, info, err := readKnowledgeSource(root, r.Name)
	if err != nil {
		return nil, fmt.Errorf("knowledge source unavailable")
	}
	sum := sha256.Sum256(body)
	return map[string]any{"name": r.Name, "content": string(body), "sha256": hex.EncodeToString(sum[:]), "modified_at": info.ModTime().UTC().Format(time.RFC3339), "context_id": runtimeEnvelope().ContextID}, nil
}

func boundedKnowledgeText(value string) string {
	runes := []rune(value)
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return value
}

func (t *Tools) knowledgeSearch(_ context.Context, query string) (map[string]any, error) {
	if strings.TrimSpace(query) == "" || len(query) > 256 {
		return nil, fmt.Errorf("a query of at most 256 bytes is required")
	}
	root, err := t.knowledgeRoot()
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{"fragments": []knowledgeFragment{}, "scanned": 0, "scan_limit": 128, "context_id": runtimeEnvelope().ContextID}, nil
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("knowledge unavailable")
	}
	query = strings.ToLower(strings.TrimSpace(query))
	hits := []knowledgeFragment{}
	scanned := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > knowledgeLimit {
			continue
		}
		if len(hits) >= 8 || scanned >= 128 {
			continue
		}
		scanned++
		body, info, err := readKnowledgeSource(root, entry.Name())
		if err != nil {
			continue
		}
		sum := sha256.Sum256(body)
		for line, text := range strings.Split(string(body), "\n") {
			if strings.Contains(strings.ToLower(text), query) {
				hits = append(hits, knowledgeFragment{Source: entry.Name(), Line: line + 1, Text: boundedKnowledgeText(text), SHA256: hex.EncodeToString(sum[:]), ModifiedAt: info.ModTime().UTC()})
				if len(hits) >= 8 {
					break
				}
			}
		}
	}
	return map[string]any{"fragments": hits, "scanned": scanned, "scan_limit": 128, "context_id": runtimeEnvelope().ContextID}, nil
}
