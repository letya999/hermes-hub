package contextlife

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Restore drill (issue #200): a backup nobody rehearsed is a gamble. Drill
// restores an archive into scratch space and proves the restored bytes
// match the manifest hashes — the same integrity contract Restore already
// enforces, exercised end-to-end so "restore works" is evidence, not hope.
// Scratch space is caller-owned; Drill never touches the live home.

type DrillReport struct {
	User       string    `json:"user"`
	Archive    string    `json:"archive"`
	Checked    int       `json:"checked"`
	VerifiedAt time.Time `json:"verified_at"`
}

// ReadManifest exposes the archive's manifest for bounded drill/reporting
// callers that need the hash map without re-implementing the zip walk.
func ReadManifest(archive string) (Manifest, error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return Manifest{}, err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Manifest{}, err
		}
		body, err := io.ReadAll(io.LimitReader(rc, 2<<20))
		_ = rc.Close()
		if err != nil {
			return Manifest{}, err
		}
		var manifest Manifest
		if err := json.Unmarshal(body, &manifest); err != nil || manifest.Schema != Schema {
			return Manifest{}, errors.New("invalid backup manifest")
		}
		return manifest, nil
	}
	return Manifest{}, errors.New("backup missing manifest")
}

// Drill restores archive into scratchHome and verifies every restored
// file against the manifest's SHA-256 map. scratchHome must be a fresh or
// disposable directory — the drill is destructive within it by design.
// Spool records are out of scope: they need a live spoolDir, which a
// scratch restore deliberately never gets.
func Drill(user, archive, scratchHome string) (DrillReport, error) {
	report := DrillReport{User: user, Archive: filepath.Base(archive), VerifiedAt: time.Now().UTC()}
	if err := Restore(scratchHome, user, archive, ""); err != nil {
		return report, err
	}
	manifest, err := ReadManifest(archive)
	if err != nil {
		return report, err
	}
	absScratch, err := filepath.Abs(scratchHome)
	if err != nil {
		return report, err
	}
	for name, expected := range manifest.SHA256 {
		if strings.HasPrefix(name, "spool/") {
			continue
		}
		target := filepath.Join(absScratch, filepath.FromSlash(name))
		if !strings.HasPrefix(target, absScratch) {
			return report, errors.New("unsafe manifest path")
		}
		body, err := os.ReadFile(target)
		if err != nil {
			return report, errors.New("restored file missing: " + name)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != expected {
			return report, errors.New("restored hash mismatch: " + name)
		}
		report.Checked++
	}
	return report, nil
}
