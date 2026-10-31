package audit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxLegacyAuditBytes = 256 << 20
const maxLegacyAuditRecords = 1 << 20

var ErrStaleLegacyPlan = errors.New("stale legacy audit plan")

// LegacyPlan describes only the size and digest of a schema-1-only ledger.
type LegacyPlan struct {
	Scope   string `json:"scope"`
	Digest  string `json:"digest"`
	Bytes   int64  `json:"bytes"`
	Records uint64 `json:"records"`
}

func legacyLedger(path string) (*Ledger, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != "audit.jsonl" {
		return nil, fmt.Errorf("%w: legacy audit path", ErrInvalid)
	}
	return &Ledger{path: path, bounds: defaultBounds}, nil
}

// PlanLegacyRollover scans every old record without emitting any audit fields.
func PlanLegacyRollover(path string) (LegacyPlan, error) {
	l, err := legacyLedger(path)
	if err != nil {
		return LegacyPlan{}, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return LegacyPlan{}, fmt.Errorf("%w: audit directory unavailable", ErrInvalid)
	}
	defer root.Close()
	return l.scanLegacy(root, nil)
}

func (l *Ledger) scanLegacy(root *os.Root, copyTo io.Writer) (LegacyPlan, error) {
	plan := LegacyPlan{Scope: "legacy-audit"}
	base := filepath.Base(l.path)
	info, err := regular(root, base)
	if err != nil || info.Size() > maxLegacyAuditBytes {
		return LegacyPlan{}, fmt.Errorf("%w: legacy audit unavailable or oversized", ErrInvalid)
	}
	dir, err := root.Open(".")
	if err != nil {
		return LegacyPlan{}, fmt.Errorf("%w: audit directory unavailable", ErrInvalid)
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return LegacyPlan{}, fmt.Errorf("%w: audit directory unavailable", ErrInvalid)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), base+".v1-") {
			return LegacyPlan{}, fmt.Errorf("%w: segmented audit cannot roll over as legacy", ErrInvalid)
		}
	}
	f, err := root.Open(base)
	if err != nil {
		return LegacyPlan{}, fmt.Errorf("%w: legacy audit unavailable", ErrInvalid)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return LegacyPlan{}, fmt.Errorf("%w: legacy audit changed", ErrInvalid)
	}
	h := sha256.New()
	r := bufio.NewReaderSize(f, MaxRecordBytes+1)
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil || len(line) > MaxRecordBytes || plan.Records >= maxLegacyAuditRecords {
			return LegacyPlan{}, fmt.Errorf("%w: partial, oversized or excessive legacy audit", ErrInvalid)
		}
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || event.Schema != 1 || validate(event) != nil || containsSensitive(line) {
			return LegacyPlan{}, fmt.Errorf("%w: invalid legacy audit record %d", ErrInvalid, plan.Records+1)
		}
		plan.Records++
		plan.Bytes += int64(len(line))
		if plan.Bytes > maxLegacyAuditBytes {
			return LegacyPlan{}, fmt.Errorf("%w: legacy audit oversized", ErrInvalid)
		}
		_, _ = h.Write(line)
		if copyTo != nil {
			if _, err := copyTo.Write(line); err != nil {
				return LegacyPlan{}, fmt.Errorf("%w: archive write failed", ErrWrite)
			}
		}
	}
	if plan.Bytes != info.Size() {
		return LegacyPlan{}, fmt.Errorf("%w: legacy audit changed", ErrInvalid)
	}
	plan.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return plan, nil
}

// RolloverLegacy retains every validated schema-1 record in a verified gzip
// archive, then starts a new bounded ledger. The ToolHub writer must be stopped.
func RolloverLegacy(path, expectedDigest string) (LegacyPlan, error) {
	l, err := legacyLedger(path)
	if err != nil {
		return LegacyPlan{}, err
	}
	var result LegacyPlan
	err = l.locked(func(root *os.Root) error {
		plan, err := l.scanLegacy(root, nil)
		if err != nil {
			return err
		}
		if plan.Digest != expectedDigest {
			return ErrStaleLegacyPlan
		}
		base := filepath.Base(path)
		archive := base + ".legacy-" + strings.TrimPrefix(plan.Digest, "sha256:") + ".jsonl.gz"
		tmp := archive + ".partial"
		raw := archive + ".raw"
		for _, name := range []string{archive, tmp, raw} {
			if _, err := root.Lstat(name); err == nil || !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: rollover target already exists", ErrInvalid)
			}
		}
		out, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("%w: archive unavailable", ErrWrite)
		}
		gz := gzip.NewWriter(out)
		copied, copyErr := l.scanLegacy(root, gz)
		closeGZ := gz.Close()
		syncErr := out.Sync()
		closeOut := out.Close()
		if copyErr != nil || closeGZ != nil || syncErr != nil || closeOut != nil || copied != plan {
			_ = root.Remove(tmp)
			return fmt.Errorf("%w: archive creation failed", ErrWrite)
		}
		if err := verifyLegacyArchive(root, tmp, plan); err != nil {
			_ = root.Remove(tmp)
			return err
		}
		if err := root.Rename(tmp, archive); err != nil {
			return fmt.Errorf("%w: archive rename failed", ErrWrite)
		}
		if err := syncAuditDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("%w: archive sync failed", ErrWrite)
		}
		if err := root.Rename(base, raw); err != nil {
			return fmt.Errorf("%w: source rename failed", ErrWrite)
		}
		if err := syncAuditDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("%w: source sync failed", ErrWrite)
		}
		fresh, err := root.OpenFile(base, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("%w: new audit unavailable", ErrWrite)
		}
		marker := NewEvent("job", "operator", "legacy-audit-rollover")
		marker.Name = plan.Digest
		body, err := json.Marshal(marker)
		if err == nil && (validate(marker) != nil || containsSensitive(body)) {
			err = ErrInvalid
		}
		if err == nil {
			_, err = fresh.Write(append(body, '\n'))
		}
		if err == nil {
			err = fresh.Sync()
		}
		closeFresh := fresh.Close()
		if err != nil || closeFresh != nil {
			return fmt.Errorf("%w: new audit write failed", ErrWrite)
		}
		if err := syncAuditDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("%w: new audit sync failed", ErrWrite)
		}
		if err := root.Remove(raw); err != nil {
			return fmt.Errorf("%w: old source cleanup failed", ErrWrite)
		}
		if err := syncAuditDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("%w: old source cleanup sync failed", ErrWrite)
		}
		result = plan
		return nil
	})
	return result, err
}

func verifyLegacyArchive(root *os.Root, name string, expected LegacyPlan) error {
	f, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("%w: archive unavailable", ErrWrite)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: archive unreadable", ErrWrite)
	}
	defer gz.Close()
	h := sha256.New()
	var bytesRead int64
	buf := make([]byte, 64<<10)
	for {
		n, readErr := gz.Read(buf)
		if n > 0 {
			bytesRead += int64(n)
			if bytesRead > expected.Bytes {
				return fmt.Errorf("%w: archive size mismatch", ErrWrite)
			}
			_, _ = h.Write(buf[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("%w: archive read failed", ErrWrite)
		}
	}
	if bytesRead != expected.Bytes || "sha256:"+hex.EncodeToString(h.Sum(nil)) != expected.Digest {
		return fmt.Errorf("%w: archive digest mismatch", ErrWrite)
	}
	return nil
}
