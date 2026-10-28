package agenttools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func parseActions(raw string) map[string]bool {
	actions := map[string]bool{}
	for _, action := range strings.Split(raw, ",") {
		if action = strings.TrimSpace(action); action != "" {
			actions[action] = true
		}
	}
	return actions
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func safe(p string) bool {
	return fs.ValidPath(p) && !strings.Contains(p, "\\") && !strings.ContainsRune(p, 0)
}
func (t *Tools) root(name string) (*os.Root, error) {
	return rootOf(name, t.Workspace, t.Archive, t.Organization, t.Skills)
}
func rootOf(name string, ws, ar, org, sk *os.Root) (*os.Root, error) {
	switch name {
	case "", "workspace":
		return ws, nil
	case "archive":
		if ar == nil {
			return nil, fmt.Errorf("archive root is not configured")
		}
		return ar, nil
	case "organization":
		if org == nil {
			return nil, fmt.Errorf("organization root is not configured")
		}
		return org, nil
	case "skills":
		if sk == nil {
			return nil, fmt.Errorf("skills root is not configured")
		}
		return sk, nil
	}
	return nil, fmt.Errorf("root must be workspace, archive, organization or skills")
}
func (t *Tools) File(op string, r Input) (map[string]any, error) {
	return t.fileScoped(op, r, t.Workspace, t.Archive, t.Organization)
}

// fileScoped is File with call-scoped roots: the ToolHub executor passes the
// capability-admitted boundaries, the direct MCP surface passes t's roots.
func (t *Tools) fileScoped(op string, r Input, ws, ar, org *os.Root) (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lock != nil {
		if err := t.lock.Lock(); err != nil {
			return nil, err
		}
		defer t.lock.Unlock()
	}
	root, err := rootOf(r.Root, ws, ar, org, t.Skills)
	if err != nil {
		return nil, err
	}
	if r.Path == "" {
		r.Path = "."
	}
	if !safe(r.Path) {
		return nil, fmt.Errorf("relative path required")
	}
	if path.Base(r.Path) == ".hub-writer.lock" {
		return nil, fmt.Errorf("reserved lock file")
	}
	if op == "list" {
		entries, err := fs.ReadDir(root.FS(), r.Path)
		if err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			items = append(items, map[string]any{"name": e.Name(), "directory": e.IsDir()})
			if len(items) == 200 {
				break
			}
		}
		return map[string]any{"items": items, "truncated": len(entries) > 200}, nil
	}
	if op == "search" {
		if strings.TrimSpace(r.Query) == "" {
			return nil, fmt.Errorf("query required")
		}
		items := []map[string]any{}
		scanned := 0
		err := fs.WalkDir(root.FS(), r.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			scanned++
			if scanned > 5000 || len(items) >= 50 {
				return fs.SkipAll
			}
			if !slices.Contains([]string{".md", ".txt", ".json", ".html", ".csv", ".eml", ".mbox"}, strings.ToLower(path.Ext(p))) {
				return nil
			}
			b, err := read(root, p)
			if err != nil {
				return nil
			}
			if strings.Contains(strings.ToLower(string(b)), strings.ToLower(r.Query)) {
				items = append(items, map[string]any{"path": p, "revision": hash(b), "bytes": len(b)})
			}
			return nil
		})
		return map[string]any{"items": items, "scanned": scanned, "bounded": true}, err
	}
	b, err := read(root, r.Path)
	if op == "read" {
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": r.Path, "text": string(b), "revision": hash(b)}, nil
	}
	if op != "write" {
		return nil, fmt.Errorf("unknown file operation")
	}
	if r.Root == "archive" || r.Root == "organization" || r.Root == "skills" {
		return nil, fmt.Errorf("%s is read-only", r.Root)
	}
	if r.exportBytes == nil && (len(r.Text) > Limit || !utf8.ValidString(r.Text)) {
		return nil, fmt.Errorf("text must be UTF-8 and <=2 MiB")
	}
	content := r.exportBytes
	if content == nil {
		content = []byte(r.Text)
	}
	if err == nil {
		if r.Revision == "" || r.Revision != hash(b) {
			return nil, fmt.Errorf("revision conflict")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else if r.Revision != "" {
		return nil, fmt.Errorf("revision conflict: file missing")
	}
	if err = root.MkdirAll(path.Dir(r.Path), 0700); err != nil {
		return nil, err
	}
	tmp := path.Join(path.Dir(r.Path), ".hub-"+hash([]byte(time.Now().String()))[:24])
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = root.Rename(tmp, r.Path); err != nil {
		return nil, err
	}
	return map[string]any{"path": r.Path, "revision": hash(content)}, nil
}
func read(root *os.Root, p string) ([]byte, error) {
	info, err := root.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("regular file required")
	}
	f, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, Limit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > Limit {
		return nil, fmt.Errorf("file exceeds 2 MiB; split large archives before reading")
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("binary file: use browser/download tools")
	}
	return b, nil
}
