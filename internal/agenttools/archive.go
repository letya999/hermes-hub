package agenttools

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// ponytail: fixed ceilings keep zip bombs bounded; raise here if workspaces outgrow them.
var (
	maxUnpackEntries = 8192
	maxUnpackBytes   = int64(1 << 30)
)

func (t *Tools) archiveCreate(ws *os.Root, r Input) (map[string]any, error) {
	if !safe(r.Path) || !strings.HasPrefix(r.Path, "artifacts/") || !strings.HasSuffix(strings.ToLower(r.Path), ".zip") {
		return nil, errors.New("output must be an artifacts/*.zip path")
	}
	if len(r.Paths) == 0 {
		return nil, errors.New("paths must list workspace files")
	}
	seen := map[string]bool{}
	for _, p := range r.Paths {
		if !safe(p) || seen[p] {
			return nil, errors.New("archive sources must be distinct relative paths")
		}
		info, err := ws.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("archive sources must be regular files")
		}
		seen[p] = true
	}
	if err := ws.MkdirAll(path.Dir(r.Path), 0700); err != nil {
		return nil, err
	}
	f, err := ws.OpenFile(r.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(f)
	for _, rel := range r.Paths {
		body, err := ws.Open(rel)
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			return nil, err
		}
		w, err := zw.Create(rel)
		if err == nil {
			_, err = io.Copy(w, body)
		}
		_ = body.Close()
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return map[string]any{"path": r.Path, "files": len(r.Paths)}, nil
}

func (t *Tools) archiveUnpack(ws *os.Root, r Input) (map[string]any, error) {
	if !safe(r.Path) {
		return nil, errors.New("archive path must be a safe relative path")
	}
	if r.Name == "" || !safe(r.Name) || strings.Contains(r.Name, "/") {
		return nil, errors.New("name must be a single safe path segment")
	}
	target := "artifacts/unpacked/" + r.Name
	if _, err := ws.Lstat(target); err == nil {
		return nil, errors.New("unpack target already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := ws.Open(r.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return nil, errors.New("not a readable zip archive")
	}
	files, err := unpackZip(ws, zr, target)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": target, "files": files}, nil
}

// unpackZip extracts a validated zip into target under ws. Entry count and
// decompressed bytes are bounded (declared size is checked, actual bytes are
// capped by the reader), unsafe names, encrypted, non-file and symlink entries
// are refused, and the partial target is removed on any failure.
func unpackZip(ws *os.Root, zr *zip.Reader, target string) (int, error) {
	if len(zr.File) > maxUnpackEntries {
		return 0, errors.New("archive has too many entries")
	}
	var declared int64
	for _, entry := range zr.File {
		declared += int64(entry.UncompressedSize64)
	}
	if declared > maxUnpackBytes {
		return 0, errors.New("archive exceeds the unpacked size limit")
	}
	files := 0
	fail := func(err error) (int, error) {
		_ = ws.RemoveAll(target)
		return 0, err
	}
	for _, entry := range zr.File {
		name, err := cleanZipName(entry.Name)
		if err != nil {
			return fail(err)
		}
		if entry.Flags&0x1 != 0 {
			return fail(errors.New("encrypted zip entries are not supported"))
		}
		mode := entry.FileInfo().Mode()
		if !mode.IsDir() && !mode.IsRegular() {
			return fail(errors.New("only regular files and directories may be unpacked"))
		}
		dest := path.Join(target, name)
		if mode.IsDir() {
			if err := ws.MkdirAll(dest, 0700); err != nil {
				return fail(err)
			}
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fail(err)
		}
		if err := ws.MkdirAll(path.Dir(dest), 0700); err != nil {
			_ = rc.Close()
			return fail(err)
		}
		out, err := ws.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = io.Copy(out, rc)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			files++
		}
		_ = rc.Close()
		if err != nil {
			return fail(err)
		}
	}
	return files, nil
}

// cleanZipName rejects entry names that could write outside the target dir:
// absolute paths, parent traversal, backslashes, drive letters, NUL bytes and
// non-normalized spellings.
func cleanZipName(name string) (string, error) {
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || strings.ContainsRune(trimmed, 0) || strings.ContainsRune(trimmed, '\\') || strings.ContainsRune(trimmed, ':') {
		return "", errors.New("unsafe zip entry name")
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") || cleaned != trimmed {
		return "", errors.New("unsafe zip entry name")
	}
	return cleaned, nil
}
