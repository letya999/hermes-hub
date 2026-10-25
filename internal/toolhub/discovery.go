package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"slices"
	"strings"
	"sync"
	"time"
)

type discoverySelection struct {
	Candidate api.DiscoveryCandidate
	Owner     identity.Envelope
	Expires   time.Time
}

// discoverySelections holds pending candidate picks behind their own lock;
// expiry and lookup never share the admission mutex.
type discoverySelections struct {
	mu      sync.Mutex
	choices map[string]discoverySelection
}

// discover never prepares, builds, enrolls credentials or creates a binding.
// Catalog source search is independent of the prepared bundle; exact artifact
// matches remain bound to the prepared source and commit.
func (c *ControlPlane) discover(ctx context.Context, auth identity.Envelope, query string) (map[string]any, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 256 || strings.ContainsAny(query, "\x00\r\n") {
		return nil, fmt.Errorf("%w: bounded discovery query required", api.ErrInvalid)
	}
	entries, err := api.PreparedCatalog()
	if err != nil {
		return nil, err
	}
	blocked, err := api.BlockedCatalog()
	if err != nil {
		return nil, err
	}
	var candidates []api.DiscoveryCandidate
	for _, entry := range entries {
		if !strings.Contains(strings.ToLower(entry.ID+" "+strings.Join(entry.Aliases, " ")+" "+entry.Name+" "+entry.Source.Repository), query) {
			continue
		}
		preparedID := entry.ID
		for _, alias := range entry.Aliases {
			if alias == query {
				preparedID = alias
			}
		}
		candidates = append(candidates, api.DiscoveryCandidate{Name: entry.Name, Source: entry.Source, PreparedID: preparedID, Credentials: entry.Connection.Fields,
			Reason: "Reviewed exact-source entry; generic build, Broker and live validation required", Status: "prepared", Runbook: entry.Runbook, Handoff: entry.Handoff})
	}
	for _, entry := range blocked {
		if strings.Contains(strings.ToLower(entry.ID+" "+entry.Name), query) {
			candidates = append(candidates, api.DiscoveryCandidate{Name: entry.Name, PreparedID: entry.ID, Reason: entry.Reason, Status: "blocked", Runbook: entry.Runbook, Handoff: entry.Handoff})
		}
	}
	slices.SortFunc(candidates, func(a, b api.DiscoveryCandidate) int { return strings.Compare(a.PreparedID, b.PreparedID) })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	var warnings []string
	if len(candidates) > 0 && candidates[0].Status == "prepared" && len(c.RecipeCatalogs) > 0 {
		lookup, _ := api.LookupRecipe(candidates[0].Source)
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		alternatives := api.LookupCatalogs(lookupCtx, c.RecipeCatalogs, lookup)
		slices.SortFunc(alternatives, func(a, b api.RecipeCandidate) int {
			return strings.Compare(a.Launch.Artifact+"@"+a.Launch.Digest, b.Launch.Artifact+"@"+b.Launch.Digest)
		})
		seen := map[string]bool{}
		for _, alternative := range alternatives {
			key := alternative.Launch.Artifact + "@" + alternative.Launch.Digest
			if len(candidates) == 5 {
				break
			}
			if !api.CandidateMatches(alternative, candidates[0].Source) || seen[key] {
				continue
			}
			seen[key] = true
			status := "metadata-only"
			if api.LaunchReady(alternative.Launch) {
				status = "requires-preflight"
			}
			candidate := alternative
			var selectedRecipe *api.RecipeCandidate
			if status == "requires-preflight" && alternative.Launch.Transport == api.ContainerMCP {
				selectedRecipe = &candidate
			}
			candidates = append(candidates, api.DiscoveryCandidate{Name: lookup.Name, Source: candidates[0].Source, Artifact: alternative.Launch.Artifact, Digest: alternative.Launch.Digest,
				Credentials: alternative.Connection.Fields, Evidence: alternative.Evidence, Reason: "Matching registry metadata; OCI proof is rechecked on selection", Status: status, Recipe: selectedRecipe})
		}
		if len(alternatives) == 0 {
			warnings = append(warnings, "Enabled registries returned no matching exact-source alternatives; prepared source remains available")
		}
	}
	if len(c.RecipeCatalogs) > 0 && len(candidates) < 5 {
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		found, searchWarnings := api.SearchCatalogSources(lookupCtx, c.RecipeCatalogs, query, false)
		warnings = append(warnings, searchWarnings...)
		candidates = api.AppendDiscovered(candidates, found)
		if len(candidates) < 5 {
			fallbackCtx, fallbackCancel := context.WithTimeout(ctx, 15*time.Second)
			defer fallbackCancel()
			found, searchWarnings = api.SearchCatalogSources(fallbackCtx, c.RecipeCatalogs, query, true)
			warnings = append(warnings, searchWarnings...)
			candidates = api.AppendDiscovered(candidates, found)
		}
	}
	if len(candidates) == 0 {
		warnings = append(warnings, "No registry or prepared repository matched; supply an exact GitHub URL for generic source review")
	}
	if len(warnings) > 4 {
		warnings = warnings[:4]
	}
	c.discovery.mu.Lock()
	defer c.discovery.mu.Unlock()
	if c.discovery.choices == nil {
		c.discovery.choices = map[string]discoverySelection{}
	}
	for id, choice := range c.discovery.choices {
		if !c.now().Before(choice.Expires) {
			delete(c.discovery.choices, id)
		}
	}
	if len(c.discovery.choices)+len(candidates) > 512 {
		return nil, fmt.Errorf("%w: discovery capacity reached; retry after expiry", api.ErrInvalid)
	}
	for i := range candidates {
		if candidates[i].Status == "blocked" {
			continue
		}
		candidates[i].ID = api.RandomNonce()
		c.discovery.choices[candidates[i].ID] = discoverySelection{Candidate: candidates[i], Owner: auth, Expires: c.now().Add(c.ttl())}
	}
	return map[string]any{"candidates": candidates, "warnings": warnings}, nil
}

func (c *ControlPlane) selectedCandidate(auth identity.Envelope, id string) (api.DiscoveryCandidate, error) {
	c.discovery.mu.Lock()
	defer c.discovery.mu.Unlock()
	choice, ok := c.discovery.choices[id]
	if !ok || !c.now().Before(choice.Expires) || choice.Owner.PrincipalID != auth.PrincipalID || choice.Owner.ContextID != auth.ContextID || choice.Owner.RuntimeID != auth.RuntimeID || choice.Owner.PolicyVersion != auth.PolicyVersion {
		return api.DiscoveryCandidate{}, fmt.Errorf("%w: discovery selection expired or unavailable", api.ErrUnauthorized)
	}
	return choice.Candidate, nil
}

func (c *ControlPlane) selectedRepository(auth identity.Envelope, id string) (string, error) {
	candidate, err := c.selectedCandidate(auth, id)
	if err != nil {
		return "", err
	}
	return candidate.SourceURL(), nil
}
