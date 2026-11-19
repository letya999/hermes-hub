// Package identityhub owns the control-zone principal and channel registry.
package identityhub

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/sqlitedb"
)

type Registry struct {
	db  *sql.DB
	now func() time.Time
}

const applicationID = 0x48494431 // HID1; a schema version alone cannot identify an owner.

type Mapping struct {
	Space string `json:"space"`
	Human string `json:"human"`
	Agent string `json:"agent"`
}

type Challenge struct {
	ID        string
	Proof     string
	ExpiresAt time.Time
}

func Open(path string) (*Registry, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, contract.ErrInvalid
		}
		if err := verifyStoreOwner(path); err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, contract.ErrUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, contract.ErrUnavailable
	}
	db, err := sqlitedb.Open(path, 1, migrate)
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, contract.ErrUnavailable
	}
	return &Registry{db: db, now: time.Now}, nil
}

// Probe only fixed schema metadata in read-only mode before enabling WAL.
func verifyStoreOwner(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return contract.ErrInvalid
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return contract.ErrInvalid
	}
	defer db.Close()
	var owner, version int
	if err := db.QueryRow("PRAGMA application_id").Scan(&owner); err != nil || owner != applicationID {
		return contract.ErrInvalid
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		return contract.ErrInvalid
	}
	return nil
}

func (r *Registry) Close() error { return r.db.Close() }

func migrate(tx *sql.Tx) error {
	_, err := tx.Exec(`
PRAGMA application_id=1212761137;
CREATE TABLE principals (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK(kind IN ('human','agent','system')),
 owner_id TEXT REFERENCES principals(id), org_id TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL CHECK(enabled IN (0,1))
);
CREATE TABLE organizations (id TEXT PRIMARY KEY, shared_space TEXT NOT NULL UNIQUE);
CREATE TABLE memberships (principal_id TEXT NOT NULL REFERENCES principals(id), scope TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('platform-admin','org-admin','member')), PRIMARY KEY(principal_id,scope,role));
CREATE TABLE profiles (id TEXT PRIMARY KEY REFERENCES principals(id), name TEXT NOT NULL, space TEXT NOT NULL, restrictions BLOB NOT NULL);
CREATE TABLE spaces (id TEXT PRIMARY KEY, human TEXT NOT NULL REFERENCES principals(id), agent TEXT NOT NULL UNIQUE REFERENCES profiles(id));
CREATE TABLE layers (scope TEXT PRIMARY KEY, restrictions BLOB NOT NULL);
CREATE TABLE imports (id TEXT PRIMARY KEY, digest TEXT NOT NULL, mappings BLOB NOT NULL);
CREATE TABLE links (id TEXT PRIMARY KEY, channel TEXT NOT NULL, external_id TEXT NOT NULL,
 principal_id TEXT NOT NULL REFERENCES principals(id), state TEXT NOT NULL CHECK(state IN ('pending','verified','revoked','gone')),
 proof_hash BLOB, expires_at INTEGER NOT NULL, issuer TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX verified_channel ON links(channel,external_id) WHERE state='verified';
CREATE TABLE grants (id TEXT PRIMARY KEY, principal_id TEXT NOT NULL REFERENCES principals(id), scope TEXT NOT NULL,
 operation TEXT NOT NULL, issuer TEXT NOT NULL REFERENCES principals(id), reason TEXT NOT NULL,
 expires_at INTEGER NOT NULL, break_glass INTEGER NOT NULL CHECK(break_glass IN (0,1)), revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE audit (sequence INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL,
 actor TEXT NOT NULL, subject TEXT NOT NULL, reason TEXT NOT NULL);
`)
	return err
}

func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return contract.ErrNotFound
	}
	return contract.ErrUnavailable
}

func (r *Registry) Principal(ctx context.Context, id string) (contract.Principal, error) {
	var p contract.Principal
	err := r.db.QueryRowContext(ctx, "SELECT id,kind,coalesce(owner_id,''),org_id FROM principals WHERE id=? AND enabled=1", id).Scan(&p.ID, &p.Kind, &p.OwnerID, &p.OrganizationID)
	return p, storeError(err)
}

const actingProfileFrom = ` FROM principals h JOIN principals a ON a.owner_id=h.id
 JOIN profiles p ON p.id=a.id JOIN spaces s ON s.id=p.space AND s.human=h.id
 LEFT JOIN organizations o ON o.id=h.org_id
 WHERE h.id=? AND h.kind='human' AND h.enabled=1 AND a.id=? AND a.kind='agent' AND a.enabled=1
 AND a.org_id=h.org_id AND p.space=? AND (h.org_id='' OR
 (o.id IS NOT NULL AND EXISTS (SELECT 1 FROM memberships m WHERE m.principal_id=h.id AND m.scope='org:'||h.org_id AND m.role IN ('member','org-admin'))))`

func (r *Registry) CanAct(ctx context.Context, subject, actor, space string) error {
	var exists int
	err := r.db.QueryRowContext(ctx, "SELECT 1"+actingProfileFrom, subject, actor, space).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.ErrForbidden
	}
	return storeError(err)
}

func audit(ctx context.Context, tx *sql.Tx, now time.Time, action, actor, subject, reason string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit(at,action,actor,subject,reason) VALUES(?,?,?,?,?)", now.UnixNano(), action, actor, subject, reason)
	return err
}
