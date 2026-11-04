// Package search is the pure-Go BM25 + link-graph retrieval lane over the
// memory vault (SPEC-0085 R3): no model calls, no vector DB, scope-confined.
package search

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// tokenize splits on unicode letter/digit boundaries and lowercases — decent
// RU+EN recall without a stemmer dependency (M20.3 v1).
func tokenize(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// Doc is one searchable unit — a daily-leaf entry, a card, or a summary file.
type Doc struct {
	ID         string
	Path       string // vault-relative evidence file
	Line       int    // evidence block start (1-based)
	EndLine    int    // evidence block end (inclusive)
	Date       string // YYYY-MM-DD for recency decay
	Audience   string // private/group — confinement filter
	Kind       string // entry kind; cards/summaries carry their own kinds
	Provenance string // owner/agent/untrusted/system
	Superseded bool   // soft-forgotten/superseded — needs history:true
	Links      []string
	Text       string
	day        time.Time // parsed once at Add; recency is arithmetic per hit
}

// Options bounds a query — audience/scope confinement lives here, never in
// query text.
type Options struct {
	Audience string
	Kinds    []string
	From     string // YYYY-MM-DD lower bound on doc date
	To       string // YYYY-MM-DD upper bound (inclusive)
	History  bool
	Limit    int
	Now      time.Time
}

// Hit is one scored result with its evidence ref.
type Hit struct {
	Doc       Doc
	Score     float64
	Evidence  string // path:line-end — never empty for a real hit
	Untrusted bool   // provenance=untrusted; excluded from lane-1 recall
}

// posting is one term occurrence in one doc — the inverted list that lets a
// query touch only matching docs instead of scanning the corpus.
type posting struct {
	di int
	tf float64
}

// Index is the in-memory BM25 index built lazily by the Indexer.
type Index struct {
	docs     []Doc
	dls      []int // per-doc total term occurrences
	postings map[string][]posting
	byID     map[string]int
	avgdl    float64
	total    int
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// maxDocBytes bounds one doc's indexed text — a crafted or corrupt oversized
// leaf cannot wedge index memory or query latency.
const maxDocBytes = 256 << 10

func (i *Index) Add(d Doc) {
	if len(d.Text) > maxDocBytes {
		d.Text = d.Text[:maxDocBytes]
	}
	d.day, _ = time.Parse("2006-01-02", d.Date)
	tf := map[string]int{}
	for _, tok := range tokenize(d.Text) {
		tf[tok]++
	}
	di := len(i.docs)
	if i.postings == nil {
		i.postings = map[string][]posting{}
	}
	for tok, c := range tf {
		i.postings[tok] = append(i.postings[tok], posting{di: di, tf: float64(c)})
	}
	dl := 0
	for _, c := range tf {
		dl += c
	}
	i.dls = append(i.dls, dl)
	if i.byID == nil {
		i.byID = map[string]int{}
	}
	i.byID[d.ID] = di
	i.total += dl // term occurrences for avgdl
	i.docs = append(i.docs, d)
	i.avgdl = float64(i.total) / float64(len(i.docs))
}

// bm25 scores term coverage with IDF weighting and tf saturation — a doc that
// mentions every query term once outranks one that stuffs a single term.
func (i *Index) Search(query string, opts Options) []Hit {
	qtoks := tokenize(query)
	n := float64(len(i.docs))
	// Inverted-index scoring: only docs holding a query term are touched.
	scores := make([]float64, len(i.docs))
	var touched []int
	for _, tok := range qtoks {
		list := i.postings[tok]
		if len(list) == 0 {
			continue
		}
		df := float64(len(list))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, p := range list {
			if scores[p.di] == 0 {
				touched = append(touched, p.di)
			}
			tf := p.tf
			scores[p.di] += idf * (tf * (bm25K1 + 1)) / (tf + bm25K1*(1-bm25B+bm25B*float64(i.dls[p.di])/i.avgdl))
		}
	}
	// Score into (docIndex, score) pairs — Doc materialization happens only
	// for the kept top-k so a wide match does not copy the whole corpus.
	type scored struct {
		di    int
		score float64
	}
	less := func(a, b scored) bool {
		if a.score == b.score {
			return i.docs[a.di].ID < i.docs[b.di].ID
		}
		return a.score > b.score
	}
	var pairs []scored
	if opts.Limit > 0 {
		pairs = make([]scored, 0, opts.Limit+1)
	} else {
		pairs = make([]scored, 0, 64)
	}
	for _, di := range touched {
		d := i.docs[di]
		if opts.Audience != "" && d.Audience != opts.Audience {
			continue
		}
		if d.Superseded && !opts.History {
			continue
		}
		if len(opts.Kinds) > 0 {
			ok := false
			for _, k := range opts.Kinds {
				if d.Kind == k {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if opts.From != "" && d.Date != "" && d.Date < opts.From {
			continue
		}
		if opts.To != "" && d.Date != "" && d.Date > opts.To {
			continue
		}
		cand := scored{di: di, score: scores[di] * recency(d.day, opts.Now)}
		// Bounded insert: a capped query keeps a sorted top-k — sorting the
		// whole match set on every call is the dominant cost at 10k docs.
		if opts.Limit > 0 {
			if len(pairs) == opts.Limit && !less(cand, pairs[len(pairs)-1]) {
				continue
			}
			pos := sort.Search(len(pairs), func(j int) bool { return less(cand, pairs[j]) })
			pairs = append(pairs, scored{})
			copy(pairs[pos+1:], pairs[pos:])
			pairs[pos] = cand
			if len(pairs) > opts.Limit {
				pairs = pairs[:opts.Limit]
			}
			continue
		}
		pairs = append(pairs, cand)
	}
	if opts.Limit <= 0 {
		sort.Slice(pairs, func(a, b int) bool { return less(pairs[a], pairs[b]) })
	}
	hits := make([]Hit, 0, len(pairs)*2)
	for _, p := range pairs {
		d := i.docs[p.di]
		hits = append(hits, Hit{
			Doc:       d,
			Score:     p.score,
			Evidence:  evidenceRef(d),
			Untrusted: d.Provenance == "untrusted",
		})
	}
	hits = i.expand(hits)
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score == hits[b].Score {
			return hits[a].Doc.ID < hits[b].Doc.ID
		}
		return hits[a].Score > hits[b].Score
	})
	return hits
}

// evidenceRef builds the path:line-end ref without fmt — it is emitted once
// per hit on the hot path.
func evidenceRef(d Doc) string {
	return d.Path + ":" + strconv.Itoa(d.Line) + "-" + strconv.Itoa(d.EndLine)
}

// halfLifeDays softens ranking toward fresh entries without hiding old ones.
const halfLifeDays = 30.0

func recency(day time.Time, now time.Time) float64 {
	if day.IsZero() || now.Before(day) {
		return 1
	}
	age := now.Sub(day).Hours() / 24
	return math.Exp2(-age / halfLifeDays)
}

// graphBoost is the score multiplier for one-hop [[link]] neighbors.
const graphBoost = 0.5

// expand adds one-hop linked docs at reduced score. A neighbor already in the
// result keeps its own (direct) score.
func (i *Index) expand(hits []Hit) []Hit {
	in := map[string]bool{}
	for _, h := range hits {
		in[h.Doc.ID] = true
	}
	var extra []Hit
	for _, h := range hits {
		for _, link := range h.Doc.Links {
			if in[link] {
				continue
			}
			di, found := i.byID[link]
			if found && h.Doc.Audience == i.docs[di].Audience {
				in[link] = true
				extra = append(extra, Hit{
					Doc:       i.docs[di],
					Score:     h.Score * graphBoost,
					Evidence:  evidenceRef(i.docs[di]),
					Untrusted: i.docs[di].Provenance == "untrusted",
				})
			}
		}
	}
	return append(hits, extra...)
}
