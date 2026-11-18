package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ExportCursorPath is the single sidecar cursor for a space's audit export.
// Keeping it beside the ledger preserves the ledger's atomic rename boundary.
func ExportCursorPath(ledgerPath string) string {
	return filepath.Join(filepath.Dir(ledgerPath), "audit-export.cursor")
}

// ExportDestinationKey binds a cursor to one destination and filter set so a
// changed webhook cannot silently receive already-acknowledged history.
func ExportDestinationKey(destination string, kinds []string) string {
	canonicalKinds := append([]string(nil), kinds...)
	slices.Sort(canonicalKinds)
	sum := sha256.Sum256([]byte(destination + "\x00" + strings.Join(canonicalKinds, ",")))
	return hex.EncodeToString(sum[:])
}

type exportCursor struct {
	Schema      int    `json:"schema"`
	Destination string `json:"destination"`
	After       uint64 `json:"after"`
}

func LoadExportCursor(path, destination string, kinds []string) (uint64, error) {
	if !filepath.IsAbs(path) {
		return 0, fmt.Errorf("%w: export cursor path", ErrInvalid)
	}
	info, err := os.Lstat(path) // #nosec G304 -- operator-selected ledger sidecar.
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%w: nonregular export cursor", ErrInvalid)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- validated sidecar path.
	if err != nil {
		return 0, err
	}
	var cursor exportCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return 0, fmt.Errorf("%w: corrupt export cursor", ErrInvalid)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return 0, fmt.Errorf("%w: trailing export cursor data", ErrInvalid)
	}
	if cursor.Schema != 1 || cursor.Destination != ExportDestinationKey(destination, kinds) {
		return 0, fmt.Errorf("%w: export cursor destination", ErrInvalid)
	}
	return cursor.After, nil
}

func StoreExportCursor(path, destination string, kinds []string, after uint64) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: export cursor path", ErrInvalid)
	}
	body, err := json.Marshal(exportCursor{Schema: 1, Destination: ExportDestinationKey(destination, kinds), After: after})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600) // #nosec G304 -- cursor sidecar derived from validated path.
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncAuditDirectory(filepath.Dir(path))
}
