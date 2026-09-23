package toolhub

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

type DiscoveryCandidate struct {
	ID          string            `json:"candidate_id"`
	Name        string            `json:"name"`
	Source      ArtifactSource    `json:"source"`
	PreparedID  string            `json:"prepared_id,omitempty"`
	Artifact    string            `json:"artifact,omitempty"`
	Digest      string            `json:"digest,omitempty"`
	Credentials []ConnectionField `json:"credentials,omitempty"`
	Evidence    []RecipeEvidence  `json:"evidence,omitempty"`
	Reason      string            `json:"reason"`
	Status      string            `json:"status"`
	Runbook     string            `json:"runbook,omitempty"`
	Handoff     string            `json:"handoff,omitempty"`
	recipe      *RecipeCandidate
}

type discoverySelection struct {
	Candidate DiscoveryCandidate
	Owner     identity.Envelope
	Expires   time.Time
}

// SourceSearchCatalog may suggest repository URLs without installation
// authority. prepare_source still pins and reviews the selected repository.
type SourceSearchCatalog interface {
	SearchSources(context.Context, string) ([]DiscoveryCandidate, error)
}

func searchCatalogSources(ctx context.Context, catalogs []RecipeCatalog, query string) []DiscoveryCandidate {
	results := make([][]DiscoveryCandidate, len(catalogs))
	var pending sync.WaitGroup
	for i, catalog := range catalogs {
		searcher, ok := catalog.(SourceSearchCatalog)
		if !ok {
			continue
		}
		pending.Add(1)
		go func(i int, searcher SourceSearchCatalog) {
			defer pending.Done()
			if found, err := searcher.SearchSources(ctx, query); err == nil && len(found) <= 32 {
				results[i] = found
			}
		}(i, searcher)
	}
	pending.Wait()
	var found []DiscoveryCandidate
	for _, result := range results {
		found = append(found, result...)
	}
	slices.SortFunc(found, func(a, b DiscoveryCandidate) int {
		return strings.Compare(a.Source.Repository+"@"+a.Source.CommitSHA, b.Source.Repository+"@"+b.Source.CommitSHA)
	})
	return found
}

// discover never prepares, builds, enrolls credentials or creates a binding.
// Catalog source search is independent of the prepared bundle; exact artifact
// matches remain bound to the prepared source and commit.
func (c *ControlPlane) discover(ctx context.Context, auth identity.Envelope, query string) (map[string]any, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 256 || strings.ContainsAny(query, "\x00\r\n") {
		return nil, fmt.Errorf("%w: bounded discovery query required", ErrInvalid)
	}
	entries, err := PreparedCatalog()
	if err != nil {
		return nil, err
	}
	var candidates []DiscoveryCandidate
	for _, entry := range entries {
		if !strings.Contains(strings.ToLower(entry.ID+" "+entry.Name+" "+entry.Source.Repository), query) {
			continue
		}
		candidates = append(candidates, DiscoveryCandidate{Name: entry.Name, Source: entry.Source, PreparedID: entry.ID, Credentials: entry.Connection.Fields,
			Reason: "Reviewed exact-source entry; generic build, Broker and live validation required", Status: "prepared", Runbook: entry.Runbook, Handoff: entry.Handoff})
	}
	slices.SortFunc(candidates, func(a, b DiscoveryCandidate) int { return strings.Compare(a.PreparedID, b.PreparedID) })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	var warnings []string
	if len(candidates) > 0 && len(c.RecipeCatalogs) > 0 {
		lookup, _ := recipeLookup(candidates[0].Source)
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		alternatives := lookupCatalogs(lookupCtx, c.RecipeCatalogs, lookup)
		slices.SortFunc(alternatives, func(a, b RecipeCandidate) int {
			return strings.Compare(a.Launch.Artifact+"@"+a.Launch.Digest, b.Launch.Artifact+"@"+b.Launch.Digest)
		})
		seen := map[string]bool{}
		for _, alternative := range alternatives {
			key := alternative.Launch.Artifact + "@" + alternative.Launch.Digest
			if len(candidates) == 5 {
				break
			}
			if !candidateMatches(alternative, candidates[0].Source) || seen[key] {
				continue
			}
			seen[key] = true
			status := "metadata-only"
			if launchReady(alternative.Launch) {
				status = "requires-preflight"
			}
			candidate := alternative
			var selectedRecipe *RecipeCandidate
			if status == "requires-preflight" && alternative.Launch.Transport == ContainerMCP {
				selectedRecipe = &candidate
			}
			candidates = append(candidates, DiscoveryCandidate{Name: lookup.Name, Source: candidates[0].Source, Artifact: alternative.Launch.Artifact, Digest: alternative.Launch.Digest,
				Credentials: alternative.Connection.Fields, Evidence: alternative.Evidence, Reason: "Matching registry metadata; OCI proof is rechecked on selection", Status: status, recipe: selectedRecipe})
		}
		if len(alternatives) == 0 {
			warnings = append(warnings, "Enabled registries returned no matching exact-source alternatives; prepared source remains available")
		}
	}
	if len(c.RecipeCatalogs) > 0 && len(candidates) < 5 {
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		seen := map[string]bool{}
		for _, candidate := range candidates {
			seen[strings.ToLower(candidate.Source.Repository)] = true
		}
		for _, candidate := range searchCatalogSources(lookupCtx, c.RecipeCatalogs, query) {
			if len(candidates) == 5 {
				break
			}
			if candidate.Name == "" || len(candidate.Name) > 120 || strings.ContainsAny(candidate.Name, "\x00\r\n") {
				continue
			}
			if candidate.Source.CommitSHA == "" {
				if _, err := parseGitHubRepository(candidate.Source.Repository); err != nil {
					continue
				}
			} else if _, err := candidate.Source.ArchiveURL(); err != nil {
				continue
			}
			key := strings.ToLower(candidate.Source.Repository)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		warnings = append(warnings, "No registry or prepared repository matched; supply an exact GitHub URL for generic source review")
	}
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	if c.discoveryChoices == nil {
		c.discoveryChoices = map[string]discoverySelection{}
	}
	for id, choice := range c.discoveryChoices {
		if !c.now().Before(choice.Expires) {
			delete(c.discoveryChoices, id)
		}
	}
	if len(c.discoveryChoices)+len(candidates) > 512 {
		return nil, fmt.Errorf("%w: discovery capacity reached; retry after expiry", ErrInvalid)
	}
	for i := range candidates {
		candidates[i].ID = randomNonce()
		c.discoveryChoices[candidates[i].ID] = discoverySelection{Candidate: candidates[i], Owner: auth, Expires: c.now().Add(c.ttl())}
	}
	return map[string]any{"candidates": candidates, "warnings": warnings}, nil
}

func (c *ControlPlane) selectedCandidate(auth identity.Envelope, id string) (DiscoveryCandidate, error) {
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	choice, ok := c.discoveryChoices[id]
	if !ok || !c.now().Before(choice.Expires) || choice.Owner.PrincipalID != auth.PrincipalID || choice.Owner.ContextID != auth.ContextID || choice.Owner.RuntimeID != auth.RuntimeID || choice.Owner.PolicyVersion != auth.PolicyVersion {
		return DiscoveryCandidate{}, fmt.Errorf("%w: discovery selection expired or unavailable", ErrUnauthorized)
	}
	return choice.Candidate, nil
}

func (candidate DiscoveryCandidate) sourceURL() string {
	source := candidate.Source
	if source.CommitSHA == "" {
		return source.Repository
	}
	if source.Subfolder != "" {
		return source.Repository + "/tree/" + source.CommitSHA + "/" + source.Subfolder
	}
	return source.Repository + "/commit/" + source.CommitSHA
}

func (c *ControlPlane) selectedRepository(auth identity.Envelope, id string) (string, error) {
	candidate, err := c.selectedCandidate(auth, id)
	if err != nil {
		return "", err
	}
	return candidate.sourceURL(), nil
}
