package toolhub

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/sqlitedb"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// SQLite substrate for the ToolHub registry (ADR-0091/SPEC-0090): one
// transaction rewrites the store under the same stale-writer contract as
// the JSON file, so a torn write can never reach readers.
const toolhubDBSchema = 1

func isStoreDB(path string) bool { return strings.HasSuffix(path, ".db") }

// CloseDB releases the store's SQLite handle; it is a no-op for the JSON
// backend and for stores that never opened a DB. Migration and inspection
// tools need it so the file can be moved or removed afterwards.
func (s *Store) CloseDB() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func openStoreDB(path string) (*sql.DB, error) {
	db, err := sqlitedb.Open(path, toolhubDBSchema, func(tx *sql.Tx) error {
		stmts := []string{
			`CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
			`CREATE TABLE ent (collection TEXT NOT NULL, id TEXT NOT NULL, seq INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY (collection, id))`,
			`CREATE TABLE kvmap (collection TEXT NOT NULL, k TEXT NOT NULL, v TEXT NOT NULL, PRIMARY KEY (collection, k))`,
			`CREATE TABLE listrec (collection TEXT NOT NULL, seq INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY (collection, seq))`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, sqlitedb.ErrSchema) {
			return nil, fmt.Errorf("%w: store schema", api.ErrInvalid)
		}
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// dbRow is one keyed record; seq preserves canonical order so the written
// set is deterministic.
type dbRow struct {
	collection, id string
	seq            int
	body           []byte
}

func entRows[T any](collection string, items []T, idOf func(T) string) ([]dbRow, error) {
	rows := make([]dbRow, 0, len(items))
	for i, item := range items {
		body, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		rows = append(rows, dbRow{collection, idOf(item), i, body})
	}
	return rows, nil
}

func mapRows[V any](collection string, m map[string]V) ([]dbRow, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	rows := make([]dbRow, 0, len(m))
	for i, k := range keys {
		body, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		rows = append(rows, dbRow{collection, k, i, body})
	}
	return rows, nil
}

func uintRows(collection string, m map[string]uint64) []dbRow {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	rows := make([]dbRow, 0, len(m))
	for i, k := range keys {
		rows = append(rows, dbRow{collection, k, i, []byte(strconv.FormatUint(m[k], 10))})
	}
	return rows
}

// snapshotRows decomposes the canonical snapshot into ent/kvmap/listrec
// rows.
func snapshotRows(state snapshot) (ents, kvs, lists []dbRow, err error) {
	parts := []struct {
		collection string
		items      func() ([]dbRow, error)
	}{
		{"definition", func() ([]dbRow, error) {
			return entRows("definition", state.Definitions, func(d api.ToolDefinition) string { return definitionKey(d.DefinitionID, d.Version) })
		}},
		{"connection", func() ([]dbRow, error) {
			return entRows("connection", state.Connections, func(c api.Connection) string { return c.ConnectionID })
		}},
		{"credential", func() ([]dbRow, error) {
			return entRows("credential", state.Credentials, func(r api.CredentialReference) string { return r.CredentialRefID })
		}},
		{"binding", func() ([]dbRow, error) {
			return entRows("binding", state.Bindings, func(b api.ToolBinding) string { return b.ToolBindingID })
		}},
		{"workload", func() ([]dbRow, error) {
			return entRows("workload", state.Workloads, func(w api.WorkloadInstance) string { return w.WorkloadID })
		}},
		{"grant", func() ([]dbRow, error) {
			return entRows("grant", state.Grants, func(g api.Grant) string { return g.GrantID })
		}},
		{"onboarding", func() ([]dbRow, error) {
			return entRows("onboarding", state.Onboardings, func(o api.Onboarding) string { return o.OnboardingID })
		}},
		{"operation", func() ([]dbRow, error) {
			return entRows("operation", state.Operations, func(o api.Operation) string { return o.OperationID })
		}},
		{"recipe", func() ([]dbRow, error) { return mapRows("recipe", state.Recipes) }},
		{"publication", func() ([]dbRow, error) {
			return entRows("publication", state.Publications, func(p api.DefinitionPublication) string { return definitionKey(p.DefinitionID, p.Version) })
		}},
		{"shared_policy", func() ([]dbRow, error) {
			return entRows("shared_policy", state.SharedPolicies, func(p api.SharedCredentialPolicy) string { return p.PolicyID })
		}},
		{"team", func() ([]dbRow, error) {
			return entRows("team", state.Teams, func(t api.Team) string { return t.TeamID })
		}},
		{"broker_grant", func() ([]dbRow, error) {
			return entRows("broker_grant", state.BrokerGrants, func(g api.BrokerBindingGrant) string { return g.BindingID })
		}},
	}
	for _, part := range parts {
		rows, e := part.items()
		if e != nil {
			return nil, nil, nil, e
		}
		ents = append(ents, rows...)
	}
	kvs = append(kvs, uintRows("projection_revision", state.ProjectionRevisions)...)
	kvs = append(kvs, uintRows("restart_applied", state.RestartApplied)...)
	restartDigests, err := mapRows("restart_digest", state.RestartDigests)
	if err != nil {
		return nil, nil, nil, err
	}
	kvs = append(kvs, restartDigests...)
	lists, err = entRows("capability_change", state.CapabilityChanges, func(api.CapabilityChange) string { return "" })
	if err != nil {
		return nil, nil, nil, err
	}
	bindings, err := entRows("binding_change", state.BindingChanges, func(bindingChange) string { return "" })
	if err != nil {
		return nil, nil, nil, err
	}
	lists = append(lists, bindings...)
	return ents, kvs, lists, nil
}

func strictDecode(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

// readDBSnapshot reassembles the snapshot from rows; undecodable or
// unknown records are corrupt state and fail closed.
func readDBSnapshot(db *sql.DB) (snapshot, error) {
	var state snapshot
	meta := map[string]string{}
	rows, err := db.Query(`SELECT k, v FROM meta`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			_ = rows.Close()
			return state, err
		}
		meta[k] = v
	}
	if err := rows.Close(); err != nil {
		return state, err
	}
	if err := rows.Err(); err != nil {
		return state, err
	}
	if meta["initialized"] != "1" {
		return state, fmt.Errorf("%w: store not initialized", api.ErrInvalid)
	}
	schema, err := strconv.Atoi(meta["schema"])
	if err != nil || schema != api.SchemaVersion {
		return state, fmt.Errorf("%w: store schema", api.ErrInvalid)
	}
	state.Schema = schema
	state.InstallationID = meta["installation_id"]
	state.ProjectionRevisions = map[string]uint64{}
	state.RestartApplied = map[string]uint64{}
	state.RestartDigests = map[string]string{}
	state.Recipes = map[string]api.RecipeResolution{}
	fail := func(what string, err error) error {
		return fmt.Errorf("%w: %s: %v", api.ErrInvalid, what, err)
	}
	decode := func(r dbRow) error {
		switch r.collection {
		case "definition":
			var v api.ToolDefinition
			if err := strictDecode(r.body, &v); err != nil {
				return fail("definition", err)
			}
			state.Definitions = append(state.Definitions, v)
		case "connection":
			var v api.Connection
			if err := strictDecode(r.body, &v); err != nil {
				return fail("connection", err)
			}
			state.Connections = append(state.Connections, v)
		case "credential":
			var v api.CredentialReference
			if err := strictDecode(r.body, &v); err != nil {
				return fail("credential", err)
			}
			state.Credentials = append(state.Credentials, v)
		case "binding":
			var v api.ToolBinding
			if err := strictDecode(r.body, &v); err != nil {
				return fail("binding", err)
			}
			state.Bindings = append(state.Bindings, v)
		case "workload":
			var v api.WorkloadInstance
			if err := strictDecode(r.body, &v); err != nil {
				return fail("workload", err)
			}
			state.Workloads = append(state.Workloads, v)
		case "grant":
			var v api.Grant
			if err := strictDecode(r.body, &v); err != nil {
				return fail("grant", err)
			}
			state.Grants = append(state.Grants, v)
		case "onboarding":
			var v api.Onboarding
			if err := strictDecode(r.body, &v); err != nil {
				return fail("onboarding", err)
			}
			state.Onboardings = append(state.Onboardings, v)
		case "operation":
			var v api.Operation
			if err := strictDecode(r.body, &v); err != nil {
				return fail("operation", err)
			}
			state.Operations = append(state.Operations, v)
		case "recipe":
			var v api.RecipeResolution
			if err := strictDecode(r.body, &v); err != nil {
				return fail("recipe", err)
			}
			state.Recipes[r.id] = v
		case "publication":
			var v api.DefinitionPublication
			if err := strictDecode(r.body, &v); err != nil {
				return fail("publication", err)
			}
			state.Publications = append(state.Publications, v)
		case "shared_policy":
			var v api.SharedCredentialPolicy
			if err := strictDecode(r.body, &v); err != nil {
				return fail("shared policy", err)
			}
			state.SharedPolicies = append(state.SharedPolicies, v)
		case "team":
			var v api.Team
			if err := strictDecode(r.body, &v); err != nil {
				return fail("team", err)
			}
			state.Teams = append(state.Teams, v)
		case "broker_grant":
			var v api.BrokerBindingGrant
			if err := strictDecode(r.body, &v); err != nil {
				return fail("broker grant", err)
			}
			state.BrokerGrants = append(state.BrokerGrants, v)
		default:
			return fmt.Errorf("%w: store collection", api.ErrInvalid)
		}
		return nil
	}
	ents, err := db.Query(`SELECT collection, id, seq, body FROM ent ORDER BY collection, seq`)
	if err != nil {
		return state, err
	}
	for ents.Next() {
		var r dbRow
		if err := ents.Scan(&r.collection, &r.id, &r.seq, &r.body); err != nil {
			_ = ents.Close()
			return state, err
		}
		if err := decode(r); err != nil {
			_ = ents.Close()
			return state, err
		}
	}
	if err := ents.Close(); err != nil {
		return state, err
	}
	if err := ents.Err(); err != nil {
		return state, err
	}
	maps2, err := db.Query(`SELECT collection, k, v FROM kvmap ORDER BY collection, k`)
	if err != nil {
		return state, err
	}
	for maps2.Next() {
		var collection, k, v string
		if err := maps2.Scan(&collection, &k, &v); err != nil {
			_ = maps2.Close()
			return state, err
		}
		switch collection {
		case "projection_revision", "restart_applied":
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				_ = maps2.Close()
				return state, fmt.Errorf("%w: store revision", api.ErrInvalid)
			}
			if collection == "projection_revision" {
				state.ProjectionRevisions[k] = n
			} else {
				state.RestartApplied[k] = n
			}
		case "restart_digest":
			var s string
			if err := strictDecode([]byte(v), &s); err != nil {
				_ = maps2.Close()
				return state, fmt.Errorf("%w: restart digest", api.ErrInvalid)
			}
			state.RestartDigests[k] = s
		default:
			_ = maps2.Close()
			return state, fmt.Errorf("%w: store map collection", api.ErrInvalid)
		}
	}
	if err := maps2.Close(); err != nil {
		return state, err
	}
	if err := maps2.Err(); err != nil {
		return state, err
	}
	lists, err := db.Query(`SELECT collection, seq, body FROM listrec ORDER BY collection, seq`)
	if err != nil {
		return state, err
	}
	for lists.Next() {
		var collection string
		var seq int
		var body []byte
		if err := lists.Scan(&collection, &seq, &body); err != nil {
			_ = lists.Close()
			return state, err
		}
		switch collection {
		case "capability_change":
			var v api.CapabilityChange
			if err := strictDecode(body, &v); err != nil {
				_ = lists.Close()
				return state, fmt.Errorf("%w: capability change", api.ErrInvalid)
			}
			state.CapabilityChanges = append(state.CapabilityChanges, v)
		case "binding_change":
			var v bindingChange
			if err := strictDecode(body, &v); err != nil {
				_ = lists.Close()
				return state, fmt.Errorf("%w: binding change", api.ErrInvalid)
			}
			state.BindingChanges = append(state.BindingChanges, v)
		default:
			_ = lists.Close()
			return state, fmt.Errorf("%w: store list collection", api.ErrInvalid)
		}
	}
	if err := lists.Close(); err != nil {
		return state, err
	}
	if err := lists.Err(); err != nil {
		return state, err
	}
	if len(state.Recipes) == 0 {
		state.Recipes = nil
	}
	storedDigest, err := hex.DecodeString(meta["digest"])
	if err != nil {
		return state, api.ErrConflict
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return state, err
	}
	if digest := sha256.Sum256(body); !bytes.Equal(digest[:], storedDigest) {
		return state, api.ErrConflict
	}
	return state, nil
}

func loadDB(path string) (*Store, error) {
	stamp, _ := statStoreStamp(path)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := openStoreDB(path)
	if err != nil {
		return nil, err
	}
	state, err := readDBSnapshot(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store, err := storeFromSnapshot(state)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.db = db
	store.path = path
	store.savedPath, store.diskDigest = path, sha256.Sum256(body)
	store.diskStamp = stamp
	return store, nil
}

// saveDBLocked writes the store meta in one transaction; the stale-writer
// fence reads the initialized flag inside that transaction.
func (s *Store) saveDBLocked(path string) error {
	if err := api.SafeStorePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	marker := path + ".initialized"
	if err := api.SafeStorePath(marker); err != nil {
		return err
	}
	markerInfo, markerErr := os.Lstat(marker)
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return api.ErrInvalid
	}
	if markerErr == nil && !markerInfo.Mode().IsRegular() {
		return api.ErrInvalid
	}
	_, statErr := os.Stat(path)
	if os.IsNotExist(statErr) && (s.savedPath == path || markerErr == nil) {
		return api.ErrConflict
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if s.db == nil {
		db, err := openStoreDB(path)
		if err != nil {
			return err
		}
		s.db = db
	}
	state, err := s.buildSnapshotLocked()
	if err != nil {
		return err
	}
	ents, kvs, lists, err := snapshotRows(state)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	counter, err := s.storageCounter(filepath.Dir(path))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	err = counter.Write(path, int64(len(body)), true, func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var initialized string
		metaErr := tx.QueryRow(`SELECT v FROM meta WHERE k = 'initialized'`).Scan(&initialized)
		if metaErr != nil && !errors.Is(metaErr, sql.ErrNoRows) {
			return metaErr
		}
		if errors.Is(metaErr, sql.ErrNoRows) {
			var rowCount int
			if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM ent) + (SELECT COUNT(*) FROM kvmap) + (SELECT COUNT(*) FROM listrec)`).Scan(&rowCount); err != nil {
				return err
			}
			if rowCount > 0 || s.savedPath == path {
				return api.ErrConflict
			}
		} else {
			var storedDigest string
			if err := tx.QueryRow(`SELECT v FROM meta WHERE k = 'digest'`).Scan(&storedDigest); err != nil {
				return api.ErrConflict
			}
			stored, err := hex.DecodeString(storedDigest)
			if initialized != "1" || s.savedPath != path || err != nil || !bytes.Equal(stored, s.diskDigest[:]) {
				return api.ErrConflict
			}
		}
		for _, table := range []string{"ent", "kvmap", "listrec"} {
			if _, err := tx.Exec(`DELETE FROM ` + table); err != nil { // #nosec G202 -- fixed table name list
				return err
			}
		}
		for _, r := range ents {
			if _, err := tx.Exec(`INSERT INTO ent (collection, id, seq, body) VALUES (?, ?, ?, ?)`, r.collection, r.id, r.seq, r.body); err != nil {
				return err
			}
		}
		for _, r := range kvs {
			if _, err := tx.Exec(`INSERT INTO kvmap (collection, k, v) VALUES (?, ?, ?)`, r.collection, r.id, string(r.body)); err != nil {
				return err
			}
		}
		for _, r := range lists {
			if _, err := tx.Exec(`INSERT INTO listrec (collection, seq, body) VALUES (?, ?, ?)`, r.collection, r.seq, r.body); err != nil {
				return err
			}
		}
		for k, v := range map[string]string{
			"initialized":     "1",
			"schema":          strconv.Itoa(api.SchemaVersion),
			"installation_id": state.InstallationID,
			"digest":          hex.EncodeToString(digest[:]),
		} {
			if _, err := tx.Exec(`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	if markerErr != nil {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G304 -- marker path was symlink-checked under the store lock
		if err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
	}
	s.savedPath, s.diskDigest = path, sha256.Sum256(body)
	s.diskStamp, _ = statStoreStamp(path)
	return nil
}
