package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
)

// A missing acknowledgement record must never turn an acknowledged revoke into a tail.
func TestMissingLatestAnchorNeverReactivatesGrant(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			p, k := setup(t)
			r, _ := setup(t)
			a, _ := setup(t)
			rk := bytes.Repeat([]byte{9}, 32)
			j, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{"active", "revoked"} {
				if _, err = j.Append("mutation.v1", map[string]string{"status": status}); err != nil {
					t.Fatal(err)
				}
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			beforeP, err := os.ReadFile(filepath.Join(p, "ledger.bin"))
			if err != nil {
				t.Fatal(err)
			}
			beforeR, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
			if err != nil {
				t.Fatal(err)
			}
			// Works against both legacy per-commit anchors and the replacement current head.
			names := []string{"current-head", "commit-00000000000000000002"}
			if all {
				names = append(names, "commit-00000000000000000001")
			}
			for _, name := range names {
				if err = os.Remove(filepath.Join(a, name)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			opened, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
			if err == nil {
				_ = opened.Close()
				t.Error("missing head accepted; revoked authority may be reactivated")
			}
			for i, dir := range []string{p, r} {
				after, err := os.ReadFile(filepath.Join(dir, "ledger.bin"))
				before := [][]byte{beforeP, beforeR}[i]
				if err != nil || !bytes.Equal(before, after) {
					t.Error("journal changed on missing-head refusal")
				}
			}
		})
	}
}

func TestHeadMigrationRequiresMatchingQuiescedCopies(t *testing.T) {
	p, k := setup(t)
	r, _ := setup(t)
	a, _ := setup(t)
	rk := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicated(p, r, k, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"active", "revoked"} {
		if _, err = j.Append("mutation.v1", map[string]string{"status": status}); err != nil {
			t.Fatal(err)
		}
	}
	hash := j.prev
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	anchor, err := newAnchor(a, seal.Derive(k, "journal-store-key/v2"), rk)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := anchor.box.Seal([]byte("v1"), []byte("initialized"))
	if err != nil {
		t.Fatal(err)
	}
	if err = securefs.Create(a, "initialized", marker, 0600); err != nil {
		t.Fatal(err)
	}
	name := "commit-00000000000000000002"
	enc, err := anchor.box.Seal([]byte(hash), []byte(name))
	if err != nil {
		t.Fatal(err)
	}
	if err = securefs.Create(a, name, enc, 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("legacy implicitly adopted")
	}
	running, err := Open(p, k, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateHead(p, r, a, k, rk, 8<<20); err == nil {
		t.Fatal("running primary not fenced")
	}
	if err = running.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(p, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = MigrateHead(p, r, a, k, rk, 8<<20); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(filepath.Join(p, "ledger.bin"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("migration rewrote source")
	}
	m, err := anchor.marker()
	if err != nil || m == "v1" {
		t.Fatal("old binary would accept initialization marker")
	}
	opened, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if len(opened.Records()) != 2 || !bytes.Contains(opened.Records()[1].Data, []byte("revoked")) {
		t.Fatal("revocation lost during migration")
	}
}

func TestHeadCommitFailureNeverAcknowledgesTail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission fault requires an unprivileged uid")
	}
	p, k := setup(t)
	r, _ := setup(t)
	a, _ := setup(t)
	rk := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(a, 0500); err != nil {
		t.Fatal(err)
	}
	_, appendErr := j.Append("mutation.v1", map[string]string{"status": "unconfirmed-grant"})
	if err = os.Chmod(a, 0700); err != nil {
		t.Fatal(err)
	}
	if appendErr == nil || j.Healthy() {
		t.Fatal("head write failure acknowledged")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Records()) != 1 || !bytes.Contains(j.Records()[0].Data, []byte("revoked")) {
		t.Fatal("failed commit exposed unconfirmed authority")
	}
}

func TestHeadMigrationResumeAndMismatchRefusal(t *testing.T) {
	for _, kind := range []string{"resume", "missing-commit", "bad-commit", "divergent", "foreign-head"} {
		t.Run(kind, func(t *testing.T) {
			p, k := setup(t)
			r, _ := setup(t)
			a, _ := setup(t)
			rk := bytes.Repeat([]byte{9}, 32)
			j, err := OpenReplicated(p, r, k, rk, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
				t.Fatal(err)
			}
			hash := j.prev
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			anchor, err := newAnchor(a, seal.Derive(k, "journal-store-key/v2"), rk)
			if err != nil {
				t.Fatal(err)
			}
			marker, err := anchor.box.Seal([]byte("v1"), []byte("initialized"))
			if err != nil {
				t.Fatal(err)
			}
			if err = securefs.Create(a, "initialized", marker, 0600); err != nil {
				t.Fatal(err)
			}
			name := "commit-00000000000000000001"
			enc, err := anchor.box.Seal([]byte(hash), []byte(name))
			if err != nil {
				t.Fatal(err)
			}
			if kind != "missing-commit" {
				if err = securefs.Create(a, name, enc, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "resume":
				if err = anchor.writeHead(1, hash); err != nil {
					t.Fatal(err)
				}
			case "bad-commit":
				if err = os.WriteFile(filepath.Join(a, name), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "divergent":
				other, otherKey := setup(t)
				copy, err := Open(other, otherKey, 8<<20)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = copy.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
					t.Fatal(err)
				}
				if err = copy.Close(); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(other, "ledger.bin"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(p, "ledger.bin"), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-head":
				if err = anchor.writeHead(1, strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string][]byte{}
			for _, dir := range []string{p, r} {
				data, err := os.ReadFile(filepath.Join(dir, "ledger.bin"))
				if err != nil {
					t.Fatal(err)
				}
				before[dir] = data
			}
			err = MigrateHead(p, r, a, k, rk, 8<<20)
			if kind == "resume" && err != nil || kind != "resume" && err == nil {
				t.Fatalf("migration outcome: %v", err)
			}
			for dir, want := range before {
				got, err := os.ReadFile(filepath.Join(dir, "ledger.bin"))
				if err != nil || !bytes.Equal(want, got) {
					t.Fatal("migration modified source")
				}
			}
		})
	}
}

func TestCurrentHeadRejectsInvalidFormatBindingAndDigest(t *testing.T) {
	for _, kind := range []string{"format", "store", "genesis-digest", "short-digest", "bad-hex", "ahead", "mismatch", "json"} {
		t.Run(kind, func(t *testing.T) {
			p, k := setup(t)
			r, _ := setup(t)
			a, _ := setup(t)
			rk := bytes.Repeat([]byte{9}, 32)
			j, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
				t.Fatal(err)
			}
			h := currentHead{Format: 2, Store: j.anchor.store, Sequence: 1, Digest: j.prev}
			anchor := j.anchor
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "format":
				h.Format = 99
			case "store":
				h.Store = "foreign"
			case "genesis-digest":
				h.Sequence = 0
			case "short-digest":
				h.Digest = "a"
			case "bad-hex":
				h.Digest = strings.Repeat("z", 64)
			case "ahead":
				h.Sequence = 2
			case "mismatch":
				h.Digest = strings.Repeat("a", 64)
			}
			data, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "json" {
				data = []byte("invalid")
			}
			enc, err := anchor.box.Seal(data, []byte("journal-current-head/v2/"+anchor.store))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(a, "current-head"), enc, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if opened, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20); err == nil {
				_ = opened.Close()
				t.Fatal("invalid current head accepted")
			}
			after, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid head changed source")
			}
		})
	}
}

func TestHeadMigrationRejectsUnprovenLegacySet(t *testing.T) {
	for _, kind := range []string{"missing-primary", "running-recovery", "wrong-key", "marker", "malformed-name", "commit-directory", "commit-tag", "missing-store", "staged-conflict", "primary-key", "unsafe-store", "marker-version", "commit-number", "commit-short", "commit-hex", "v2-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			p, k := setup(t)
			r, _ := setup(t)
			a, _ := setup(t)
			rk := bytes.Repeat([]byte{9}, 32)
			j, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
				t.Fatal(err)
			}
			anchor, hash := j.anchor, j.prev
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(a, "current-head")); err != nil {
				t.Fatal(err)
			}
			marker, err := anchor.box.Seal([]byte("v1"), []byte("initialized"))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(a, "initialized"), marker, 0600); err != nil {
				t.Fatal(err)
			}
			name := "commit-00000000000000000001"
			enc, err := anchor.box.Seal([]byte(hash), []byte(name))
			if err != nil {
				t.Fatal(err)
			}
			if err = securefs.Create(a, name, enc, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing-primary":
				if err = os.Remove(filepath.Join(p, "ledger.bin")); err != nil {
					t.Fatal(err)
				}
			case "running-recovery":
				running, err := Open(r, rk, 8<<20)
				if err != nil {
					t.Fatal(err)
				}
				defer running.Close()
			case "wrong-key":
				rk = bytes.Repeat([]byte{8}, 32)
			case "marker":
				if err = os.WriteFile(filepath.Join(a, "initialized"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed-name":
				if err = os.WriteFile(filepath.Join(a, "commit-malformed"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "commit-directory":
				if err = os.Mkdir(filepath.Join(a, "commit-00000000000000000002"), 0700); err != nil {
					t.Fatal(err)
				}
			case "commit-tag":
				if err = os.WriteFile(filepath.Join(a, name), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-store":
				p = filepath.Join(p, "missing")

			case "primary-key":
				k = bytes.Repeat([]byte{8}, 32)
			case "unsafe-store":
				if err = os.Chmod(p, 0755); err != nil {
					t.Fatal(err)
				}
			case "marker-version":
				enc, err := anchor.box.Seal([]byte("v99"), []byte("initialized"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(a, "initialized"), enc, 0600); err != nil {
					t.Fatal(err)
				}
			case "commit-number":
				if err = os.WriteFile(filepath.Join(a, "commit-0000000000000000000x"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "commit-short", "commit-hex":
				hash := "short"
				if kind == "commit-hex" {
					hash = strings.Repeat("z", 64)
				}
				enc, err := anchor.box.Seal([]byte(hash), []byte(name))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(a, name), enc, 0600); err != nil {
					t.Fatal(err)
				}
			case "v2-mismatch":
				if err = anchor.writeHead(2, hash); err != nil {
					t.Fatal(err)
				}
				if err = anchor.markInitialized(); err != nil {
					t.Fatal(err)
				}
			case "staged-conflict":
				if err = os.WriteFile(filepath.Join(a, "current-head"), []byte("unverified"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if err = MigrateHead(p, r, a, k, rk, 8<<20); err == nil {
				t.Fatal("unproven legacy set accepted")
			}
			after, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected migration changed recovery")
			}
		})
	}
}

func TestSeedResumesOnlyVerifiedStagedHead(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			p, k := setup(t)
			r, _ := setup(t)
			a, _ := setup(t)
			rk := bytes.Repeat([]byte{9}, 32)
			j, err := Open(p, k, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
				t.Fatal(err)
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			if err = SeedReplicated(p, r, a, k, rk, 8<<20); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(a, "initialized")); err != nil {
				t.Fatal(err)
			}
			if conflict {
				if err = os.WriteFile(filepath.Join(a, "unknown"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = SeedReplicated(p, r, a, k, rk, 8<<20)
			if conflict {
				if err == nil {
					t.Fatal("ambiguous staged set accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			j, err = OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if len(j.Records()) != 1 || !bytes.Contains(j.Records()[0].Data, []byte("revoked")) {
				t.Fatal("resumed seed lost authority state")
			}
		})
	}
}

func TestDivergentCommittedPrimaryNeverTruncatesRecoveryTail(t *testing.T) {
	p, k := setup(t)
	r, _ := setup(t)
	a, _ := setup(t)
	rk := bytes.Repeat([]byte{9}, 32)
	j, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Append("mutation.v1", map[string]string{"status": "revoked"}); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(r, rk, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backup.Append("mutation.v1", map[string]string{"status": "unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	if err = backup.Close(); err != nil {
		t.Fatal(err)
	}
	alt, _ := setup(t)
	other, err := Open(alt, k, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Append("mutation.v1", map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(alt, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p, "ledger.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReplicatedAnchored(p, r, a, k, rk, 8<<20); err == nil {
		_ = opened.Close()
		t.Fatal("divergent committed prefix accepted")
	}
	after, err := os.ReadFile(filepath.Join(r, "ledger.bin"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source truncated before validating primary prefix")
	}
}
