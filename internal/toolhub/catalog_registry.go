package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// DockerMCPCatalog reads Docker's public catalog declaration. The catalog's
// source field is required to be an exact GitHub tree/commit URL; image labels
// and immutable OCI attestations are checked by completeRecipeCandidate.
type DockerMCPCatalog struct {
	Endpoint string
	Client   *http.Client
}

func (c DockerMCPCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = dockerMCPCatalogURL
	}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, nil, 32<<20)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("%w: Docker MCP catalog response", ErrInvalid)
	}
	registry, ok := document["registry"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: Docker MCP catalog registry", ErrInvalid)
	}
	var candidates []RecipeCandidate
	for name, raw := range registry {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sourceURL := firstString(entry, "source", "upstream")
		source, err := ParseGitHubSource(sourceURL)
		if err != nil || source.Repository != lookup.Repository || source.Subfolder != lookup.Subfolder || source.CommitSHA != lookup.CommitSHA {
			continue
		}
		image := firstString(entry, "image", "identifier")
		if image == "" {
			continue
		}
		launch := LaunchRecipe{Artifact: image, Transport: ContainerMCP, Evidence: []RecipeEvidence{{Source: "docker-mcp", Path: "registry/" + name, Detail: "source-pinned Docker MCP catalog entry"}}}
		if command := stringArray(entry["command"]); len(command) > 0 {
			launch.Entrypoint = command
		}
		candidate := RecipeCandidate{Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA, Launch: launch, Connection: connectionFromDockerEntry(entry), Evidence: launch.Evidence}
		if completed, ok := completeRecipeCandidate(ctx, c.Client, candidate, lookup); ok {
			candidates = append(candidates, completed)
		}
	}
	return candidates, nil
}

func (c DockerMCPCatalog) SearchSources(ctx context.Context, query string) ([]DiscoveryCandidate, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = dockerMCPCatalogURL
	}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, nil, 32<<20)
	if err != nil {
		return nil, err
	}
	var document struct {
		Registry map[string]map[string]any `json:"registry"`
	}
	if json.Unmarshal(body, &document) != nil || len(document.Registry) == 0 {
		return nil, fmt.Errorf("%w: Docker MCP search response", ErrInvalid)
	}
	var found []DiscoveryCandidate
	for name, entry := range document.Registry {
		if !strings.Contains(strings.ToLower(name), strings.ToLower(query)) {
			continue
		}
		source, ok := discoverySource(firstString(entry, "source", "upstream"))
		if !ok {
			continue
		}
		found = append(found, DiscoveryCandidate{Name: name, Source: source, Status: "registry-source", Reason: "Docker MCP source metadata; generic review verifies the selected artifact", Credentials: connectionFromDockerEntry(entry).Fields, Evidence: []RecipeEvidence{{Source: "docker-mcp", Path: "registry/" + name, Detail: "source metadata"}}})
		if len(found) == 32 {
			break
		}
	}
	return found, nil
}

// DockerHubCatalog searches the public namespace API, then verifies every
// candidate through the OCI distribution API. Tags are never trusted: the
// selected image must carry the exact GitHub source/revision and attestations.
type DockerHubCatalog struct {
	Endpoint     string
	RegistryBase string
	Client       *http.Client
}

func (c DockerHubCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	c.Client = catalogHTTPClient(c.Client)
	proofClient := c.Client
	if c.RegistryBase == "" {
		proofClient = ociHTTPClient("registry-1.docker.io", "registry-1.docker.io")
	}
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = dockerHubAPIURL
	}
	repositories := dockerHubHintRepositories(lookup)
	var lastErr error
	for _, namespace := range appendUnique([]string{lookup.Owner}, "mcp") {
		discovered, err := c.listRepositories(ctx, base, namespace, lookup.Name)
		if err != nil {
			lastErr = err
			continue
		}
		for _, repository := range discovered {
			if dockerHubRepositoryMatches(repository, lookup) {
				repositories = appendUnique(repositories, repository)
			}
		}
	}
	if len(repositories) == 0 && lastErr != nil && len(lookup.ImageHints) == 0 {
		return nil, lastErr
	}
	var candidates []RecipeCandidate
	var proofErr error
	for _, repository := range repositories {
		namespace, name := splitDockerHubRepository(repository, lookup.Owner)
		tags, err := c.listTags(ctx, base, namespace, name)
		if err != nil {
			continue
		}
		for _, tag := range orderedTags(tags, lookup.VersionHints) {
			image := repository + ":" + tag
			candidate := RecipeCandidate{Repository: lookup.Repository, Subfolder: lookup.Subfolder, CommitSHA: lookup.CommitSHA, Launch: LaunchRecipe{Artifact: image, Transport: ContainerMCP, Evidence: []RecipeEvidence{{Source: "docker-hub", Path: image, Detail: "namespace/tag candidate verified by OCI metadata"}}}}
			if completed, ok, err := verifyRecipeCandidateAt(ctx, proofClient, c.RegistryBase, candidate, lookup); ok {
				candidates = append(candidates, completed)
				return candidates, nil
			} else if err != nil {
				proofErr = err
			}
		}
	}
	for _, hint := range lookup.ImageHints {
		ref, err := parseOCIReference(hint)
		if err != nil || (ref.Registry != "registry-1.docker.io" && ref.Registry != "docker.io") {
			continue
		}
		candidate := RecipeCandidate{Repository: lookup.Repository, Subfolder: lookup.Subfolder, CommitSHA: lookup.CommitSHA, Launch: LaunchRecipe{Artifact: hint, Transport: ContainerMCP, Evidence: []RecipeEvidence{{Source: "docker-hub", Path: hint, Detail: "repository image hint verified by OCI metadata"}}}}
		if completed, ok, err := verifyRecipeCandidateAt(ctx, proofClient, c.RegistryBase, candidate, lookup); ok {
			candidates = append(candidates, completed)
			return candidates, nil
		} else if err != nil {
			proofErr = err
		}
	}
	if proofErr != nil {
		return nil, proofErr
	}
	return candidates, nil
}

func (c DockerHubCatalog) listRepositories(ctx context.Context, base, namespace, name string) ([]string, error) {
	if !repositoryPartPattern.MatchString(namespace) {
		return nil, fmt.Errorf("%w: Docker Hub namespace", ErrInvalid)
	}
	endpoint, err := catalogQuery(base+"/v2/namespaces/"+url.PathEscape(namespace)+"/repositories", map[string]string{"page": "1", "page_size": "100", "name": name})
	if err != nil {
		return nil, err
	}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		return nil, err
	}
	var page struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil, fmt.Errorf("%w: Docker Hub repositories", ErrInvalid)
	}
	var repositories []string
	for _, result := range page.Results {
		if result.Name != "" {
			repositories = append(repositories, namespace+"/"+result.Name)
		}
	}
	return repositories, nil
}

func (c DockerHubCatalog) listTags(ctx context.Context, base, namespace, repository string) ([]string, error) {
	endpoint, err := catalogQuery(base+"/v2/namespaces/"+url.PathEscape(namespace)+"/repositories/"+url.PathEscape(repository)+"/tags", map[string]string{"page": "1", "page_size": "100"})
	if err != nil {
		return nil, err
	}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		return nil, err
	}
	var page struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil, fmt.Errorf("%w: Docker Hub tags", ErrInvalid)
	}
	var tags []string
	for _, result := range page.Results {
		if result.Name != "" && validImageTag(result.Name) {
			tags = append(tags, result.Name)
		}
	}
	return tags, nil
}

// GitHubPackagesCatalog uses the official GitHub Packages API to discover
// GHCR package names and tags. Public package listing may require a token;
// HUB_GHCR_TOKEN_ENV is optional and is sent only to api.github.com.
type GitHubPackagesCatalog struct {
	Endpoint     string
	RegistryBase string
	Token        string `json:"-"`
	Client       *http.Client
}

func (c GitHubPackagesCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	c.Client = catalogHTTPClient(c.Client)
	proofClient := c.Client
	if c.RegistryBase == "" {
		proofClient = ociHTTPClient("ghcr.io", "ghcr.io")
	}
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = githubAPIURL
	}
	packages, scope, err := c.listPackages(ctx, base, lookup.Owner)
	if err != nil {
		return nil, err
	}
	var candidates []RecipeCandidate
	for _, pkg := range packages {
		if !githubPackageMatches(pkg.Name, lookup) {
			continue
		}
		versions, err := c.listPackageVersions(ctx, base, scope, lookup.Owner, pkg.Name)
		if err != nil {
			continue
		}
		for _, version := range versions {
			for _, tag := range version.Metadata.Container.Tags {
				image := githubPackageImage(lookup.Owner, pkg.Name, tag)
				candidate := RecipeCandidate{Repository: lookup.Repository, Subfolder: lookup.Subfolder, CommitSHA: lookup.CommitSHA, Launch: LaunchRecipe{Artifact: image, Transport: ContainerMCP, Evidence: []RecipeEvidence{{Source: "ghcr", Path: image, Detail: "GitHub Packages tag verified by OCI metadata"}}}}
				if completed, ok := completeRecipeCandidateAt(ctx, proofClient, c.RegistryBase, candidate, lookup); ok {
					candidates = append(candidates, completed)
					return candidates, nil
				}
			}
		}
	}
	for _, hint := range lookup.ImageHints {
		ref, err := parseOCIReference(hint)
		if err != nil || ref.Registry != "ghcr.io" {
			continue
		}
		candidate := RecipeCandidate{Repository: lookup.Repository, Subfolder: lookup.Subfolder, CommitSHA: lookup.CommitSHA, Launch: LaunchRecipe{Artifact: hint, Transport: ContainerMCP, Evidence: []RecipeEvidence{{Source: "ghcr", Path: hint, Detail: "repository image hint verified by OCI metadata"}}}}
		if completed, ok := completeRecipeCandidateAt(ctx, proofClient, c.RegistryBase, candidate, lookup); ok {
			candidates = append(candidates, completed)
			return candidates, nil
		}
	}
	return candidates, nil
}

type githubPackage struct {
	Name string `json:"name"`
}

type githubPackageVersion struct {
	Name     string `json:"name"`
	Metadata struct {
		Container struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

func (c GitHubPackagesCatalog) listPackages(ctx context.Context, base, owner string) ([]githubPackage, string, error) {
	if !repositoryPartPattern.MatchString(owner) {
		return nil, "", fmt.Errorf("%w: GitHub Packages owner", ErrInvalid)
	}
	header := http.Header{"Accept": []string{"application/vnd.github+json"}, "X-GitHub-Api-Version": []string{"2022-11-28"}}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	for _, scope := range []string{"orgs", "users"} {
		endpoint, err := catalogQuery(base+"/"+scope+"/"+url.PathEscape(owner)+"/packages", map[string]string{"package_type": "container", "per_page": "100", "page": "1"})
		if err != nil {
			return nil, "", err
		}
		body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, header, 8<<20)
		if err != nil {
			if strings.Contains(err.Error(), "catalog status 401") || strings.Contains(err.Error(), "catalog status 403") || strings.Contains(err.Error(), "catalog status 404") {
				continue
			}
			return nil, "", err
		}
		var packages []githubPackage
		if json.Unmarshal(body, &packages) != nil {
			return nil, "", fmt.Errorf("%w: GitHub Packages response", ErrInvalid)
		}
		return packages, scope, nil
	}
	return nil, "", nil
}

func (c GitHubPackagesCatalog) listPackageVersions(ctx context.Context, base, scope, owner, packageName string) ([]githubPackageVersion, error) {
	if scope == "" {
		return nil, nil
	}
	header := http.Header{"Accept": []string{"application/vnd.github+json"}, "X-GitHub-Api-Version": []string{"2022-11-28"}}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	endpoint, err := catalogQuery(base+"/"+scope+"/"+url.PathEscape(owner)+"/packages/container/"+url.PathEscape(packageName)+"/versions", map[string]string{"per_page": "100", "page": "1"})
	if err != nil {
		return nil, err
	}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, header, 8<<20)
	if err != nil {
		return nil, err
	}
	var versions []githubPackageVersion
	if json.Unmarshal(body, &versions) != nil {
		return nil, fmt.Errorf("%w: GitHub package versions", ErrInvalid)
	}
	return versions, nil
}

func githubPackageImage(owner, packageName, tag string) string {
	name := strings.TrimPrefix(packageName, owner+"/")
	return "ghcr.io/" + owner + "/" + name + ":" + tag
}

func githubPackageMatches(name string, lookup RecipeLookup) bool {
	for _, hint := range lookup.ImageHints {
		if ref, err := parseOCIReference(hint); err == nil && ref.Registry == "ghcr.io" && strings.EqualFold(ref.Repository, lookup.Owner+"/"+strings.TrimPrefix(name, lookup.Owner+"/")) {
			return true
		}
	}
	base := strings.ToLower(pathBase(name))
	wanted := strings.ToLower(lookup.Name)
	return base == wanted || base == "mcp-"+wanted || base == wanted+"-mcp"
}

func pathBase(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func dockerHubHintRepositories(lookup RecipeLookup) []string {
	var repositories []string
	for _, hint := range lookup.ImageHints {
		if ref, err := parseOCIReference(hint); err == nil && (ref.Registry == "registry-1.docker.io" || ref.Registry == "docker.io") {
			repositories = appendUnique(repositories, ref.Repository)
		}
	}
	return repositories
}

func dockerHubRepositoryMatches(repository string, lookup RecipeLookup) bool {
	base := strings.ToLower(pathBase(repository))
	wanted := strings.ToLower(lookup.Name)
	return base == wanted || base == "mcp-"+wanted || base == wanted+"-mcp"
}

func splitDockerHubRepository(repository, fallbackNamespace string) (string, string) {
	parts := strings.Split(repository, "/")
	if len(parts) < 2 {
		return fallbackNamespace, repository
	}
	return parts[0], strings.Join(parts[1:], "/")
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func orderedTags(tags, preferred []string) []string {
	ordered := []string{}
	for _, tag := range append(append([]string(nil), preferred...), tags...) {
		if validImageTag(tag) {
			ordered = appendUnique(ordered, tag)
		}
		if len(ordered) >= 64 {
			break
		}
	}
	return ordered
}

func validImageTag(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\x00\r\n /")
}
