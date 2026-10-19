package api

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// corpusFixture records one real target's claims exactly as its upstream
// metadata states them; expect_digest pins the resolved recipe.
type corpusFixture struct {
	Target       string        `json:"target"`
	Subject      RecipeSubject `json:"subject"`
	Provenance   []string      `json:"provenance"`
	Entries      []LedgerEntry `json:"entries"`
	ExpectDigest string        `json:"expect_digest"`
}

func shuffleEntries(entries []LedgerEntry) []LedgerEntry {
	shuffled := append([]LedgerEntry(nil), entries...)
	for i := len(shuffled) - 1; i > 0; i-- {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		j := int(n.Int64())
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	return shuffled
}

func TestGoldenCorpus(t *testing.T) {
	paths, err := filepath.Glob("testdata/corpus/*.json")
	if err != nil || len(paths) < 20 {
		t.Fatalf("golden corpus needs >=20 real targets, found %d", len(paths))
	}
	update := os.Getenv("GOLDEN_UPDATE") == "1"
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture corpusFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Run(fixture.Target, func(t *testing.T) {
			first, err := ResolveLedger(fixture.Subject, fixture.Entries)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			digest := first.Digest()
			for i := 0; i < 100; i++ {
				replay, err := ResolveLedger(fixture.Subject, shuffleEntries(fixture.Entries))
				if err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				if again := replay.Digest(); again != digest {
					t.Fatalf("run %d: shuffled entries resolved to a different digest %q (want %q)", i, again, digest)
				}
			}
			if update {
				fixture.ExpectDigest = digest
				body, _ := json.MarshalIndent(fixture, "", "  ")
				if err := os.WriteFile(path, append(body, '\n'), 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			if fixture.ExpectDigest == "" {
				t.Fatalf("unpinned corpus entry; run with GOLDEN_UPDATE=1 to record digests")
			}
			if digest != fixture.ExpectDigest {
				t.Fatalf("digest drifted: %q (want %q)", digest, fixture.ExpectDigest)
			}
		})
	}
}
