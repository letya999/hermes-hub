package agenttools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
)

// memorySearch is the M20.3 BM25+graph lane over the memory vault
// (SPEC-0085 R3). The audience comes from the gateway-stamped run-scope
// sidecar: agent write paths are workspace-confined and the governed
// vault writer rejects the runtime/ zone, so the stamp cannot be
// lifted through the declared tool surface.
func (t *Tools) memorySearch(r Input) (map[string]any, error) {
	idx, rebuilt, err := memorysearch.NewIndexer(t.VaultDir).Ensure()
	if err != nil {
		return map[string]any{"status": "unavailable", "hits": []map[string]any{}}, nil
	}
	limit := r.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	hits := idx.Search(strings.TrimSpace(r.Query), memorysearch.Options{
		Audience: runAudience(t.VaultDir),
		Kinds:    r.Kinds,
		From:     r.From,
		To:       r.To,
		History:  r.History,
		Limit:    limit,
		Now:      time.Now(),
	})
	out := []map[string]any{}
	for _, h := range hits {
		out = append(out, map[string]any{
			"id":        h.Doc.ID,
			"evidence":  h.Evidence,
			"date":      h.Doc.Date,
			"kind":      h.Doc.Kind,
			"score":     h.Score,
			"untrusted": h.Untrusted,
			"excerpt":   searchExcerpt(h.Doc.Text),
		})
	}
	status := "ok"
	if rebuilt {
		status = "degraded"
	}
	return map[string]any{"status": status, "hits": out}, nil
}

// runAudience reads the gateway-stamped scope for the active run; absence
// fails closed to "group".
func runAudience(vaultDir string) string {
	data, err := os.ReadFile(filepath.Join(vaultDir, "runtime", "run-scope", "current"))
	if err != nil {
		return "group"
	}
	var stamp struct {
		Runs map[string]struct {
			Audience string `json:"audience"`
			Expires  string `json:"expires"`
		} `json:"runs"`
	}
	if json.Unmarshal(data, &stamp) != nil || len(stamp.Runs) == 0 {
		return "group"
	}
	// Most-restrictive live audience: any active group run confines every
	// in-flight call to "group"; expiry bounds a crash-orphaned stamp to
	// one run budget.
	for _, run := range stamp.Runs {
		live := true
		if run.Expires != "" {
			if exp, err := time.Parse(time.RFC3339, run.Expires); err == nil && time.Now().After(exp) {
				live = false
			}
		}
		if live && run.Audience == "group" {
			return "group"
		}
	}
	for _, run := range stamp.Runs {
		if run.Audience == "private" {
			if run.Expires == "" {
				return "private"
			}
			if exp, err := time.Parse(time.RFC3339, run.Expires); err == nil && time.Now().Before(exp) {
				return "private"
			}
		}
	}
	return "group"
}

// searchExcerpt bounds one hit's preview — the full body stays in the
// evidence file, not the tool result budget.
func searchExcerpt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return string(runes)
}
