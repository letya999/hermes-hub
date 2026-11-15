//go:build linux && cgo

package journal

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

func (j *Journal) sqlSchemas() []string {
	if j.sql.replicated {
		return []string{"r", "p"}
	}
	return []string{"r"}
}

// appendSQLAux runs the normalized-table writes and marker update inside tx.
func (j *Journal) appendSQLAux(tx *sql.Tx, seq uint64, aux *AuxWrites) error {
	for _, schema := range j.sqlSchemas() {
		for _, s := range aux.States {
			// #nosec G202 -- schema is a fixed "r"/"p" literal.
			if _, err := tx.Exec("REPLACE INTO "+schema+".state(kind,id,seq,body) VALUES(?,?,?,?)", s.Kind, s.ID, seq, s.Body); err != nil {
				return err
			}
		}
		if a := aux.Audit; a != nil {
			// #nosec G202 -- schema is a fixed "r"/"p" literal.
			if _, err := tx.Exec("INSERT OR REPLACE INTO "+schema+".audit(seq,kind,principal,context,body) VALUES(?,?,?,?,?)", seq, a.Kind, a.Principal, a.Context, a.Body); err != nil {
				return err
			}
		}
		// #nosec G202 -- schema is a fixed "r"/"p" literal.
		if _, err := tx.Exec("REPLACE INTO "+schema+".meta(k,v) VALUES('state_seq',?)", strconv.FormatUint(seq, 10)); err != nil {
			return err
		}
	}
	return nil
}

// sqlNormalized reports whether the state/audit tables provably cover the
// head: the marker must exist and its state_seq must equal the latest
// sequence. Any append that bypassed the aux path (recovery catch-up, a raw
// Append) leaves state_seq stale and the store reports degraded instead of
// serving incomplete tables.
func (j *Journal) sqlNormalized() bool {
	var v string
	if err := j.sql.db.QueryRow("SELECT v FROM r.meta WHERE k='normalized'").Scan(&v); err != nil || v != "1" {
		return false
	}
	var stateSeq string
	if err := j.sql.db.QueryRow("SELECT v FROM r.meta WHERE k='state_seq'").Scan(&stateSeq); err != nil {
		return false
	}
	if j.sql.replicated {
		var pv string
		if err := j.sql.db.QueryRow("SELECT v FROM p.meta WHERE k='normalized'").Scan(&pv); err != nil || pv != "1" {
			return false
		}
	}
	return stateSeq == strconv.FormatUint(j.seq, 10)
}

// sqlRecords streams the record chain on demand. The sql backend does not
// retain records in memory; callers that need history (legacy replay,
// migrations) pull it explicitly. A read failure poisons the journal so an
// empty result can never pass for an intact store.
func (j *Journal) sqlRecords() []Record {
	rows, err := j.sql.db.Query("SELECT seq,prev,kind,body FROM r.records ORDER BY seq")
	if err != nil {
		j.poisoned = true
		return nil
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var body []byte
		if err := rows.Scan(&r.Sequence, &r.Previous, &r.Kind, &body); err != nil {
			j.poisoned = true
			return nil
		}
		r.Data = append([]byte(nil), body...)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		j.poisoned = true
		return nil
	}
	return out
}

// StateRows returns the complete current-state table. It is bounded by the
// number of live domain objects, never by journal history length.
func (j *Journal) StateRows() ([]StateRow, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sql == nil {
		return nil, ErrUnavailable
	}
	rows, err := j.sql.db.Query("SELECT kind,id,seq,body FROM r.state ORDER BY kind,id")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	var out []StateRow
	for rows.Next() {
		var r StateRow
		if err := rows.Scan(&r.Kind, &r.ID, &r.Seq, &r.Body); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

// AuditPage returns up to limit audit rows with seq > after, scope-filtered.
func (j *Journal) AuditPage(after uint64, limit int, scope AuditScope) ([]AuditRow, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sql == nil {
		return nil, ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	any := 0
	if scope.AnyPrincipal {
		any = 1
	}
	rows, err := j.sql.db.Query(
		"SELECT seq,kind,principal,context,body FROM r.audit WHERE seq > ? AND (kind = ? OR (context = ? AND (principal = ? OR ?))) ORDER BY seq LIMIT ?",
		after, scope.Broadcast, scope.Context, scope.Principal, any, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.Seq, &r.Kind, &r.Principal, &r.Context, &r.Body); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

// normalizeSQL writes normalized-table rows derived from every journaled
// record in bounded batch transactions, then sets the marker. A crash mid-pass
// leaves partial tables without a marker — rerunnable because REPLACE is
// idempotent and keyed by (kind,id)/seq.
func normalizeSQL(db *sql.DB, schemas []string, head uint64, norm NormalizeFunc) error {
	rows, err := db.Query("SELECT seq,prev,kind,body FROM r.records ORDER BY seq")
	if err != nil {
		return ErrUnavailable
	}
	var recs []Record
	for rows.Next() {
		var r Record
		var body []byte
		if err := rows.Scan(&r.Sequence, &r.Previous, &r.Kind, &body); err != nil {
			_ = rows.Close()
			return ErrUnavailable
		}
		r.Data = append([]byte(nil), body...)
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ErrUnavailable
	}
	_ = rows.Close()
	const batch = 512
	writeBatch := func(recs []Record, finalize bool) error {
		tx, err := db.Begin()
		if err != nil {
			return ErrUnavailable
		}
		for _, r := range recs {
			states, audit, err := norm(r)
			if err != nil {
				_ = tx.Rollback()
				return ErrUnavailable
			}
			for _, schema := range schemas {
				for _, s := range states {
					if s.Kind == "" || s.ID == "" || len(s.Body) == 0 || len(s.Body) > MaxRecord {
						_ = tx.Rollback()
						return ErrUnavailable
					}
					// #nosec G202 -- schema is a fixed "r"/"p" literal.
					if _, err := tx.Exec("REPLACE INTO "+schema+".state(kind,id,seq,body) VALUES(?,?,?,?)", s.Kind, s.ID, r.Sequence, s.Body); err != nil {
						_ = tx.Rollback()
						return ErrUnavailable
					}
				}
				if audit != nil {
					if audit.Kind == "" || len(audit.Body) == 0 || len(audit.Body) > MaxRecord {
						_ = tx.Rollback()
						return ErrUnavailable
					}
					// #nosec G202 -- schema is a fixed "r"/"p" literal.
					if _, err := tx.Exec("INSERT OR REPLACE INTO "+schema+".audit(seq,kind,principal,context,body) VALUES(?,?,?,?,?)", r.Sequence, audit.Kind, audit.Principal, audit.Context, audit.Body); err != nil {
						_ = tx.Rollback()
						return ErrUnavailable
					}
				}
			}
		}
		if finalize {
			for _, schema := range schemas {
				// #nosec G202 -- schema is a fixed "r"/"p" literal.
				if _, err := tx.Exec("REPLACE INTO "+schema+".meta(k,v) VALUES('normalized','1'),('state_seq',?)", strconv.FormatUint(head, 10)); err != nil {
					_ = tx.Rollback()
					return ErrUnavailable
				}
			}
		}
		if err := tx.Commit(); err != nil {
			return ErrUnavailable
		}
		return nil
	}
	for i := 0; i < len(recs); i += batch {
		end := i + batch
		if end > len(recs) {
			end = len(recs)
		}
		if err := writeBatch(recs[i:end], false); err != nil {
			return err
		}
	}
	return writeBatch(nil, true)
}

// NormalizeSQLCipher backfills the normalized state/audit tables on an
// activated replicated store. Writer locks fence a live service; the chain is
// fully verified on open before any row is derived.
func NormalizeSQLCipher(primaryDir, recoveryDir, anchorDir string, primaryKey, recoveryKey []byte, limit int64, norm NormalizeFunc) error {
	anchor, err := newAnchor(anchorDir, seal.Derive(primaryKey, "journal-store-key/v2"), recoveryKey)
	if err != nil {
		return err
	}
	j, err := openSQLReplicated(primaryDir, recoveryDir, anchor, primaryKey, recoveryKey, limit)
	if err != nil {
		return err
	}
	defer func() { _ = j.Close() }()
	return normalizeSQL(j.sql.db, j.sqlSchemas(), j.seq, norm)
}

// sqlAuditBytes reports the live byte size of the hot audit table. The caller
// holds j.mu.
func (j *Journal) sqlAuditBytes() int64 {
	if j.sql == nil {
		return 0
	}
	var n int64
	if err := j.sql.db.QueryRow("SELECT COALESCE(SUM(LENGTH(body)),0) FROM r.audit").Scan(&n); err != nil {
		return 0
	}
	return n
}

// AuditFloor returns the smallest audit sequence still stored — the re-anchor
// point a client reports when its cursor expired under retention pruning.
func (j *Journal) AuditFloor() (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sql == nil {
		return 0, ErrUnavailable
	}
	// The prune marker carries the floor across an emptied table; the live
	// minimum covers rows retained since it was set.
	var floor uint64
	var v string
	if err := j.sql.db.QueryRow("SELECT v FROM r.meta WHERE k='audit_floor'").Scan(&v); err == nil {
		floor, _ = strconv.ParseUint(v, 10, 64)
	}
	var min sql.NullInt64
	if err := j.sql.db.QueryRow("SELECT MIN(seq) FROM r.audit").Scan(&min); err != nil {
		return 0, ErrUnavailable
	}
	if min.Valid && min.Int64 > 0 && uint64(min.Int64) > floor {
		floor = uint64(min.Int64)
	}
	return floor, nil
}

// auditArchiveRow is the canonical payload encoding inside a sealed archive;
// encoding/json renders Body as base64 so arbitrary record bytes survive.
type auditArchiveRow struct {
	Seq       uint64 `json:"seq"`
	Kind      string `json:"kind"`
	Principal string `json:"principal"`
	Context   string `json:"context"`
	Body      []byte `json:"body"`
}

// auditManifestDoc is the sealed manifest beside every .seal archive. Seal is
// an HMAC-SHA256 (base64url) over the canonical JSON of every other field,
// keyed by a purpose-separated key derived from the store identity — a
// tampered field never verifies, so a forged manifest can never authorize a
// prune (SPEC-0087 R5.4).
type auditManifestDoc struct {
	Format        int    `json:"format"`
	Store         string `json:"store"`
	SeqLo         uint64 `json:"seq_lo"`
	SeqHi         uint64 `json:"seq_hi"`
	Rows          int    `json:"rows"`
	Bytes         int64  `json:"bytes"`
	Digest        string `json:"digest"`
	ArchiveSHA256 string `json:"archive_sha256"`
	Seal          string `json:"seal"`
}

// auditManifestHMAC seals the manifest fields. Struct marshal order is fixed
// so the covered form is canonical.
func auditManifestHMAC(d auditManifestDoc, identity []byte) string {
	d.Seal = ""
	raw, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	h := hmac.New(sha256.New, seal.Derive(identity, "audit-manifest/v1"))
	_, _ = h.Write(raw)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func auditArchiveAAD(lo, hi uint64) []byte {
	return []byte("audit-archive/v1|" + strconv.FormatUint(lo, 10) + "|" + strconv.FormatUint(hi, 10))
}

func auditArchiveNames(lo, hi uint64) (sealName, manifestName string) {
	base := "audit-" + strconv.FormatUint(lo, 10) + "-" + strconv.FormatUint(hi, 10)
	return base + ".seal", base + ".manifest.json"
}

// writeAuditArchive seals the exported rows and emits the .seal payload plus
// its HMAC-sealed manifest as immutable files. Existing files are never
// overwritten — a partial earlier attempt fails closed instead of masking
// corruption.
func (j *Journal) writeAuditArchive(dir string, rows []AuditRow, lo, hi uint64) (string, error) {
	enc := make([]auditArchiveRow, 0, len(rows))
	var total int64
	for _, r := range rows {
		enc = append(enc, auditArchiveRow{Seq: r.Seq, Kind: r.Kind, Principal: r.Principal, Context: r.Context, Body: r.Body})
		total += int64(len(r.Body))
	}
	payload, err := json.Marshal(enc)
	if err != nil {
		return "", err
	}
	box, err := seal.New(seal.Derive(j.identity, "audit-archive/v1"))
	if err != nil {
		return "", err
	}
	sealed, err := box.Seal(payload, auditArchiveAAD(lo, hi))
	if err != nil {
		return "", err
	}
	sealName, manName := auditArchiveNames(lo, hi)
	if err := securefs.Create(dir, sealName, sealed, 0600); err != nil {
		return "", err
	}
	payloadSum := sha256.Sum256(payload)
	encSum := sha256.Sum256(sealed)
	d := auditManifestDoc{
		Format:        1,
		Store:         base64.RawURLEncoding.EncodeToString(j.identity),
		SeqLo:         lo,
		SeqHi:         hi,
		Rows:          len(rows),
		Bytes:         total,
		Digest:        base64.RawURLEncoding.EncodeToString(payloadSum[:]),
		ArchiveSHA256: base64.RawURLEncoding.EncodeToString(encSum[:]),
	}
	d.Seal = auditManifestHMAC(d, j.identity)
	manifest, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	if err := securefs.Create(dir, manName, manifest, 0600); err != nil {
		return "", err
	}
	return filepath.Join(dir, sealName), nil
}

// auditManifest reads, parses and authenticates a manifest document.
func auditManifest(dir, name string, identity []byte) (auditManifestDoc, error) {
	raw, err := securefs.Read(dir, name, 1<<20)
	if err != nil {
		return auditManifestDoc{}, err
	}
	var d auditManifestDoc
	if err := strictjson.Decode(raw, &d); err != nil {
		return auditManifestDoc{}, errors.New("audit manifest malformed")
	}
	if d.Format != 1 || d.Rows < 0 || d.SeqHi < d.SeqLo {
		return auditManifestDoc{}, errors.New("audit manifest invalid")
	}
	if !seal.Equal(d.Store, base64.RawURLEncoding.EncodeToString(identity)) {
		return auditManifestDoc{}, errors.New("audit manifest store mismatch")
	}
	if !seal.Equal(d.Seal, auditManifestHMAC(d, identity)) {
		return auditManifestDoc{}, errors.New("audit manifest seal mismatch")
	}
	return d, nil
}

// sqlAuditRows streams the hot audit prefix. The caller holds j.mu.
func (j *Journal) sqlAuditRows() ([]AuditRow, error) {
	rows, err := j.sql.db.Query("SELECT seq,kind,principal,context,body FROM r.audit ORDER BY seq")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.Seq, &r.Kind, &r.Principal, &r.Context, &r.Body); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

// RetainAudits exports the hot audit prefix into a sealed archive and, only
// under an explicit destructive policy, prunes the verified source rows. The
// policy is always bounded: an unlimited cap is refused rather than applied.
func (j *Journal) RetainAudits() (RetainReport, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.retainAuditsLocked()
}

// retainAuditsLocked is RetainAudits with the caller already holding j.mu.
func (j *Journal) retainAuditsLocked() (RetainReport, error) {
	rep := RetainReport{}
	if j.sql == nil {
		return rep, ErrUnavailable
	}
	pol := j.retention
	if pol.HotMaxBytes <= 0 || pol.HotMaxBytes > maxAuditHotCap {
		return rep, errors.New("audit retention requires a bounded hot cap")
	}
	if pol.ArchiveDir == "" {
		return rep, errors.New("audit retention requires an archive directory")
	}
	if err := securefs.Dir(pol.ArchiveDir); err != nil {
		return rep, err
	}
	rows, err := j.sqlAuditRows()
	if err != nil {
		return rep, err
	}
	if len(rows) == 0 {
		rep.HotBytes = j.sqlAuditBytes()
		return rep, nil
	}
	lo, hi := rows[0].Seq, rows[len(rows)-1].Seq
	sealName, manName := auditArchiveNames(lo, hi)
	sealPath := filepath.Join(pol.ArchiveDir, sealName)
	manPath := filepath.Join(pol.ArchiveDir, manName)
	// An archive already covering this range is verified, never rewritten:
	// tampering must fail the pass, never self-heal. A fresh export is
	// verified before it may authorize any prune (SPEC-0087 R5.4).
	if _, err := os.Stat(sealPath); err == nil {
		if _, err := ReadAuditArchive(sealPath, manPath, j.identity); err != nil {
			return rep, fmt.Errorf("existing audit archive failed verification: %w", err)
		}
	} else {
		if _, err := j.writeAuditArchive(pol.ArchiveDir, rows, lo, hi); err != nil {
			return rep, err
		}
		if _, err := ReadAuditArchive(sealPath, manPath, j.identity); err != nil {
			return rep, fmt.Errorf("fresh audit archive failed verification: %w", err)
		}
	}
	rep.Exported = true
	rep.Archive = sealPath
	rep.SeqLo, rep.SeqHi, rep.Rows = lo, hi, len(rows)
	for _, r := range rows {
		rep.Bytes += int64(len(r.Body))
	}
	if pol.Destructive {
		// The verified archive covers [lo,hi]: the source prefix is deleted
		// in one transaction across both legs.
		tx, err := j.sql.db.Begin()
		if err != nil {
			return rep, ErrUnavailable
		}
		for _, schema := range j.sqlSchemas() {
			// #nosec G202 -- schema is a fixed "r"/"p" literal.
			if _, err := tx.Exec("DELETE FROM "+schema+".audit WHERE seq >= ? AND seq <= ?", lo, hi); err != nil {
				_ = tx.Rollback()
				j.poisoned = true
				return rep, ErrUnavailable
			}
			// #nosec G202 -- schema is a fixed "r"/"p" literal.
			if _, err := tx.Exec("REPLACE INTO "+schema+".meta(k,v) VALUES('audit_floor',?)", strconv.FormatUint(hi+1, 10)); err != nil {
				_ = tx.Rollback()
				j.poisoned = true
				return rep, ErrUnavailable
			}
		}
		if err := tx.Commit(); err != nil {
			j.poisoned = true
			return rep, ErrUnavailable
		}
		rep.Pruned = true
		j.auditBytes = j.sqlAuditBytes()
	}
	rep.HotBytes = j.sqlAuditBytes()
	rep.OverCap = rep.HotBytes > pol.HotMaxBytes
	return rep, nil
}

// ReadAuditArchive decrypts and verifies a sealed audit archive against its
// manifest: store identity, manifest seal, archive digest, plaintext digest,
// row count and the contiguous sequence range must all match. Any deviation
// fails closed and returns nothing.
func ReadAuditArchive(sealFile, manifestFile string, identity []byte) ([]AuditRow, error) {
	if len(identity) == 0 {
		return nil, errors.New("audit archive identity missing")
	}
	mdir, mname := filepath.Dir(manifestFile), filepath.Base(manifestFile)
	d, err := auditManifest(mdir, mname, identity)
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
		return nil, errors.New("audit archive digest mismatch")
	}
	box, err := seal.New(seal.Derive(identity, "audit-archive/v1"))
	if err != nil {
		return nil, err
	}
	plain, err := box.Open(enc, auditArchiveAAD(d.SeqLo, d.SeqHi))
	if err != nil {
		return nil, errors.New("audit archive decryption failed")
	}
	plainSum := sha256.Sum256(plain)
	if !seal.Equal(d.Digest, base64.RawURLEncoding.EncodeToString(plainSum[:])) {
		return nil, errors.New("audit archive payload digest mismatch")
	}
	var rows []auditArchiveRow
	if err := strictjson.Decode(plain, &rows); err != nil {
		return nil, errors.New("audit archive payload malformed")
	}
	var bytes int64
	for _, r := range rows {
		bytes += int64(len(r.Body))
	}
	if len(rows) != d.Rows || bytes != d.Bytes {
		return nil, errors.New("audit archive manifest fields inconsistent")
	}
	out := make([]AuditRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditRow{Seq: r.Seq, Kind: r.Kind, Principal: r.Principal, Context: r.Context, Body: r.Body})
	}
	return out, nil
}

// NormalizeSQLCipherSingle backfills normalized tables on an activated
// single-directory ledger.db.
func NormalizeSQLCipherSingle(dir string, key []byte, limit int64, norm NormalizeFunc) error {
	j, err := openSQLSingle(dir, key, limit)
	if err != nil {
		return err
	}
	defer func() { _ = j.Close() }()
	return normalizeSQL(j.sql.db, j.sqlSchemas(), j.seq, norm)
}
