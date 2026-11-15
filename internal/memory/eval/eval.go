// Package eval is the M20.7 deterministic wave-1 gate (SPEC-0085 R7.2): a
// seeded vault plus a scripted turn sequence driven through the real capture,
// index and recall machinery — no model anywhere. Same seed, same metrics.
package eval

import (
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/vault"
)

// Result reports the fixture metrics.
type Result struct {
	RecallAt4 float64  `json:"recall_at_4"`
	Misses    []string `json:"misses,omitempty"`
}

// turn is one scripted capture; queries pair a question with the entry id a
// correct search must surface in the top-4.
type turn struct {
	id         string
	token      string // unique searchable token the query keys on
	provenance memory.Provenance
	kind       memory.Kind
	audience   memory.Audience
	day        int // day-of-month inside the fixture month
}

// script is the fixed private interactive owner turn sequence the recall
// bound is measured over.
var script = []turn{
	{id: "eval-01", token: "kepler", day: 3},
	{id: "eval-02", token: "marzipan", day: 3},
	{id: "eval-03", token: "tugboat", day: 4},
	{id: "eval-04", token: "quicksilver", day: 4},
	{id: "eval-05", token: "verbena", day: 5},
	{id: "eval-06", token: "northern", day: 5},
	{id: "eval-07", token: "calligraphy", day: 6},
	{id: "eval-08", token: "semaphore", day: 6},
	{id: "eval-09", token: "obsidian", day: 7},
	{id: "eval-10", token: "pastoral", day: 7},
	{id: "eval-b1", token: "routine-beacon", provenance: memory.ProvenanceOwner, kind: memory.KindRoutine, audience: memory.AudiencePrivate, day: 5},
	{id: "eval-u1", token: "forwarded-anchor", provenance: memory.ProvenanceUntrusted, kind: memory.KindInteractive, audience: memory.AudiencePrivate, day: 4},
	{id: "eval-g1", token: "group-lantern", provenance: memory.ProvenanceOwner, kind: memory.KindInteractive, audience: memory.AudienceGroup, day: 5},
}

func fixtureAt(day int) time.Time {
	return time.Date(2026, 4, day, 12, 0, 0, 0, time.UTC)
}

// Run seeds the vault at root (must be a fresh dir), replays the scripted
// turns through the real capture path, builds the index and measures the
// fixture metrics. Nothing outside root is touched.
func Run(root string) (Result, error) {
	var res Result
	if err := vault.Ensure(root); err != nil {
		return res, err
	}
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		return res, err
	}
	for _, s := range script {
		kind, prov := s.kind, s.provenance
		if kind == "" {
			kind = memory.KindInteractive
		}
		if prov == "" {
			prov = memory.ProvenanceOwner
		}
		aud := s.audience
		if aud == "" {
			aud = memory.AudiencePrivate
		}
		e := memory.Entry{
			ID:         s.id,
			Kind:       kind,
			Provenance: prov,
			Audience:   aud,
			SourceRef:  "eval-fixture",
			Body:       s.token + " — scripted fixture turn " + s.id,
			ObservedAt: fixtureAt(s.day),
		}
		if err := daily.Append(e, "eval"); err != nil {
			return res, err
		}
	}
	idx, _, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		return res, err
	}
	now := fixtureAt(10)
	var hits4 int
	for _, s := range script {
		hits := idx.Search(s.token, memorysearch.Options{Audience: "private", Limit: 4, Now: now})
		found := false
		for _, h := range hits {
			if h.Doc.ID == s.id {
				found = true
				break
			}
		}
		if found {
			hits4++
		} else {
			res.Misses = append(res.Misses, s.id)
		}
	}
	res.RecallAt4 = float64(hits4) / float64(len(script))
	return res, nil
}
