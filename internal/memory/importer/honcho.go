package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// honchoConclusion is the verified Conclusion wire shape at the upstream v3
// API (POST /v3/workspaces/{ws}/conclusions/list → Page[Conclusion].items).
// Only the fields the import uses are decoded.
type honchoConclusion struct {
	Content    string `json:"content"`
	ObservedID string `json:"observed_id"`
	Level      string `json:"level"`
}

// ParseHoncho converts a Honcho conclusions export — the Page object
// `{items: [...]}` — into reviewable card drafts, one card per observed
// peer. The reasoning level rides each fact so review can tell what Honcho
// inferred (deductive/inductive/contradiction) from what a peer stated
// (explicit). Nothing applies until Stage+Apply (SPEC-0085 R11.5).
func ParseHoncho(path string) (Report, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var items []honchoConclusion
	var page struct {
		Items []honchoConclusion `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err == nil && page.Items != nil {
		items = page.Items
	} else if err := json.Unmarshal(body, &items); err != nil {
		return Report{}, fmt.Errorf("honcho export is neither a Page nor an array: %w", err)
	}
	var rep Report
	rep.Files = append(rep.Files, path)
	byPeer := map[string][]string{}
	seen := map[string]map[string]bool{}
	var order []string
	for _, c := range items {
		if seen[c.ObservedID] == nil {
			seen[c.ObservedID] = map[string]bool{}
			order = append(order, c.ObservedID)
		}
		fact := strings.TrimSpace(c.Content)
		if c.Level != "" && c.Level != "explicit" {
			fact += " (" + c.Level + ")"
		}
		if seen[c.ObservedID][fact] {
			continue
		}
		seen[c.ObservedID][fact] = true
		byPeer[c.ObservedID] = append(byPeer[c.ObservedID], fact)
	}
	sort.Strings(order)
	for _, peer := range order {
		rep.Cards = append(rep.Cards, CardDraft{
			Name:  "honcho-" + slugify(peer),
			Facts: byPeer[peer],
			From:  "honcho",
		})
	}
	return rep, nil
}
