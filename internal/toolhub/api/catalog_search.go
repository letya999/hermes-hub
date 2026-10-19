package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// MCPRegistryCatalog is the read-only official MCP Registry adapter. It only
// returns metadata; the resolver still requires exact source/commit and OCI
// provenance before a container candidate can become ready.
type MCPRegistryCatalog struct {
	Endpoint string
	Client   *http.Client
}

func (c MCPRegistryCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	endpoint, err := CatalogQuery(c.Endpoint, map[string]string{"search": lookup.Name, "version": "latest", "limit": "32"})
	if err != nil {
		return nil, err
	}
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Servers []json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("%w: MCP Registry response", ErrInvalid)
	}
	return catalogCandidates(ctx, c.Client, response.Servers, "mcp-registry", lookup)
}

func (c MCPRegistryCatalog) SearchSources(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	endpoint, err := CatalogQuery(c.Endpoint, map[string]string{"search": query, "version": "latest", "limit": "32"})
	if err != nil {
		return nil, err
	}
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Servers []json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Servers) > 32 {
		return nil, fmt.Errorf("%w: MCP Registry search response", ErrInvalid)
	}
	var found []DiscoveryCandidate
	for _, raw := range response.Servers {
		var document map[string]any
		if json.Unmarshal(raw, &document) != nil {
			continue
		}
		if nested, ok := document["server"].(map[string]any); ok {
			document = nested
		}
		repository := ""
		if value, ok := document["repository"].(map[string]any); ok {
			repository = firstString(value, "url", "uri")
		} else {
			repository = firstString(document, "repository", "repositoryUrl", "repository_url")
		}
		source, ok := discoverySource(repository)
		if value, ok := document["repository"].(map[string]any); ok && source.Subfolder == "" {
			source.Subfolder = firstString(value, "subfolder")
		}
		name := firstString(document, "name", "title")
		if !ok || name == "" || !strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
			continue
		}
		found = append(found, DiscoveryCandidate{Name: name, Source: source, Status: "registry-source", Reason: "Registry source metadata; generic review pins the source and verifies the artifact", Evidence: []RecipeEvidence{{Source: "mcp-registry", Detail: "name and repository metadata"}}})
	}
	return found, nil
}

// ToolHiveCatalog reads the published server.json entry from ToolHive's
// catalog repository. It deliberately does not run the thv CLI or trust a
// catalog name without repository and OCI correlation.
type ToolHiveCatalog struct {
	Endpoint      string
	IndexEndpoint string
	Client        *http.Client
}

// GitHubSearchCatalog is a source-only fallback. Its results never authorize
// an artifact or replace repository review.
type GitHubSearchCatalog struct {
	Endpoint string
	Token    string `json:"-"`
	Client   *http.Client
}

func (c GitHubSearchCatalog) SearchSources(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = githubAPIURL
	}
	endpoint, err := CatalogQuery(base+"/search/repositories", map[string]string{"q": query + " mcp in:name,description", "page": "1", "per_page": "16"})
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Accept": []string{"application/vnd.github+json"}}
	if c.Token != "" {
		headers.Set("Authorization", "Bearer "+c.Token)
	}
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, headers, 4<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Items) > 16 {
		return nil, fmt.Errorf("%w: GitHub repository search response", ErrInvalid)
	}
	var found []DiscoveryCandidate
	for _, item := range response.Items {
		if item["fork"] == true || item["archived"] == true || item["disabled"] == true || item["private"] == true {
			continue
		}
		source, ok := discoverySource(firstString(item, "html_url"))
		if !ok {
			continue
		}
		name := firstString(item, "full_name")
		if name == "" || !strings.EqualFold(source.Repository, "https://github.com/"+name) {
			continue
		}
		found = append(found, DiscoveryCandidate{Name: name, Source: source, Status: "github-source", Reason: "GitHub search result only; generic review must establish MCP compatibility", Evidence: []RecipeEvidence{{Source: "github-search", Detail: "public repository search result"}}})
	}
	return found, nil
}

func (c ToolHiveCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = toolHiveCatalogURL
	}
	endpoint := base + "/" + url.PathEscape(lookup.Name) + "/server.json"
	body, headers, err := FetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		if headers != nil && headers.Get("X-Catalog-Not-Found") == "1" {
			return nil, nil
		}
		return nil, err
	}
	candidates, err := catalogCandidates(ctx, c.Client, []json.RawMessage{body}, "toolhive", lookup)
	return candidates, err
}

func (c ToolHiveCatalog) SearchSources(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	endpoint := c.IndexEndpoint
	if endpoint == "" {
		endpoint = toolHiveIndexURL
	}
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		return nil, err
	}
	var index struct {
		Data struct {
			Servers []map[string]any `json:"servers"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &index) != nil || len(index.Data.Servers) == 0 || len(index.Data.Servers) > 512 {
		return nil, fmt.Errorf("%w: ToolHive search response", ErrInvalid)
	}
	query = strings.ToLower(query)
	var found []DiscoveryCandidate
	for _, server := range index.Data.Servers {
		name := firstString(server, "title", "name")
		if !strings.Contains(strings.ToLower(name+" "+path.Base(firstString(server, "name"))+" "+firstString(server, "description")), query) {
			continue
		}
		repository, _ := server["repository"].(map[string]any)
		source, ok := discoverySource(firstString(repository, "url"))
		if !ok {
			continue
		}
		if source.Subfolder == "" {
			source.Subfolder = firstString(repository, "subfolder")
		}
		found = append(found, DiscoveryCandidate{Name: name, Source: source, Status: "registry-source", Reason: "ToolHive source metadata; generic review pins and verifies the repository", Evidence: []RecipeEvidence{{Source: "toolhive", Detail: "published catalog repository metadata"}}})
		if len(found) == 32 {
			break
		}
	}
	return found, nil
}

func discoverySource(raw string) (ArtifactSource, bool) {
	if source, err := ParseGitHubSource(raw); err == nil {
		return source, true
	}
	repository, err := parseGitHubRepository(raw)
	if err != nil {
		return ArtifactSource{}, false
	}
	return ArtifactSource{Repository: repository}, true
}

// SmitheryCatalog is opt-in because Smithery requires an API key even for
// registry reads. The key is used only as an HTTP header and never enters a
// recipe, error, or audit record.
type SmitheryCatalog struct {
	Endpoint string
	Token    string `json:"-"`
	Client   *http.Client
}

func (c SmitheryCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, fmt.Errorf("%w: Smithery catalog token required", ErrUnauthorized)
	}
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = smitheryAPIURL
	}
	endpoint, err := CatalogQuery(base+"/servers", map[string]string{"repoOwner": lookup.Owner, "repoName": lookup.Name, "page": "1", "pageSize": "50"})
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Authorization": []string{"Bearer " + c.Token}}
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, headers, 8<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("%w: Smithery server list", ErrInvalid)
	}
	if len(response.Servers) == 0 {
		fallback, fallbackErr := CatalogQuery(base+"/servers", map[string]string{"q": lookup.Name, "page": "1", "pageSize": "50"})
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		body, _, fallbackErr = FetchCatalogJSON(ctx, c.Client, fallback, headers, 8<<20)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("%w: Smithery search response", ErrInvalid)
		}
	}
	var candidates []RecipeCandidate
	for _, summary := range response.Servers {
		qualified := firstString(summary, "qualifiedName")
		if qualified == "" {
			continue
		}
		detail, err := c.getSmithery(ctx, base+"/servers/"+url.PathEscape(qualified), headers)
		if err != nil {
			continue
		}
		releases, err := c.getSmithery(ctx, base+"/servers/"+url.PathEscape(qualified)+"/releases", headers)
		if err != nil {
			continue
		}
		var releaseList []map[string]any
		if json.Unmarshal(releases, &releaseList) != nil {
			continue
		}
		for _, release := range releaseList {
			commit := firstString(release, "commit")
			upstream := firstString(release, "upstreamUrl", "upstreamURL")
			if !gitSHAPattern.MatchString(commit) || upstream == "" {
				continue
			}
			repository, subfolder := SmitheryRepository(upstream)
			if !sameRepository(repository, lookup.Repository) || subfolder != lookup.Subfolder || !strings.EqualFold(commit, lookup.CommitSHA) {
				continue
			}
			candidate := smitheryCandidate(detail, release, repository, subfolder, commit)
			if candidate.Launch.Endpoint != "" || candidate.Launch.Artifact != "" {
				candidate.Evidence = append(candidate.Evidence, RecipeEvidence{Source: "smithery", Path: qualified + "/releases", Detail: "repository and commit matched Smithery release"})
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates, nil
}

func (c SmitheryCatalog) SearchSources(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, fmt.Errorf("%w: Smithery catalog token required", ErrUnauthorized)
	}
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = smitheryAPIURL
	}
	endpoint, err := CatalogQuery(base+"/servers", map[string]string{"q": query, "page": "1", "pageSize": "32"})
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Authorization": []string{"Bearer " + c.Token}}
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, headers, 8<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		Servers []map[string]any `json:"servers"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Servers) > 32 {
		return nil, fmt.Errorf("%w: Smithery search response", ErrInvalid)
	}
	var found []DiscoveryCandidate
	for _, server := range response.Servers {
		if len(found) == 8 {
			break
		}
		qualified := firstString(server, "qualifiedName")
		name := firstString(server, "displayName", "qualifiedName")
		if qualified == "" || len(qualified) > 160 || !strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
			continue
		}
		releases, err := c.getSmithery(ctx, base+"/servers/"+url.PathEscape(qualified)+"/releases", headers)
		if err != nil {
			continue
		}
		var versions []map[string]any
		if json.Unmarshal(releases, &versions) != nil || len(versions) > 100 {
			continue
		}
		for _, version := range versions {
			source, ok := discoverySource(firstString(version, "upstreamUrl", "upstreamURL"))
			if !ok {
				continue
			}
			if commit := strings.ToLower(firstString(version, "commit")); gitSHAPattern.MatchString(commit) {
				if source.CommitSHA != "" && source.CommitSHA != commit {
					continue
				}
				source.CommitSHA = commit
			}
			found = append(found, DiscoveryCandidate{Name: name, Source: source, Status: "registry-source", Reason: "Smithery release source; generic review rechecks the repository and revision", Evidence: []RecipeEvidence{{Source: "smithery", Detail: "release repository metadata"}}})
			break
		}
	}
	return found, nil
}

func (c SmitheryCatalog) getSmithery(ctx context.Context, endpoint string, headers http.Header) ([]byte, error) {
	body, _, err := FetchCatalogJSON(ctx, c.Client, endpoint, headers, 8<<20)
	return body, err
}

func SmitheryRepository(raw string) (string, string) {
	if source, err := ParseGitHubSource(raw); err == nil {
		return source.Repository, source.Subfolder
	}
	if repository, err := parseGitHubRepository(raw); err == nil {
		return repository, ""
	}
	return "", ""
}

func smitheryCandidate(detail []byte, releaseDocument map[string]any, repository, subfolder, commit string) RecipeCandidate {
	var document map[string]any
	_ = json.Unmarshal(detail, &document)
	launch := LaunchRecipe{Transport: BoundedCLI, Version: firstString(releaseDocument, "id", "version")}
	connection := ConnectionRecipe{}
	if connections, ok := document["connections"].([]any); ok {
		for _, raw := range connections {
			value, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typeName := strings.ToLower(firstString(value, "type"))
			config := value["configSchema"]
			connection = mergeConnection(connection, ConnectionFromSchema(config, "smithery"))
			if typeName == "http" {
				launch = MergeLaunch(launch, LaunchRecipe{Transport: RemoteMCP, Endpoint: firstString(value, "deploymentUrl", "url"), Network: []string{HostOfURL(firstString(value, "deploymentUrl", "url"))}}, nil)
			} else if bundle := firstString(value, "bundleUrl"); bundle != "" {
				launch.Artifact = bundle
			}
		}
	}
	return RecipeCandidate{Repository: repository, Subfolder: subfolder, CommitSHA: commit, Launch: launch, Connection: connection}
}
