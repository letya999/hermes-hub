//go:build linux && cgo

package journal

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

// sqlMetaGet reads one meta key from a schema. Missing keys are not errors.
func sqlMetaGet(db *sql.DB, schema, key string) (string, bool) {
	var v string
	// #nosec G202 -- schema is a fixed "r"/"p" literal from call sites.
	if err := db.QueryRow("SELECT v FROM "+schema+".meta WHERE k=?", key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// sqlMetaSet upserts a meta key inside tx on every schema.
func sqlMetaSet(tx *sql.Tx, schemas []string, key, value string) error {
	for _, schema := range schemas {
		// #nosec G202 -- schema is a fixed "r"/"p" literal.
		if _, err := tx.Exec("REPLACE INTO "+schema+".meta(k,v) VALUES(?,?)", key, value); err != nil {
			return err
		}
	}
	return nil
}

// sqlRecordsFloor reads the rotation markers: the smallest retained sequence
// and the hash it chains onto (the tip of the archived history). Defaults
// describe an unrotated store.
func sqlRecordsFloor(db *sql.DB, schema string) (floor uint64, floorPrev string) {
	floor = 1
	if v, ok := sqlMetaGet(db, schema, "records_floor"); ok {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 1 {
			floor = n
		}
	}
	if v, ok := sqlMetaGet(db, schema, "records_floor_prev"); ok {
		floorPrev = v
	}
	return floor, floorPrev
}

// recordHash recomputes the chain hash of one stored record — the same
// canonical marshal replay uses.
func recordHash(seq uint64, prev, kind string, body []byte) string {
	plain, err := json.Marshal(Record{Sequence: seq, Previous: prev, Kind: kind, Data: json.RawMessage(body)})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(plain)
	return hex.EncodeToString(sum[:])
}

// recordsArchiveRow is the canonical payload inside a sealed records
// archive — the full chained record, so the exported segment verifies
// standalone.
type recordsArchiveRow struct {
	Seq  uint64 `json:"seq"`
	Prev string `json:"prev"`
	Kind string `json:"kind"`
	Body []byte `json:"body"`
}

// recordsManifestDoc seals the archive manifest. PrevHi pins the tip hash of
// the archived segment — the value the retained floor re-anchors on, so the
// manifest and the live chain provably meet.
type recordsManifestDoc struct {
	Format        int    `json:"format"`
	Store         string `json:"store"`
	SeqLo         uint64 `json:"seq_lo"`
	SeqHi         uint64 `json:"seq_hi"`
	PrevHi        string `json:"prev_hi"`
	Rows          int    `json:"rows"`
	Bytes         int64  `json:"bytes"`
	Digest        string `json:"digest"`
	ArchiveSHA256 string `json:"archive_sha256"`
	Seal          string `json:"seal"`
}

func recordsManifestHMAC(d recordsManifestDoc, identity []byte) string {
	d.Seal = ""
	raw, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	h := hmac.New(sha256.New, seal.Derive(identity, "records-manifest/v1"))
	_, _ = h.Write(raw)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func recordsArchiveAAD(lo, hi uint64) []byte {
	return []byte("records-archive/v1|" + strconv.FormatUint(lo, 10) + "|" + strconv.FormatUint(hi, 10))
}

func recordsArchiveNames(lo, hi uint64) (sealName, manifestName string) {
	base := "records-" + strconv.FormatUint(lo, 10) + "-" + strconv.FormatUint(hi, 10)
	return base + ".seal", base + ".manifest.json"
}

// writeRecordsArchive seals the exported history prefix as immutable files —
// existing files are never overwritten.
func (j *Journal) writeRecordsArchive(dir string, rows []recordsArchiveRow, lo, hi uint64) (string, error) {
	enc := make([]recordsArchiveRow, 0, len(rows))
	var total int64
	var prevHi string
	for _, r := range rows {
		enc = append(enc, r)
		total += int64(len(r.Body))
		prevHi = recordHash(r.Seq, r.Prev, r.Kind, r.Body)
	}
	payload, err := json.Marshal(enc)
	if err != nil {
		return "", err
	}
	box, err := seal.New(seal.Derive(j.identity, "records-archive/v1"))
	if err != nil {
		return "", err
	}
	sealed, err := box.Seal(payload, recordsArchiveAAD(lo, hi))
	if err != nil {
		return "", err
	}
	sealName, manName := recordsArchiveNames(lo, hi)
	if err := securefs.Create(dir, sealName, sealed, 0600); err != nil {
		return "", err
	}
	payloadSum := sha256.Sum256(payload)
	encSum := sha256.Sum256(sealed)
	d := recordsManifestDoc{
		Format:        1,
		Store:         base64.RawURLEncoding.EncodeToString(j.identity),
		SeqLo:         lo,
		SeqHi:         hi,
		PrevHi:        prevHi,
		Rows:          len(rows),
		Bytes:         total,
		Digest:        base64.RawURLEncoding.EncodeToString(payloadSum[:]),
		ArchiveSHA256: base64.RawURLEncoding.EncodeToString(encSum[:]),
	}
	d.Seal = recordsManifestHMAC(d, j.identity)
	manifest, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	if err := securefs.Create(dir, manName, manifest, 0600); err != nil {
		return "", err
	}
	return filepath.Join(dir, sealName), nil
}

func recordsManifest(dir, name string, identity []byte) (recordsManifestDoc, error) {
	raw, err := securefs.Read(dir, name, 1<<20)
	if err != nil {
		return recordsManifestDoc{}, err
	}
	var d recordsManifestDoc
	if err := strictjson.Decode(raw, &d); err != nil {
		return recordsManifestDoc{}, errors.New("records manifest malformed")
	}
	if d.Format != 1 || d.Rows < 0 || d.SeqHi < d.SeqLo {
		return recordsManifestDoc{}, errors.New("records manifest invalid")
	}
	if !seal.Equal(d.Store, base64.RawURLEncoding.EncodeToString(identity)) {
		return recordsManifestDoc{}, errors.New("records manifest store mismatch")
	}
	if !seal.Equal(d.Seal, recordsManifestHMAC(d, identity)) {
		return recordsManifestDoc{}, errors.New("records manifest seal mismatch")
	}
	return d, nil
}

// ReadRecordsArchive decrypts and verifies a sealed records archive against
// its manifest — and re-verifies the internal hash chain so the exported
// segment stands alone as provable history.
func ReadRecordsArchive(sealFile, manifestFile string, identity []byte) ([]Record, error) {
	if len(identity) == 0 {
		return nil, errors.New("records archive identity missing")
	}
	mdir, mname := filepath.Dir(manifestFile), filepath.Base(manifestFile)
	d, err := recordsManifest(mdir, mname, identity)
	if err != nil {
		return nil, err
	}
	sdir, sname := filepath.Dir(sealFile), filepath.Base(sealFile)
	enc, err := securefs.Read(sdir, sname, maxAuditHotCap*2)
	if err != nil {
		return nil, err
	}
	encSum := sha256.Sum256(enc)
	if !seal.Equal(d.ArchiveSHA256, base64.RawURLEncoding.EncodeToString(encSum[:])) {
		return nil, errors.New("records archive digest mismatch")
	}
	box, err := seal.New(seal.Derive(identity, "records-archive/v1"))
	if err != nil {
		return nil, err
	}
	plain, err := box.Open(enc, recordsArchiveAAD(d.SeqLo, d.SeqHi))
	if err != nil {
		return nil, errors.New("records archive decryption failed")
	}
	plainSum := sha256.Sum256(plain)
	if !seal.Equal(d.Digest, base64.RawURLEncoding.EncodeToString(plainSum[:])) {
		return nil, errors.New("records archive payload digest mismatch")
	}
	var rows []recordsArchiveRow
	if err := strictjson.Decode(plain, &rows); err != nil {
		return nil, errors.New("records archive payload malformed")
	}
	var bytes int64
	for _, r := range rows {
		bytes += int64(len(r.Body))
	}
	if len(rows) != d.Rows || bytes != d.Bytes {
		return nil, errors.New("records archive manifest fields inconsistent")
	}
	out := make([]Record, 0, len(rows))
	expect := d.SeqLo
	var prev string
	for _, r := range rows {
		// The segment must chain internally: every row's prev equals the
		// previous row's hash, sequences are gapless and the tip matches the
		// sealed manifest link.
		if r.Seq != expect || (r.Seq > d.SeqLo && r.Prev != prev) {
			return nil, errors.New("records archive chain broken")
		}
		out = append(out, Record{Sequence: r.Seq, Previous: r.Prev, Kind: r.Kind, Data: append(json.RawMessage(nil), r.Body...)})
		prev = recordHash(r.Seq, r.Prev, r.Kind, r.Body)
		expect++
	}
	if len(out) > 0 && !seal.Equal(prev, d.PrevHi) {
		return nil, errors.New("records archive tip hash mismatch")
	}
	return out, nil
}

// RetainRecords exports the history prefix into a sealed records archive and,
// only under the explicit destructive policy, deletes the verified source
// rows from both legs, re-anchors the chain at the new floor and VACUUMs the
// files — the physical shrink the byte cap alone never delivers.
func (j *Journal) RetainRecords(keepBytes int64) (RetainReport, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.retainRecordsLocked(keepBytes)
}

// retainRecordsLocked is RetainRecords with the caller already holding j.mu.
func (j *Journal) retainRecordsLocked(keepBytes int64) (RetainReport, error) {
	rep := RetainReport{}
	if j.sql == nil {
		return rep, ErrUnavailable
	}
	pol := j.retainR
	if pol.ArchiveDir == "" {
		return rep, errors.New("records retention requires an archive directory")
	}
	if keepBytes <= 0 || (j.limit > 0 && keepBytes >= j.limit) {
		return rep, errors.New("records retention requires a keep bound below the ledger cap")
	}
	if err := securefs.Dir(pol.ArchiveDir); err != nil {
		return rep, err
	}
	floor, _ := sqlRecordsFloor(j.sql.db, "r")
	rows, err := j.sql.db.Query("SELECT seq,prev,kind,body FROM r.records ORDER BY seq")
	if err != nil {
		return rep, ErrUnavailable
	}
	var recs []recordsArchiveRow
	for rows.Next() {
		var r recordsArchiveRow
		if err := rows.Scan(&r.Seq, &r.Prev, &r.Kind, &r.Body); err != nil {
			_ = rows.Close()
			return rep, ErrUnavailable
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return rep, ErrUnavailable
	}
	_ = rows.Close()
	if len(recs) == 0 {
		rep.HotBytes = j.size
		return rep, nil
	}
	// Walk back from the head until the retained suffix fits keepBytes; the
	// head row is always retained.
	var suffix int64
	cut := len(recs) - 1
	for ; cut >= 0; cut-- {
		n := int64(len(recs[cut].Body)) + 64
		if suffix+n > keepBytes && cut < len(recs)-1 {
			break
		}
		suffix += n
	}
	cut++
	newFloor := recs[cut].Seq
	rep.HotBytes = suffix
	if newFloor <= floor {
		return rep, nil
	}
	lo, hi := floor, newFloor-1
	var archive []recordsArchiveRow
	for _, r := range recs {
		if r.Seq >= lo && r.Seq <= hi {
			archive = append(archive, r)
		}
	}
	sealName, manName := recordsArchiveNames(lo, hi)
	sealPath := filepath.Join(pol.ArchiveDir, sealName)
	manPath := filepath.Join(pol.ArchiveDir, manName)
	// An archive already covering this range is verified, never rewritten —
	// the same fail-closed discipline audit retention follows.
	if _, err := os.Stat(sealPath); err == nil {
		if _, err := ReadRecordsArchive(sealPath, manPath, j.identity); err != nil {
			return rep, fmt.Errorf("existing records archive failed verification: %w", err)
		}
	} else {
		if _, err := j.writeRecordsArchive(pol.ArchiveDir, archive, lo, hi); err != nil {
			return rep, err
		}
		if _, err := ReadRecordsArchive(sealPath, manPath, j.identity); err != nil {
			return rep, fmt.Errorf("fresh records archive failed verification: %w", err)
		}
	}
	rep.Exported = true
	rep.Archive = sealPath
	rep.SeqLo, rep.SeqHi, rep.Rows = lo, hi, len(archive)
	for _, r := range archive {
		rep.Bytes += int64(len(r.Body))
	}
	if pol.Destructive {
		// The verified archive covers [lo,hi]: delete the source prefix and
		// re-anchor the floor on the first retained row's prev — the hash of
		// the archived tip — in one transaction across both legs.
		tx, err := j.sql.db.Begin()
		if err != nil {
			return rep, ErrUnavailable
		}
		for _, schema := range j.sqlSchemas() {
			// #nosec G202 -- schema is a fixed "r"/"p" literal.
			if _, err := tx.Exec("DELETE FROM "+schema+".records WHERE seq < ?", newFloor); err != nil {
				_ = tx.Rollback()
				j.poisoned = true
				return rep, ErrUnavailable
			}
		}
		if err := sqlMetaSet(tx, j.sqlSchemas(), "records_floor", strconv.FormatUint(newFloor, 10)); err != nil {
			_ = tx.Rollback()
			j.poisoned = true
			return rep, ErrUnavailable
		}
		if err := sqlMetaSet(tx, j.sqlSchemas(), "records_floor_prev", recs[cut].Prev); err != nil {
			_ = tx.Rollback()
			j.poisoned = true
			return rep, ErrUnavailable
		}
		if err := tx.Commit(); err != nil {
			j.poisoned = true
			return rep, ErrUnavailable
		}
		rep.Pruned = true
		j.size = suffix
		// Compaction physically returns the space. A failure leaves the prune
		// committed and is reported, never hidden.
		for _, schema := range j.sqlSchemas() {
			// #nosec G202 -- schema is a fixed "r"/"p" literal.
			if _, err := j.sql.db.Exec("VACUUM " + schema); err != nil {
				return rep, fmt.Errorf("records compaction failed: %w", err)
			}
		}
	}
	rep.HotBytes = j.size
	rep.OverCap = j.limit > 0 && j.size > j.limit*8/10
	return rep, nil
}

// sweepRecordsArchives bounds the archive directory: verified pairs older
// than the age cap — and the oldest pairs while total bytes exceed the byte
// cap — are permanently deleted. The newest verified pair is never deleted:
// it carries the provenance link the retained floor chains onto. Anything
// not verified as this store's archive is left untouched, fail-closed
// (SPEC-0087 R5.2).
func (j *Journal) sweepRecordsArchives(dir string, maxBytes int64, maxAgeDays int) error {
	if dir == "" || (maxBytes <= 0 && maxAgeDays <= 0) {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type pair struct {
		lo, hi       uint64
		kind         string
		sealName     string
		manifestName string
		bytes        int64
		mtime        time.Time
	}
	byBase := map[string]*pair{}
	for _, e := range ents {
		var lo, hi uint64
		var kind string
		var isSeal bool
		switch {
		case strings.HasSuffix(e.Name(), ".seal"):
			if n, err := fmt.Sscanf(e.Name(), "records-%d-%d.seal", &lo, &hi); err == nil && n == 2 {
				kind = "records"
			} else if n, err := fmt.Sscanf(e.Name(), "audit-%d-%d.seal", &lo, &hi); err == nil && n == 2 {
				kind = "audit"
			}
			isSeal = true
		case strings.HasSuffix(e.Name(), ".manifest.json"):
			if n, err := fmt.Sscanf(e.Name(), "records-%d-%d.manifest.json", &lo, &hi); err == nil && n == 2 {
				kind = "records"
			} else if n, err := fmt.Sscanf(e.Name(), "audit-%d-%d.manifest.json", &lo, &hi); err == nil && n == 2 {
				kind = "audit"
			}
		}
		if kind == "" {
			continue
		}
		base := kind + "-" + strconv.FormatUint(lo, 10) + "-" + strconv.FormatUint(hi, 10)
		p := byBase[base]
		if p == nil {
			p = &pair{lo: lo, hi: hi, kind: kind}
			byBase[base] = p
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		p.bytes += fi.Size()
		if fi.ModTime().After(p.mtime) {
			p.mtime = fi.ModTime()
		}
		if isSeal {
			p.sealName = e.Name()
		} else {
			p.manifestName = e.Name()
		}
	}
	var owned []*pair
	for _, p := range byBase {
		if p.sealName == "" || p.manifestName == "" {
			continue
		}
		var valid bool
		switch p.kind {
		case "records":
			_, err := recordsManifest(dir, p.manifestName, j.identity)
			valid = err == nil
		case "audit":
			_, err := auditManifest(dir, p.manifestName, j.identity)
			valid = err == nil
		}
		if !valid {
			continue
		}
		owned = append(owned, p)
	}
	sort.Slice(owned, func(a, b int) bool { return owned[a].hi < owned[b].hi })
	var newestHi uint64
	if len(owned) > 0 {
		newestHi = owned[len(owned)-1].hi
	}
	drop := func(p *pair) {
		_ = os.Remove(filepath.Join(dir, p.sealName))
		_ = os.Remove(filepath.Join(dir, p.manifestName))
	}
	if maxAgeDays > 0 {
		cutoff := time.Now().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
		var keep []*pair
		for _, p := range owned {
			if p.hi != newestHi && p.mtime.Before(cutoff) {
				drop(p)
				continue
			}
			keep = append(keep, p)
		}
		owned = keep
	}
	var total int64
	for _, p := range owned {
		total += p.bytes
	}
	for i := 0; i < len(owned)-1 && total > maxBytes; i++ {
		total -= owned[i].bytes
		drop(owned[i])
	}
	return nil
}

// RetainMaintenance runs one high-water tick: when hot records bytes pass 80%
// of the ledger cap the prefix rotates to half-cap; when hot audit bytes pass
// 80% of the audit cap the prefix exports (and prunes under the destructive
// policy). Checks read in-memory counters — the tick is free at low water.
// The outcome persists in meta so a failing cleanup stays visible across
// restarts (SPEC-0087 R5.2).
func (j *Journal) RetainMaintenance() MaintenanceReport {
	j.mu.Lock()
	defer j.mu.Unlock()
	rep := MaintenanceReport{}
	if j.sql == nil {
		return rep
	}
	var lastErr string
	if j.retention.HotMaxBytes > 0 && j.retention.ArchiveDir != "" &&
		j.auditBytes > j.retention.HotMaxBytes*8/10 {
		if r, err := j.retainAuditsLocked(); err != nil {
			lastErr = err.Error()
		} else {
			rep.Audit = &r
		}
	}
	if j.limit > 0 && j.retainR.ArchiveDir != "" && j.size > j.limit*8/10 {
		if r, err := j.retainRecordsLocked(j.limit / 2); err != nil {
			if lastErr != "" {
				lastErr += "; "
			}
			lastErr += err.Error()
		} else {
			rep.Records = &r
		}
	}
	if err := j.sweepRecordsArchives(j.retainR.ArchiveDir, j.retainR.ArchiveMaxBytes, j.retainR.ArchiveMaxAgeDays); err != nil {
		if lastErr != "" {
			lastErr += "; "
		}
		lastErr += err.Error()
	}
	if j.retention.ArchiveDir != j.retainR.ArchiveDir {
		if err := j.sweepRecordsArchives(j.retention.ArchiveDir, j.retention.ArchiveMaxBytes, j.retention.ArchiveMaxAgeDays); err != nil {
			if lastErr != "" {
				lastErr += "; "
			}
			lastErr += err.Error()
		}
	}
	rep.LastError = lastErr
	// The marker persists only when a pass ran or failed — idle ticks write
	// nothing, and an unchanged marker is not rewritten (per-second writes
	// at sustained high water would churn the WAL for no benefit).
	if rep.Audit != nil || rep.Records != nil || lastErr != "" {
		key, val := "retain_last_ok", "1"
		if lastErr != "" {
			key, val = "retain_last_error", truncateErr(lastErr)
		}
		if cur, ok := sqlMetaGet(j.sql.db, "r", key); !ok || cur != val {
			if tx, err := j.sql.db.Begin(); err == nil {
				if sqlMetaSet(tx, j.sqlSchemas(), key, val) == nil {
					if e := tx.Commit(); e == nil {
						rep.Marker = key
					}
				} else {
					_ = tx.Rollback()
				}
			}
		}
	}
	return rep
}

func truncateErr(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
