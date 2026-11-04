package agenttools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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

// readOnlyPathList splits the rendered HUB_WORKSPACE_RO boundary list into
// workspace-relative directory prefixes.
func readOnlyPathList(raw string) []string {
	return strings.Split(raw, ",")
}
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
	knowledge := r.Root == "knowledge"
	var root *os.Root
	var err error
	switch {
	case r.Root == "vault":
		if t.Vault == nil {
			return nil, fmt.Errorf("vault root is not configured")
		}
		root = t.Vault
	case knowledge:
		// root=knowledge resolves the staged knowledge directory for this
		// runtime's own principal (ADR-0094 S1); it stays read-only.
		root, err = t.knowledgeRoot()
		if err != nil {
			return nil, err
		}
		if root != nil {
			defer root.Close()
		}
	default:
		root, err = rootOf(r.Root, ws, ar, org, t.Skills)
		if err != nil {
			return nil, err
		}
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
		if root == nil {
			return map[string]any{"items": []map[string]any{}, "truncated": false}, nil
		}
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
		if root == nil {
			return map[string]any{"items": items, "scanned": scanned, "bounded": true}, nil
		}
		needle := strings.ToLower(r.Query)
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
			b, err := read(root, p)
			if err != nil {
				return nil
			}
			if !strings.Contains(strings.ToLower(string(b)), needle) {
				return nil
			}
			snippets := []map[string]any{}
			for line, text := range strings.Split(string(b), "\n") {
				if strings.Contains(strings.ToLower(text), needle) {
					snippets = append(snippets, map[string]any{"line": line + 1, "text": boundedKnowledgeText(text)})
					if len(snippets) == 8 {
						break
					}
				}
			}
			items = append(items, map[string]any{"path": p, "revision": hash(b), "bytes": len(b), "snippets": snippets})
			return nil
		})
		return map[string]any{"items": items, "scanned": scanned, "bounded": true}, err
	}
	if op == "read" {
		if root == nil {
			return nil, fmt.Errorf("knowledge unavailable")
		}
		b, err := read(root, r.Path)
		if err != nil {
			return nil, err
		}
		if r.Offset > uint64(len(b)) {
			return nil, fmt.Errorf("offset beyond end of file")
		}
		chunk := b[r.Offset:]
		limit := len(chunk)
		if r.Limit > 0 && r.Limit < limit {
			limit = r.Limit
		}
		res := map[string]any{"path": r.Path, "text": string(chunk[:limit]), "revision": hash(b)}
		if r.Offset+uint64(limit) < uint64(len(b)) {
			res["next_offset"] = float64(r.Offset + uint64(limit))
		}
		return res, nil
	}
	if op != "write" && op != "edit" && op != "mkdir" && op != "remove" {
		return nil, fmt.Errorf("unknown file operation")
	}
	if knowledge || r.Root == "archive" || r.Root == "organization" || r.Root == "skills" {
		return nil, fmt.Errorf("%s is read-only", r.Root)
	}
	boundary, berr := t.boundary()
	if berr != nil {
		return nil, berr // published passport undecodable or gone: fail closed
	}
	if boundary != nil {
		prefix := "/" + r.Root
		if r.Root == "" {
			prefix = "/workspace"
		}
		resolved := resolvePathLinks(root, r.Path)
		if err := boundary.denyWrite(path.Join(prefix, r.Path)); err != nil {
			return nil, err
		}
		if err := boundary.denyWrite(path.Join(prefix, resolved)); err != nil {
			return nil, err
		}
		// Project creation is a distinct right: minting a new top-level
		// directory under projects/ goes through project_create
		// (workspace.create_project); writes land only in projects that
		// already exist.
		if prefix == "/workspace" {
			parts := strings.Split(r.Path, "/")
			if parts[0] == "projects" && len(parts) > 1 {
				if _, statErr := root.Lstat(path.Join("projects", parts[1])); os.IsNotExist(statErr) {
					return nil, fmt.Errorf("%s does not exist: create it with project_create (workspace.create_project)", parts[1])
				}
			}
		}
	} else if err := t.guardWritable(root, r.Path); err != nil {
		return nil, err
	}
	if op == "mkdir" {
		if r.Parents {
			err = root.MkdirAll(r.Path, 0700)
		} else {
			err = root.Mkdir(r.Path, 0700)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": r.Path}, nil
	}
	if op == "remove" {
		if err := root.Remove(r.Path); err != nil {
			return nil, err
		}
		return map[string]any{"path": r.Path, "removed": true}, nil
	}
	var content []byte
	if op == "edit" {
		if r.OldString == "" {
			return nil, fmt.Errorf("old_string required")
		}
		b, err := read(root, r.Path)
		if err != nil {
			return nil, err
		}
		if r.Revision == "" || r.Revision != hash(b) {
			return nil, fmt.Errorf("revision conflict")
		}
		text := string(b)
		if !strings.Contains(text, r.OldString) {
			return nil, fmt.Errorf("old_string not found")
		}
		content = []byte(strings.Replace(text, r.OldString, r.NewString, 1))
		if len(content) > Limit || !utf8.Valid(content) {
			return nil, fmt.Errorf("text must be UTF-8 and <=2 MiB")
		}
	} else {
		if r.exportBytes == nil && (len(r.Text) > Limit || !utf8.ValidString(r.Text)) {
			return nil, fmt.Errorf("text must be UTF-8 and <=2 MiB")
		}
		content = r.exportBytes
		if content == nil {
			content = []byte(r.Text)
		}
		b, err := read(root, r.Path)
		if err == nil {
			if r.Revision == "" || r.Revision != hash(b) {
				return nil, fmt.Errorf("revision conflict")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		} else if r.Revision != "" {
			return nil, fmt.Errorf("revision conflict: file missing")
		}
	}
	if err = commitFile(root, r.Path, content); err != nil {
		return nil, err
	}
	return map[string]any{"path": r.Path, "revision": hash(content)}, nil
}

// commitFile writes content to rel inside root atomically: a tmp file in the
// same directory is synced, closed, then renamed over the target.
func commitFile(root *os.Root, rel string, content []byte) error {
	if err := root.MkdirAll(path.Dir(rel), 0700); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(rel), ".hub-"+hash([]byte(time.Now().String()))[:24])
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}

// guardWritable rejects writes that land inside a declared read-only
// boundary, resolved through in-root symlinks so a link cannot launder the
// write across the boundary.
func (t *Tools) guardWritable(root *os.Root, rel string) error {
	resolved := resolvePathLinks(root, rel)
	for _, ro := range t.ReadOnlyPaths {
		if ro == "" {
			continue
		}
		if rel == ro || strings.HasPrefix(rel, ro+"/") || resolved == ro || strings.HasPrefix(resolved, ro+"/") {
			return fmt.Errorf("%s is read-only", rel)
		}
	}
	return nil
}

// resolvePathLinks returns rel with a symlinked ancestor replaced by its
// in-root target. Read-only boundaries must hold for the path the write
// actually lands on, not just the literal string the caller sent.
func resolvePathLinks(root *os.Root, rel string) string {
	prefix := ""
	for _, dir := range strings.Split(rel, "/") {
		p := path.Join(prefix, dir)
		info, err := root.Lstat(p)
		if err != nil {
			return rel
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := root.Readlink(p)
			target = strings.ReplaceAll(target, "\\", "/")
			if err != nil || path.IsAbs(target) {
				return rel
			}
			return path.Join(path.Dir(p), target, strings.TrimPrefix(rel, p))
		}
		prefix = p
	}
	return rel
}

// projectCreate mints one project directory under projects/ — the only way
// a new top-level project appears (workspace.create_project). It respects
// the same live boundary as file writes.
func (t *Tools) projectCreate(ws *os.Root, r Input) (map[string]any, error) {
	name := strings.TrimSpace(r.Name)
	if !fs.ValidPath(name) || strings.ContainsAny(name, "/\\") || name == "." {
		return nil, fmt.Errorf("project name must be a single clean path segment")
	}
	boundary, berr := t.boundary()
	if berr != nil {
		return nil, berr // published passport undecodable or gone: fail closed
	}
	target := path.Join("projects", name)
	if boundary != nil {
		resolved := resolvePathLinks(ws, target)
		if err := boundary.denyWrite(path.Join("/workspace", target)); err != nil {
			return nil, err
		}
		if err := boundary.denyWrite(path.Join("/workspace", resolved)); err != nil {
			return nil, err
		}
	}
	info, err := ws.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%s exists and is not a project directory", target)
		}
		return map[string]any{"path": target, "created": false}, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := ws.Mkdir(target, 0700); err != nil {
		// A workspace that predates the projects/ skeleton has no parent;
		// minting it is part of the same creation contract.
		if !os.IsNotExist(err) {
			return nil, err
		}
		if perr := ws.Mkdir("projects", 0700); perr != nil && !os.IsExist(perr) {
			return nil, perr
		}
		if err = ws.Mkdir(target, 0700); err != nil {
			return nil, err
		}
	}
	return map[string]any{"path": target, "created": true}, nil
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
