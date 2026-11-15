//go:build linux && cgo

package journal

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	_ "github.com/mattn/go-sqlite3"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
)

var errNoCipher = errors.New("sqlcipher engine unavailable")

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS {s}.records(
		seq INTEGER PRIMARY KEY CHECK(seq > 0),
		prev TEXT NOT NULL,
		kind TEXT NOT NULL,
		body BLOB NOT NULL
	) WITHOUT ROWID`,
	`CREATE TABLE IF NOT EXISTS {s}.state(
		kind TEXT NOT NULL,
		id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		body BLOB NOT NULL,
		PRIMARY KEY (kind, id)
	) WITHOUT ROWID`,
	`CREATE TABLE IF NOT EXISTS {s}.audit(
		seq INTEGER PRIMARY KEY,
		kind TEXT NOT NULL,
		principal TEXT NOT NULL,
		context TEXT NOT NULL,
		body BLOB NOT NULL
	) WITHOUT ROWID`,
	`CREATE TABLE IF NOT EXISTS {s}.meta(
		k TEXT PRIMARY KEY,
		v TEXT NOT NULL
	) WITHOUT ROWID`,
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// sqlEnsureSchema creates the record/state/audit/meta tables idempotently —
// stores activated before normalized tables existed adopt them at open.
func sqlEnsureSchema(ex sqlExecer, schema string) error {
	for _, stmt := range sqlSchema {
		// #nosec G202 -- schema is a fixed "r"/"p" literal from call sites.
		if _, err := ex.Exec(strings.Replace(stmt, "{s}", schema, -1)); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

// sqlStore is the durable backend behind an activated Journal. In replicated
// mode both databases attach to a :memory: session — recovery as r, primary
// as p — so one COMMIT covers both files atomically.
type sqlStore struct {
	db         *sql.DB
	replicated bool
	recLock    *os.File
}

// appendSQL commits the record into the recovery schema and, in replicated
// mode, the attached primary schema inside one transaction, then advances the
// witness head. A crash between COMMIT and the head write drops the
// unacknowledged row on the next open — the same acknowledge ordering as the
// framed backend.
func (j *Journal) appendSQL(r Record, plain []byte, aux *AuxWrites) (uint64, error) {
	size := j.size + int64(len(r.Data)) + 64
	// A ledger-cap refusal is a visible admission stop, not a poisoned store:
	// history rotation can free headroom and the journal keeps serving
	// (SPEC-0087 R5.3).
	if size > j.limit {
		return 0, ErrFull
	}
	// The audit admission stop is visible, not poisonous: the commit is
	// refused before it begins so the journal stays usable and a retention
	// pass can free headroom (SPEC-0087 R5.3).
	if aux != nil && aux.Audit != nil && j.retention.HotMaxBytes > 0 &&
		j.auditBytes+int64(len(aux.Audit.Body)) > j.retention.HotMaxBytes {
		return 0, ErrFull
	}
	tx, err := j.sql.db.Begin()
	if err != nil {
		j.poisoned = true
		return 0, ErrUnavailable
	}
	if _, err := tx.Exec("INSERT INTO r.records(seq,prev,kind,body) VALUES(?,?,?,?)", r.Sequence, r.Previous, r.Kind, []byte(r.Data)); err != nil {
		_ = tx.Rollback()
		j.poisoned = true
		return 0, ErrUnavailable
	}
	if j.sql.replicated {
		if _, err := tx.Exec("INSERT INTO p.records(seq,prev,kind,body) VALUES(?,?,?,?)", r.Sequence, r.Previous, r.Kind, []byte(r.Data)); err != nil {
			_ = tx.Rollback()
			j.poisoned = true
			return 0, ErrUnavailable
		}
	}
	if aux != nil {
		// State upserts, the audit row and the marker land in the same commit
		// as the chained record — mutation, safe audit and state are atomic.
		if err := j.appendSQLAux(tx, r.Sequence, aux); err != nil {
			_ = tx.Rollback()
			j.poisoned = true
			return 0, ErrUnavailable
		}
	}
	if err := tx.Commit(); err != nil {
		j.poisoned = true
		return 0, ErrUnavailable
	}
	h := sha256.Sum256(plain)
	if j.anchor != nil {
		if err := j.anchor.writeHead(r.Sequence, hex.EncodeToString(h[:])); err != nil {
			j.poisoned = true
			return 0, ErrUnavailable
		}
	}
	j.prev = hex.EncodeToString(h[:])
	j.seq = r.Sequence
	j.size = size
	if aux != nil && aux.Audit != nil {
		j.auditBytes += int64(len(aux.Audit.Body))
	}
	return r.Sequence, nil
}

func sqlLock(dir string) (*os.File, error) {
	lock, err := securefs.Open(dir, "ledger.lock", syscall.O_RDWR|syscall.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	return lock, nil
}

// openSQLReplicated opens the activated primary+recovery pair: recovery
// attaches as r and primary as p on a :memory: session, so one COMMIT
// covers both files atomically.
func openSQLReplicated(primaryDir, recoveryDir string, anchor *recoveryAnchor, primaryKey, recoveryKey []byte, limit int64) (*Journal, error) {
	plock, err := sqlLock(primaryDir)
	if err != nil {
		return nil, err
	}
	rlock, err := sqlLock(recoveryDir)
	if err != nil {
		_ = plock.Close()
		return nil, err
	}
	db, err := sqlOpenDB()
	if err != nil {
		_ = plock.Close()
		_ = rlock.Close()
		return nil, err
	}
	fail := func(err error) (*Journal, error) {
		_ = db.Close()
		_ = plock.Close()
		_ = rlock.Close()
		return nil, err
	}
	if err := sqlAttach(db, "r", filepath.Join(recoveryDir, sqlRecoveryDB), sqlKeyPass(recoveryKey, "sqlcipher-recovery/v1")); err != nil {
		return fail(err)
	}
	if err := sqlAttach(db, "p", filepath.Join(primaryDir, sqlPrimaryDB), sqlKeyPass(primaryKey, "sqlcipher-ledger/v1")); err != nil {
		return fail(err)
	}
	if err := sqlEnsureSchema(db, "r"); err != nil {
		return fail(err)
	}
	if err := sqlEnsureSchema(db, "p"); err != nil {
		return fail(err)
	}
	recovery, _, _, floor, err := sqlReplay(db, "r")
	if err != nil {
		return fail(err)
	}
	primary, _, _, _, err := sqlReplay(db, "p")
	if err != nil {
		return fail(err)
	}
	rseq, pseq := floor-1, floor-1
	if len(recovery) > 0 {
		rseq = recovery[len(recovery)-1].Sequence
	}
	if len(primary) > 0 {
		pseq = primary[len(primary)-1].Sequence
	}
	if anchor == nil {
		return fail(ErrUnavailable)
	}
	latest, hash, err := anchor.current()
	if err != nil || latest > rseq {
		return fail(ErrUnavailable)
	}
	// A witness head below the retained floor — including a lost anchor
	// (latest=0) on a rotated store — means the archive boundary swallowed
	// acknowledged history; refuse before index math.
	if floor > 1 && latest < floor {
		return fail(ErrUnavailable)
	}
	if latest > 0 {
		// The witness head binds the acknowledged record's digest.
		plain, err := json.Marshal(recovery[latest-floor])
		if err != nil {
			return fail(err)
		}
		sum := sha256.Sum256(plain)
		if hex.EncodeToString(sum[:]) != hash {
			return fail(ErrUnavailable)
		}
	}
	upto := pseq
	if upto > latest {
		upto = latest
	}
	for i := uint64(0); i+floor <= upto; i++ {
		p, r := primary[i], recovery[i]
		if p.Sequence != r.Sequence || p.Previous != r.Previous || p.Kind != r.Kind || !bytes.Equal(p.Data, r.Data) {
			return fail(ErrUnavailable)
		}
	}
	if rseq > latest || pseq > latest || pseq < latest {
		tx, err := db.Begin()
		if err != nil {
			return fail(ErrUnavailable)
		}
		if rseq > latest {
			if _, err := tx.Exec("DELETE FROM r.records WHERE seq > ?", latest); err != nil {
				_ = tx.Rollback()
				return fail(ErrUnavailable)
			}
		}
		if pseq > latest {
			if _, err := tx.Exec("DELETE FROM p.records WHERE seq > ?", latest); err != nil {
				_ = tx.Rollback()
				return fail(ErrUnavailable)
			}
		}
		// Rows committed in the recovery journal but missing from primary are
		// still acknowledged — replay them into p inside the same transaction.
		for s := pseq + 1; s <= latest && s <= rseq; s++ {
			r := recovery[s-floor]
			if _, err := tx.Exec("INSERT INTO p.records(seq,prev,kind,body) VALUES(?,?,?,?)", r.Sequence, r.Previous, r.Kind, []byte(r.Data)); err != nil {
				_ = tx.Rollback()
				return fail(ErrUnavailable)
			}
		}
		if err := tx.Commit(); err != nil {
			return fail(ErrUnavailable)
		}
	}
	primary = recovery[:latest-floor+1]
	pprev := ""
	if latest > 0 {
		plain, err := json.Marshal(recovery[latest-floor])
		if err != nil {
			return fail(err)
		}
		sum := sha256.Sum256(plain)
		pprev = hex.EncodeToString(sum[:])
	}
	var psize int64
	for _, r := range primary {
		psize += int64(len(r.Data)) + 64
	}
	// Records are not retained: the sql backend streams them on demand.
	j := &Journal{lock: plock, limit: limit, seq: latest, prev: pprev, size: psize,
		sql: &sqlStore{db: db, replicated: true, recLock: rlock}}
	j.identity = seal.Derive(primaryKey, "journal-store-key/v2")
	j.anchor = anchor
	return j, nil
}

// openSQLSingle opens an activated single-directory ledger.db. There is no
// witness leg here: a committed row is durable by itself, the same ordering
// the framed single ledger had.
func openSQLSingle(dir string, key []byte, limit int64) (*Journal, error) {
	lock, err := sqlLock(dir)
	if err != nil {
		return nil, err
	}
	db, err := sqlOpenDB()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	fail := func(err error) (*Journal, error) {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if err := sqlAttach(db, "r", filepath.Join(dir, sqlSingleDB), sqlKeyPass(key, "sqlcipher-ledger/v1")); err != nil {
		return fail(err)
	}
	if err := sqlEnsureSchema(db, "r"); err != nil {
		return fail(err)
	}
	recs, prev, size, _, err := sqlReplay(db, "r")
	if err != nil {
		return fail(err)
	}
	var head uint64
	if len(recs) > 0 {
		head = recs[len(recs)-1].Sequence
	}
	j := &Journal{lock: lock, limit: limit, seq: head, prev: prev, size: size,
		sql: &sqlStore{db: db}}
	j.identity = seal.Derive(key, "journal-store-key/v2")
	return j, nil
}

// sqlDispatchSingle routes the single-directory open path; an unverified
// ledger.db without a legacy source refuses service.
func sqlDispatchSingle(dir string, key []byte, limit int64) (*Journal, bool, error) {
	if sqlActivated(dir) {
		j, err := openSQLSingle(dir, key, limit)
		return j, true, err
	}
	if sqlFileExists(filepath.Join(dir, sqlSingleDB)) && !sqlFileExists(filepath.Join(dir, "ledger.bin")) {
		return nil, true, ErrUnavailable
	}
	return nil, false, nil
}

// sqlDispatchReplicated routes the open path. A database file without the
// activation marker is an unverified import and refuses service.
func sqlDispatchReplicated(primaryDir, recoveryDir string, anchor *recoveryAnchor, primaryKey, recoveryKey []byte, limit int64) (*Journal, bool, error) {
	pact, ract := sqlActivated(primaryDir), sqlActivated(recoveryDir)
	if pact != ract {
		return nil, true, ErrUnavailable
	}
	if pact {
		j, err := openSQLReplicated(primaryDir, recoveryDir, anchor, primaryKey, recoveryKey, limit)
		return j, true, err
	}
	if sqlFileExists(filepath.Join(primaryDir, sqlPrimaryDB)) || sqlFileExists(filepath.Join(recoveryDir, sqlRecoveryDB)) {
		if !sqlFileExists(filepath.Join(primaryDir, "ledger.bin")) && !sqlFileExists(filepath.Join(recoveryDir, "ledger.bin")) {
			return nil, true, ErrUnavailable
		}
	}
	return nil, false, nil
}

type SQLCipherManifest struct {
	Format       int    `json:"format"`
	Records      uint64 `json:"records"`
	HeadSequence uint64 `json:"head_sequence"`
	HeadDigest   string `json:"head_digest"`
	Store        string `json:"store"`
	StoreBytes   int64  `json:"store_bytes"`
}

// SQLCipherVersion reports the linked engine's cipher version. An empty value
// means the binary was built without the sqlcipher tag or against a plain
// SQLite library; every store open refuses to run in that case.
func SQLCipherVersion() (string, error) {
	db, err := sql.Open("sqlite3", ":memory:?_pragma=key('probe')")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var v string
	if err := db.QueryRow("PRAGMA cipher_version").Scan(&v); err != nil || v == "" {
		return "", errNoCipher
	}
	return v, nil
}
