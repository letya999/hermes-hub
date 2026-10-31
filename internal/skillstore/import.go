package skillstore

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Foreign packages arrive as directories, zip archives or standalone
// SKILL.md files (R10.9). All of them land in the same draft pipeline:
// bounded extraction, dot paths dropped with a report (dot-space is
// hub-reserved and repo plumbing like .github/ is never skill content),
// skill roots discovered by SKILL.md — the caller selects, never guesses.

// MaxImportBytes bounds the compressed archive a caller may hand over;
// the extracted tree is bounded again by the store quotas at draft
// creation.
const MaxImportBytes = 32 << 20

// ImportZip extracts a zip archive into the package tree. Unsafe members
// (traversal, absolute paths, links, quota escapes, duplicates) refuse
// the whole archive — a package is installed whole or not at all.
func ImportZip(data []byte) (tree Tree, dropped []string, err error) {
	if len(data) > MaxImportBytes {
		return nil, nil, fmt.Errorf("%w: archive exceeds %d bytes", ErrQuota, MaxImportBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: not a readable zip", ErrInvalid)
	}
	tree = Tree{}
	for _, f := range zr.File {
		name, err := cleanMemberName(f.Name)
		if err != nil {
			return nil, nil, err
		}
		if name == "" {
			continue // directory entries carry no content
		}
		info := f.FileInfo()
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("%w: archive member %q is not a regular file", ErrInvalid, name)
		}
		if _, exists := tree[name]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate archive member %q", ErrInvalid, name)
		}
		if dotSegmented(name) {
			dropped = append(dropped, name)
			continue
		}
		if info.Size() > MaxFileBytes {
			return nil, nil, fmt.Errorf("%w: %s is %d bytes (max %d)", ErrQuota, name, info.Size(), MaxFileBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		body, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(body) > MaxFileBytes {
			return nil, nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrQuota, name, MaxFileBytes)
		}
		tree[name] = File{Data: body, Exec: info.Mode()&0o111 != 0}
	}
	return tree, dropped, nil
}

// ImportTree walks an fs.FS (an os.DirFS path or a sandboxed os.Root)
// into a Tree, dropping dot paths — the same shape publish reads. The
// caller passes the directory that holds the package.
func ImportTree(fsys fs.FS) (tree Tree, dropped []string, err error) {
	tree = Tree{}
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				dropped = append(dropped, p+"/")
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			dropped = append(dropped, p)
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s is not allowed in a skill", ErrInvalid, p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrInvalid, p)
		}
		if info.Size() > MaxFileBytes {
			return fmt.Errorf("%w: %s is %d bytes (max %d)", ErrQuota, p, info.Size(), MaxFileBytes)
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		tree[p] = File{Data: body, Exec: info.Mode()&0o111 != 0}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return tree, dropped, nil
}

// FileTree wraps a standalone SKILL.md body — a single-file import needs
// no container.
func FileTree(body []byte) Tree {
	return Tree{SkillFile: File{Data: body}}
}

// SkillRoots lists every directory containing SKILL.md ("" = package
// root) — the candidate list a caller reports or selects from.
func SkillRoots(tree Tree) []string {
	roots := []string{}
	for p := range tree {
		if p == SkillFile {
			roots = append(roots, "")
		} else if strings.HasSuffix(p, "/"+SkillFile) {
			roots = append(roots, strings.TrimSuffix(p, "/"+SkillFile))
		}
	}
	sort.Strings(roots)
	return roots
}

// SelectTree returns the subtree under prefix ("" = the whole package),
// root-relative again — the subtree becomes the skill directory.
func SelectTree(tree Tree, prefix string) Tree {
	out := Tree{}
	for p, f := range tree {
		if prefix == "" {
			out[p] = f
			continue
		}
		if rel, ok := strings.CutPrefix(p, strings.TrimSuffix(prefix, "/")+"/"); ok && rel != "" {
			out[rel] = f
		}
	}
	return out
}

// DropNotes summarizes the filtered dot paths for a draft's import audit
// — hub-reserved names and repo plumbing never reach the skill tree.
func DropNotes(dropped []string) []string {
	if len(dropped) == 0 {
		return nil
	}
	// A repo can carry thousands of vendored files — the audit note shows
	// examples, then a "+N more" tail instead of enumerating every path.
	const show = 8
	list := strings.Join(dropped[:min(show, len(dropped))], ", ")
	if len(dropped) > show {
		list += fmt.Sprintf(" … +%d more", len(dropped)-show)
	}
	return []string{fmt.Sprintf("%d paths dropped on import (hub-reserved, vendored): %s", len(dropped), list)}
}

// NameFromTree resolves a skill name from the SKILL.md frontmatter —
// authoritative because publish requires name == dir — falling back to
// the package's own name when the frontmatter is absent or unreadable.
func NameFromTree(tree Tree, fallback string) string {
	if m, _, err := ParseSkillMD("", tree[SkillFile].Data, true); err == nil && m.Name != "" {
		return m.Name
	}
	return fallback
}

// cleanMemberName normalizes an archive member name to a portable
// relative path and refuses traversal and absolute paths — "" marks a
// non-file entry.
func cleanMemberName(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || path.IsAbs(n) {
		return "", fmt.Errorf("%w: archive path %q escapes the root", ErrInvalid, name)
	}
	parts := []string{}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: archive path %q escapes the root", ErrInvalid, name)
		}
		if seg != "" && seg != "." {
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "/"), nil
}

// dotSegmented reports whether a path contains a dot-prefixed segment.
func dotSegmented(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
