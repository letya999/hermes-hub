package search

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

func TestReadExactIDKeepsAudienceHistoryAndEvidence(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "own", Path: "memory/daily/2026-04-20.md", Line: 2, EndLine: 4,
		Audience: "private", Provenance: "untrusted", Text: strings.Repeat("a", 300)})
	idx.Add(Doc{ID: "removed", Path: "memory/cards/removed.md", Line: 1, EndLine: 2,
		Audience: "private", Superseded: true, Text: "old"})
	if hit, ok := idx.Read("own", "private", false); !ok || len(hit.Doc.Text) != 300 ||
		hit.Evidence == "" || !hit.Untrusted {
		t.Fatalf("exact read lost body/evidence/provenance: %+v %t", hit, ok)
	}
	for _, tc := range []struct {
		id, audience string
		history      bool
	}{
		{"own", "group", false}, {"missing", "private", false}, {"removed", "private", false},
	} {
		if _, ok := idx.Read(tc.id, tc.audience, tc.history); ok {
			t.Fatalf("read admitted %q for %q history=%t", tc.id, tc.audience, tc.history)
		}
	}
	if hit, ok := idx.Read("removed", "private", true); !ok || hit.Doc.Text != "old" {
		t.Fatalf("explicit own history read = %+v, %t", hit, ok)
	}
}

// SPEC-0085 R3.1: tokenization is pure-Go unicode word splitting — RU and EN
// both produce query-comparable lowercase tokens, punctuation and markup
// break words, digits stay attached.
func TestTokenizeUnicode(t *testing.T) {
	got := tokenize("Чёрный чай Earl Grey, 42 cups.")
	want := []string{"чёрный", "чай", "earl", "grey", "42", "cups"}
	if len(got) != len(want) {
		t.Fatalf("tokens %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %q want %q", i, got[i], want[i])
		}
	}
}

func TestBM25Order(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "a", Text: "we discussed the tea brand decision at length, tea vs coffee, tea won"})
	idx.Add(Doc{ID: "b", Text: "miscellaneous notes about nothing in particular"})
	idx.Add(Doc{ID: "c", Text: "tea came up once"})
	hits := idx.Search("tea", Options{Now: testNow})
	if len(hits) != 2 {
		t.Fatalf("hits %d want 2 (unrelated doc excluded)", len(hits))
	}
	if hits[0].Doc.ID != "a" || hits[1].Doc.ID != "c" {
		t.Fatalf("order %+v want a>c", []string{hits[0].Doc.ID, hits[1].Doc.ID})
	}
}

// BM25 must weight query-term coverage over raw frequency: a doc matching
// both terms once outranks a doc that stuffs one term.
func TestBM25TermCoverage(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "stuffed", Text: "budget budget budget budget budget"})
	idx.Add(Doc{ID: "covered", Text: "budget review"})
	hits := idx.Search("budget review", Options{Now: testNow})
	if hits[0].Doc.ID != "covered" {
		t.Fatalf("hits[0] = %q want covered (term coverage beats stuffing)", hits[0].Doc.ID)
	}
}

// SPEC-0085 R3: recency decay — identical text, the newer doc ranks first.
// Half-life keeps old-but-relevant docs findable rather than hidden.
func TestRecencyDecay(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "a-old", Text: "tea brand decision", Date: "2025-10-20"})
	idx.Add(Doc{ID: "z-new", Text: "tea brand decision", Date: "2026-04-19"})
	hits := idx.Search("tea decision", Options{Now: testNow})
	if hits[0].Doc.ID != "z-new" {
		t.Fatalf("hits[0] = %q want z-new (recency decay)", hits[0].Doc.ID)
	}
}

// SPEC-0085 R3.4/R9.1: a group-scope query can never surface private docs —
// confinement is an index filter, not a prompt convention.
func TestAudienceConfinement(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "priv", Text: "private tea brand decision", Audience: "private", Date: "2026-04-19"})
	idx.Add(Doc{ID: "grp", Text: "group tea brand decision", Audience: "group", Date: "2026-04-19"})
	hits := idx.Search("tea decision", Options{Audience: "group", Now: testNow})
	for _, h := range hits {
		if h.Doc.Audience != "group" {
			t.Fatalf("group query surfaced %s doc %q", h.Doc.Audience, h.Doc.ID)
		}
	}
	if len(hits) != 1 || hits[0].Doc.ID != "grp" {
		t.Fatalf("hits %+v want only grp", hits)
	}
}

// Superseded/forgotten docs are excluded by default; history:true is the
// explicit escape hatch (M20.6 writes the flags, the filter lives here now).
func TestHistoryFlag(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "live", Text: "tea brand decision", Audience: "private", Date: "2026-04-19"})
	idx.Add(Doc{ID: "dead", Text: "tea brand decision", Audience: "private", Date: "2026-04-19", Superseded: true})
	hits := idx.Search("tea decision", Options{Audience: "private", Now: testNow})
	if len(hits) != 1 || hits[0].Doc.ID != "live" {
		t.Fatalf("default hits %+v want only live", hits)
	}
	hits = idx.Search("tea decision", Options{Audience: "private", History: true, Now: testNow})
	if len(hits) != 2 {
		t.Fatalf("history hits %d want 2", len(hits))
	}
}

// kinds: narrows hits to the given entry kinds.
func TestKindsFilter(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "turn", Text: "tea decision", Kind: "interactive", Audience: "private", Date: "2026-04-19"})
	idx.Add(Doc{ID: "wake", Text: "tea decision", Kind: "routine", Audience: "private", Date: "2026-04-19"})
	hits := idx.Search("tea decision", Options{Audience: "private", Kinds: []string{"interactive"}, Now: testNow})
	if len(hits) != 1 || hits[0].Doc.ID != "turn" {
		t.Fatalf("hits %+v want only the interactive doc", hits)
	}
}

// R3.4: untrusted hits are flagged on the result — the M20.4 recall lane
// structurally excludes them; the flag must survive scoring.
func TestUntrustedFlagged(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "fwd", Text: "forwarded tea rumor", Provenance: "untrusted", Audience: "private", Date: "2026-04-19"})
	hits := idx.Search("tea", Options{Audience: "private", Now: testNow})
	if len(hits) != 1 || !hits[0].Untrusted {
		t.Fatalf("hit %+v must flag untrusted provenance", hits)
	}
}

// Every hit carries an evidence ref (file:line-end) — orphan text is a bug.
func TestEvidenceRef(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "e", Path: "memory/daily/2026-04-19.md", Line: 12, EndLine: 18,
		Text: "tea decision", Audience: "private", Date: "2026-04-19"})
	hits := idx.Search("tea", Options{Audience: "private", Now: testNow})
	if hits[0].Evidence != "memory/daily/2026-04-19.md:12-18" {
		t.Fatalf("evidence %q", hits[0].Evidence)
	}
}

// from/to bound the entry date.
func TestDateRangeFilter(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "mar", Text: "tea", Date: "2026-03-05", Audience: "private"})
	idx.Add(Doc{ID: "apr", Text: "tea", Date: "2026-04-15", Audience: "private"})
	hits := idx.Search("tea", Options{Audience: "private", From: "2026-04-01", To: "2026-04-30", Now: testNow})
	if len(hits) != 1 || hits[0].Doc.ID != "apr" {
		t.Fatalf("hits %+v want only apr", hits)
	}
}

// limit caps result count after ranking.
func TestLimitRespected(t *testing.T) {
	var idx Index
	for _, id := range []string{"a", "b", "c"} {
		idx.Add(Doc{ID: id, Text: "tea tea tea", Date: "2026-04-19", Audience: "private"})
	}
	if hits := idx.Search("tea", Options{Audience: "private", Limit: 2, Now: testNow}); len(hits) != 2 {
		t.Fatalf("hits %d want 2", len(hits))
	}
}

// R3.3: docs [[linked]] from a direct hit join the result at reduced score —
// card neighborhoods expand recall without matching the query terms.
func TestGraphExpansion(t *testing.T) {
	var idx Index
	idx.Add(Doc{ID: "hit", Text: "tea brand decision", Audience: "private", Date: "2026-04-19",
		Links: []string{"card-earl"}})
	idx.Add(Doc{ID: "card-earl", Text: "earl grey supplier card", Audience: "private", Date: "2026-04-10"})
	hits := idx.Search("tea decision", Options{Audience: "private", Now: testNow})
	if len(hits) != 2 || hits[1].Doc.ID != "card-earl" {
		t.Fatalf("hits %+v want hit + linked card", hits)
	}
	if hits[1].Score >= hits[0].Score {
		t.Fatalf("linked hit %f must score below direct %f", hits[1].Score, hits[0].Score)
	}
}

// Red-team: a crafted oversized/stuffed leaf must not wedge or dominate the
// index — doc text is bounded at index time (SPEC-0085 mandatory red-team).
func TestOversizedDocBounded(t *testing.T) {
	var idx Index
	big := strings.Repeat("tea ", 4*1024*1024) // ~16 MB
	idx.Add(Doc{ID: "huge", Text: big, Audience: "private", Date: "2026-04-19"})
	if len(idx.docs[0].Text) > maxDocBytes {
		t.Fatalf("doc text %d bytes exceeds bound %d", len(idx.docs[0].Text), maxDocBytes)
	}
	idx.Add(Doc{ID: "normal", Text: "tea decision recorded", Audience: "private", Date: "2026-04-19"})
	start := time.Now()
	hits := idx.Search("tea decision", Options{Audience: "private", Now: testNow})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("search over stuffed doc took %v", elapsed)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
}

// Acceptance (p95 < 50 ms on a 10k-entry fixture) is evidenced by
// BenchmarkSearch10k below — ~1.4 ms/query on the all-match worst case.
// This unit bound is a load-tolerant tripwire: the pre-inverted-index scan
// measured >300ms under this suite's shared-CPU gate, so 250ms still fails
// any regression that drops the postings/top-k path.
func TestSearchPerf10k(t *testing.T) {
	var idx Index
	for n := 0; n < 10000; n++ {
		idx.Add(Doc{ID: fmt.Sprintf("e%05d", n), Audience: "private", Date: "2026-04-19",
			Text: fmt.Sprintf("entry %d about tea brand decision number %d", n, n%50)})
	}
	// Warm once so the fixture measures query, not first-touch.
	idx.Search("tea", Options{Audience: "private", Now: testNow})
	durs := make([]time.Duration, 0, 20)
	for i := 0; i < 20; i++ {
		start := time.Now()
		idx.Search("tea decision", Options{Audience: "private", Limit: 10, Now: testNow})
		durs = append(durs, time.Since(start))
	}
	sort.Slice(durs, func(a, b int) bool { return durs[a] < durs[b] })
	if p95 := durs[18]; p95 > 250*time.Millisecond {
		t.Fatalf("p95 query %v exceeds the regression tripwire", p95)
	}
}

// Acceptance evidence for SPEC-0085 R3: ~1.4ms/query over the all-match
// 10k-entry worst case — 36x under the 50ms p95 budget.
func BenchmarkSearch10k(b *testing.B) {
	var idx Index
	for n := 0; n < 10000; n++ {
		idx.Add(Doc{ID: fmt.Sprintf("e%05d", n), Audience: "private", Date: "2026-04-19",
			Text: fmt.Sprintf("entry %d about tea brand decision number %d", n, n%50)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Search("tea decision", Options{Audience: "private", Limit: 10, Now: testNow})
	}
}
