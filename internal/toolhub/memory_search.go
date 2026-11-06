package toolhub

import (
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// memorySearch executes the cataloged memory_search inside ToolHub: the
// verified envelope picks the vault through the server-rendered resolver —
// arguments and headers never choose a user, root or audience. The #514
// exact-run gate upstream of this handler already proved a live private
// run, so the private audience is a consequence of admission, not a file a
// caller could influence. Vault/index failures report honest unavailable;
// there is no local fallback to a shared or runtime vault.
func (g *Gateway) memorySearch(auth identity.Envelope, arguments map[string]any) (map[string]any, error) {
	unavailable := func() (map[string]any, error) {
		return map[string]any{"status": "unavailable", "hits": []map[string]any{}}, nil
	}
	if g.Vaults == nil {
		return unavailable()
	}
	u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		return unavailable()
	}
	limit := api.ArgInt(arguments, "limit")
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	history, _ := arguments["history"].(bool)
	var out map[string]any
	if err := u.Locked(func(root string) error {
		idx, rebuilt, err := memorysearch.NewIndexer(root).Ensure()
		if err != nil {
			out = map[string]any{"status": "unavailable", "hits": []map[string]any{}}
			return nil
		}
		hits := idx.Search(strings.TrimSpace(api.ArgString(arguments, "query")), memorysearch.Options{
			Audience: string(memory.AudiencePrivate),
			Kinds:    api.ArgStrings(arguments, "kinds"),
			From:     api.ArgString(arguments, "from"),
			To:       api.ArgString(arguments, "to"),
			History:  history,
			Limit:    limit,
			Now:      time.Now(),
		})
		list := []map[string]any{}
		for _, h := range hits {
			list = append(list, map[string]any{
				"id":        h.Doc.ID,
				"evidence":  h.Evidence,
				"date":      h.Doc.Date,
				"kind":      h.Doc.Kind,
				"score":     h.Score,
				"untrusted": h.Untrusted,
				"excerpt":   memorySearchExcerpt(h.Doc.Text),
			})
		}
		status := "ok"
		if rebuilt {
			status = "degraded"
		}
		out = map[string]any{"status": status, "hits": list}
		return nil
	}); err != nil {
		return unavailable()
	}
	return out, nil
}

// memorySearchExcerpt bounds one hit's preview — the full body stays in the
// evidence file, not the tool result budget.
func memorySearchExcerpt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return string(runes)
}
