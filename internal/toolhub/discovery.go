package toolhub

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
}

type discoverySelection struct {
	Candidate DiscoveryCandidate
	Owner     identity.Envelope
	Expires   time.Time
}

// discover never prepares, builds, enrolls credentials or creates a binding.
// Catalog lookup is bounded to repositories identified by the reviewed bundle.
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
			candidates = append(candidates, DiscoveryCandidate{Name: lookup.Name, Source: candidates[0].Source, Artifact: alternative.Launch.Artifact, Digest: alternative.Launch.Digest,
				Credentials: alternative.Connection.Fields, Evidence: alternative.Evidence, Reason: "Matching registry metadata; source is re-resolved and reviewed on selection", Status: status})
		}
		if len(alternatives) == 0 {
			warnings = append(warnings, "Enabled registries returned no matching exact-source alternatives; prepared source remains available")
		}
	}
	if len(candidates) == 0 {
		warnings = append(warnings, "No prepared repository matched; supply an exact GitHub URL for generic source review")
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

func (c *ControlPlane) selectedRepository(auth identity.Envelope, id string) (string, error) {
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	choice, ok := c.discoveryChoices[id]
	if !ok || !c.now().Before(choice.Expires) || choice.Owner.PrincipalID != auth.PrincipalID || choice.Owner.ContextID != auth.ContextID || choice.Owner.RuntimeID != auth.RuntimeID || choice.Owner.PolicyVersion != auth.PolicyVersion {
		return "", fmt.Errorf("%w: discovery selection expired or unavailable", ErrUnauthorized)
	}
	return choice.Candidate.Source.Repository, nil
}
