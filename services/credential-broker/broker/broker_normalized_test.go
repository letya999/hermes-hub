//go:build linux && cgo

package broker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/materialize"
	"github.com/letya999/credential-broker/provider"
)

// sqlFixture builds a fixture whose journal is an activated replicated
// SQLCipher store with normalized tables armed. restartSql reopens the same
// encrypted pair through the full witness path.
type sqlFixture struct {
	*fixture
	primary, recovery, anchor string
	pk, rk                    []byte
}

func newSqlFixture(t *testing.T, cs ...contract.Contract) *sqlFixture {
	t.Helper()
	if _, err := journal.SQLCipherVersion(); err != nil {
		t.Skip("sqlcipher engine unavailable in this build")
	}
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	if len(cs) == 0 {
		cs = []contract.Contract{plainContract()}
	}
	primary := filepath.Join(root, "state")
	recovery := filepath.Join(root, "recovery", "journal")
	anchor := filepath.Join(root, "anchor")
	for _, d := range []string{primary, recovery, anchor} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pk, rk := bytes.Repeat([]byte{6}, 32), bytes.Repeat([]byte{9}, 32)
	j, err := journal.OpenReplicatedAnchored(primary, recovery, anchor, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ImportSQLCipher(primary, recovery, anchor, pk, rk, 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := journal.ActivateSQLCipher(primary, recovery, anchor, pk, rk); err != nil {
		t.Fatal(err)
	}
	// The store is empty at this point; arming the marker makes live appends
	// carry normalized-table writes from the first commit on.
	if err := journal.NormalizeSQLCipher(primary, recovery, anchor, pk, rk, 8<<20, NormalizeRecord); err != nil {
		t.Fatal(err)
	}
	j, err = journal.OpenReplicatedAnchored(primary, recovery, anchor, pk, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	p, err := provider.NewLocal("local", filepath.Join(root, "secrets"), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := materialize.New(filepath.Join(root, "runtime"), false)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{at: time.Now().UTC()}
	cfg := Config{Contracts: cs, Providers: provider.Registry{"local": p}, Journal: j, Materializer: m, PublicOrigin: "https://credentials.example", SessionKey: bytes.Repeat([]byte{5}, 32), Now: clock.now, OAuth: map[string]*OAuthProvider{}, ExternalAliases: map[string]ExternalAlias{}}
	f := &fixture{cfg: cfg, root: root, clock: clock, t: t}
	f.start()
	sf := &sqlFixture{fixture: f, primary: primary, recovery: recovery, anchor: anchor, pk: pk, rk: rk}
	t.Cleanup(func() {
		if f.b != nil {
			_ = f.b.Close()
		}
	})
	return sf
}

func (f *sqlFixture) restart() {
	f.t.Helper()
	if err := f.b.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.b = nil
	j, err := journal.OpenReplicatedAnchored(f.primary, f.recovery, f.anchor, f.pk, f.rk, 8<<20)
	if err != nil {
		f.t.Fatal(err)
	}
	m, err := materialize.New(filepath.Join(f.root, "runtime"), false)
	if err != nil {
		f.t.Fatal(err)
	}
	f.cfg.Journal = j
	f.cfg.Materializer = m
	f.start()
}

// Restart preserves the revoked credential from state rows and drops leases;
// audit pages come from the table with scope isolation intact.
func TestBrokerNormalizedStoreRestart(t *testing.T) {
	f := newSqlFixture(t)
	c := f.enroll(alice, "pat", "github")
	c2 := f.enroll(alice, "pat", "gh2")
	g, agent := f.grant(alice, c, "pat", "binding1", "workload1")
	_ = f.lease(agent, g)
	if err := f.b.RevokeCredential(alice, c.ID); err != nil {
		t.Fatal(err)
	}
	if !f.cfg.Journal.Normalized() {
		rows, rerr := f.cfg.Journal.StateRows()
		t.Fatalf("marker lost during live commits: stateRows=%d err=%v records=%d",
			len(rows), rerr, len(f.cfg.Journal.Records()))
	}
	f.restart()
	if !f.cfg.Journal.Normalized() {
		t.Fatal("store lost its normalized marker across restart")
	}
	if len(f.b.events) != 0 {
		t.Fatal("events RAM clone rebuilt on a normalized store")
	}
	rows, err := f.cfg.Journal.StateRows()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("state table empty after live commits")
	}
	if len(f.b.sessions) == 0 {
		t.Fatal("sessions lost: multi-object commits must project every field")
	}
	cred, err := f.b.GetCredential(alice, c.ID)
	if err != nil {
		t.Fatal("credential lost across normalized restart:", err)
	}
	if cred.Status != "revoked" {
		t.Fatalf("revoke not preserved across restart: %s", cred.Status)
	}
	// c2 was never revoked — its state row exists only if the multi-object
	// Submit mutation projected every field, not just the first.
	if _, err := f.b.GetCredential(alice, c2.ID); err != nil {
		t.Fatal("unrevoked credential lost across normalized restart:", err)
	}
	if len(f.b.leases) != 0 {
		t.Fatalf("%d leases survived restart", len(f.b.leases))
	}
	evs := f.b.Events(alice, 0, 100)
	if len(evs.Events) == 0 {
		t.Fatal("normalized store served no audit events")
	}
	seenStarted := false
	for _, e := range evs.Events {
		if e.Kind == "broker.started" {
			seenStarted = true
		}
		if e.Sequence == 0 {
			t.Fatal("audit page returned an unstamped event")
		}
	}
	if !seenStarted {
		t.Fatal("broker.started missing from audit table")
	}
	for _, e := range f.b.Events(bob, 0, 100).Events {
		if e.PrincipalID == "alice" {
			t.Fatal("foreign scope received alice's events")
		}
	}
}

// Normalized rows are DTO projections like the journal records were: no secret
// material may reach state or audit bodies.
func TestNoSecretMaterialInStateAuditRows(t *testing.T) {
	f := newSqlFixture(t)
	c := f.enroll(alice, "pat", "github")
	g, agent := f.grant(alice, c, "pat", "binding1", "workload1")
	_ = f.lease(agent, g)
	recs := f.b.cfg.Journal.Records()
	if len(recs) == 0 {
		t.Fatal("no records in journal")
	}
	sawState, sawAudit := false, false
	for _, rec := range recs {
		states, auditRow, err := NormalizeRecord(rec)
		if err != nil {
			t.Fatalf("NormalizeRecord seq=%d: %v", rec.Sequence, err)
		}
		for _, s := range states {
			sawState = true
			if bytes.Contains(s.Body, []byte("SENTINEL_NOT_A_REAL_SECRET")) {
				t.Fatalf("state row %s/%s seq=%d leaks secret material", s.Kind, s.ID, rec.Sequence)
			}
		}
		if auditRow != nil {
			sawAudit = true
			if bytes.Contains(auditRow.Body, []byte("SENTINEL_NOT_A_REAL_SECRET")) {
				t.Fatalf("audit row seq=%d leaks secret material", rec.Sequence)
			}
		}
	}
	if !sawState || !sawAudit {
		t.Fatal("fixture produced no state/audit rows to inspect")
	}
}
