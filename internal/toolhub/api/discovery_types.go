package api

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	Recipe      *RecipeCandidate  `json:"-"`
}

// SourceSearchCatalog may suggest repository URLs without installation
// authority. prepare_source still pins and reviews the selected repository.
type SourceSearchCatalog interface {
	SearchSources(context.Context, string) ([]DiscoveryCandidate, error)
}

func SearchCatalogSources(ctx context.Context, catalogs []RecipeCatalog, query string, fallback bool) ([]DiscoveryCandidate, []string) {
	results := make([][]DiscoveryCandidate, len(catalogs))
	warnings := make([]string, len(catalogs))
	var pending sync.WaitGroup
	for i, catalog := range catalogs {
		_, isFallback := catalog.(GitHubSearchCatalog)
		if isFallback != fallback {
			continue
		}
		searcher, ok := catalog.(SourceSearchCatalog)
		if !ok {
			continue
		}
		pending.Add(1)
		go func(i int, searcher SourceSearchCatalog) {
			defer pending.Done()
			if found, err := searcher.SearchSources(ctx, query); err == nil && len(found) <= 32 {
				results[i] = found
			} else {
				warnings[i] = fmt.Sprintf("%T search unavailable", catalogs[i])
			}
		}(i, searcher)
	}
	pending.Wait()
	var found []DiscoveryCandidate
	for _, result := range results {
		found = append(found, result...)
	}
	slices.SortFunc(found, func(a, b DiscoveryCandidate) int {
		if rank := discoverySourceRank(a) - discoverySourceRank(b); rank != 0 {
			return rank
		}
		return strings.Compare(a.Source.Repository+"@"+a.Source.CommitSHA, b.Source.Repository+"@"+b.Source.CommitSHA)
	})
	return found, slices.DeleteFunc(warnings, func(warning string) bool { return warning == "" })
}

func discoverySourceRank(candidate DiscoveryCandidate) int {
	if len(candidate.Evidence) == 0 {
		return 9
	}
	switch candidate.Evidence[0].Source {
	case "mcp-registry":
		return 0
	case "toolhive":
		return 1
	case "docker-mcp":
		return 2
	case "smithery":
		return 3
	default:
		return 9
	}
}

func AppendDiscovered(candidates []DiscoveryCandidate, found []DiscoveryCandidate) []DiscoveryCandidate {
	seen := map[string]int{}
	for i, candidate := range candidates {
		seen[strings.ToLower(candidate.Source.Repository+"/"+candidate.Source.Subfolder)] = i
	}
	for _, candidate := range found {
		if candidate.Name == "" || len(candidate.Name) > 120 || strings.ContainsAny(candidate.Name, "\x00\r\n") {
			continue
		}
		if candidate.Source.CommitSHA == "" {
			if _, err := parseGitHubRepository(candidate.Source.Repository); err != nil || !validGitHubSubfolder(candidate.Source.Subfolder) {
				continue
			}
		} else if _, err := candidate.Source.ArchiveURL(); err != nil {
			continue
		}
		key := strings.ToLower(candidate.Source.Repository + "/" + candidate.Source.Subfolder)
		if i, ok := seen[key]; ok {
			candidates[i].Evidence = append(candidates[i].Evidence, candidate.Evidence...)
			continue
		}
		if len(candidates) == 5 {
			break
		}
		seen[key] = len(candidates)
		candidates = append(candidates, candidate)
	}
	return candidates
}

func (candidate DiscoveryCandidate) SourceURL() string {
	source := candidate.Source
	if source.CommitSHA == "" {
		return source.Repository
	}
	if strings.HasPrefix(source.Repository, "https://gitlab.com/") {
		value := source.Repository + "/-/tree/" + source.CommitSHA
		if source.Subfolder != "" {
			value += "/" + source.Subfolder
		}
		return value
	}
	if source.Subfolder != "" {
		return source.Repository + "/tree/" + source.CommitSHA + "/" + source.Subfolder
	}
	return source.Repository + "/commit/" + source.CommitSHA
}
