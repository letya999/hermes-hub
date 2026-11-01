//go:build linux && cgo

package journal

import (
	"database/sql"
	"strconv"

	"github.com/letya999/credential-broker/internal/seal"
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
	if limit < 1 || limit > 1000 {
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
