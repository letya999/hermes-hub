package inboundfile

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

const (
	maxInboundUnpackEntries = 8192
	maxInboundUnpackBytes   = int64(1 << 30)
	maxInboundEntryBytes    = int64(8 << 20)
)

// unpackZip extracts a validated zip into target under ws. Entry count and
// decompressed bytes are bounded (declared size is checked, actual bytes are
// capped by the reader), unsafe names, encrypted, non-file and symlink entries
// are refused, and the partial target is removed on any failure.
func SafeUnpack(ws *os.Root, zr *zip.Reader, target string) (int, error) {
	if maxInboundUnpackBytes < 0 {
		return 0, errors.New("archive byte limit must not be negative")
	}
	if len(zr.File) > maxInboundUnpackEntries {
		return 0, errors.New("archive has too many entries")
	}
	var declared uint64
	for _, entry := range zr.File {
		if entry.UncompressedSize64 > uint64(maxInboundEntryBytes) {
			return 0, errors.New("archive entry exceeds the 8 MiB artifact limit")
		}
		if entry.UncompressedSize64 > uint64(maxInboundUnpackBytes)-declared {
			return 0, errors.New("archive exceeds the unpacked size limit")
		}
		declared += entry.UncompressedSize64
	}
	if err := ws.MkdirAll(path.Dir(target), 0700); err != nil {
		return 0, err
	}
	// Exclusive creation establishes which directory this attempt may remove.
	if err := ws.Mkdir(target, 0700); err != nil {
		return 0, err
	}
	files := 0
	var actual int64
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
			var n int64
			n, err = io.Copy(out, io.LimitReader(rc, maxInboundUnpackBytes-actual+1))
			actual += n
			if err == nil && actual > maxInboundUnpackBytes {
				err = errors.New("archive exceeds the unpacked size limit")
			}
			if err == nil {
				err = out.Sync()
			}
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
