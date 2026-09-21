package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const (
	officialMCPRegistryURL = "https://registry.modelcontextprotocol.io/v0.1/servers"
	toolHiveCatalogURL     = "https://raw.githubusercontent.com/stacklok/toolhive-catalog/main/registries/toolhive/servers"
	dockerMCPCatalogURL    = "https://desktop.docker.com/mcp/catalog/v3/catalog.json"
	smitheryAPIURL         = "https://api.smithery.ai"
	dockerHubAPIURL        = "https://hub.docker.com"
	githubAPIURL           = "https://api.github.com"
)

var allRecipeCatalogNames = []string{"mcp-registry", "toolhive", "docker-mcp", "smithery", "docker-hub", "ghcr"}

// RecipeCatalogsFromEnv keeps external integrations opt-in. The value is a
// comma-separated list of the fixed adapters, or "all" for the complete
// public catalog set. No arbitrary URL or command is accepted here.
func RecipeCatalogsFromEnv() ([]RecipeCatalog, error) {
	raw := strings.TrimSpace(os.Getenv("HUB_RECIPE_CATALOGS"))
	if raw == "" {
		return nil, nil
	}
	requested := []string{}
	for _, item := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(item))
		if name == "all" {
			requested = append(requested, allRecipeCatalogNames...)
		} else {
			requested = append(requested, name)
		}
	}
	var catalogs []RecipeCatalog
	seen := map[string]bool{}
	for _, name := range requested {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "mcp-registry":
			catalogs = append(catalogs, MCPRegistryCatalog{Endpoint: officialMCPRegistryURL})
		case "toolhive":
			catalogs = append(catalogs, ToolHiveCatalog{Endpoint: toolHiveCatalogURL})
		case "docker-mcp":
			catalogs = append(catalogs, DockerMCPCatalog{Endpoint: dockerMCPCatalogURL})
		case "docker-hub":
			catalogs = append(catalogs, DockerHubCatalog{Endpoint: dockerHubAPIURL})
		case "ghcr":
			token := ""
			if envName := strings.TrimSpace(os.Getenv("HUB_GHCR_TOKEN_ENV")); envName != "" {
				if !credentialPattern.MatchString(envName) {
					return nil, fmt.Errorf("%w: GHCR token environment", ErrInvalid)
				}
				token = os.Getenv(envName)
			}
			catalogs = append(catalogs, GitHubPackagesCatalog{Endpoint: githubAPIURL, Token: token})
		case "smithery":
			envName := strings.TrimSpace(os.Getenv("HUB_SMITHERY_TOKEN_ENV"))
			if envName == "" {
				envName = "SMITHERY_API_KEY"
			}
			if !credentialPattern.MatchString(envName) || os.Getenv(envName) == "" {
				return nil, fmt.Errorf("%w: Smithery token environment", ErrUnauthorized)
			}
			catalogs = append(catalogs, SmitheryCatalog{Endpoint: smitheryAPIURL, Token: os.Getenv(envName)})
		default:
			return nil, fmt.Errorf("%w: unknown recipe catalog %q", ErrInvalid, name)
		}
	}
	return catalogs, nil
}

// MCPRegistryCatalog is the read-only official MCP Registry adapter. It only
// returns metadata; the resolver still requires exact source/commit and OCI
// provenance before a container candidate can become ready.
type MCPRegistryCatalog struct {
	Endpoint string
	Client   *http.Client
}

func (c MCPRegistryCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	endpoint, err := catalogQuery(c.Endpoint, map[string]string{"search": lookup.Name, "version": "latest", "limit": "32"})
	if err != nil {
		return nil, err
	}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
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

// ToolHiveCatalog reads the published server.json entry from ToolHive's
// catalog repository. It deliberately does not run the thv CLI or trust a
// catalog name without repository and OCI correlation.
type ToolHiveCatalog struct {
	Endpoint string
	Client   *http.Client
}

func (c ToolHiveCatalog) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = toolHiveCatalogURL
	}
	endpoint := base + "/" + url.PathEscape(lookup.Name) + "/server.json"
	body, headers, err := fetchCatalogJSON(ctx, c.Client, endpoint, nil, 8<<20)
	if err != nil {
		if headers != nil && headers.Get("X-Catalog-Not-Found") == "1" {
			return nil, nil
		}
		return nil, err
	}
	candidates, err := catalogCandidates(ctx, c.Client, []json.RawMessage{body}, "toolhive", lookup)
	return candidates, err
}

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
	endpoint, err := catalogQuery(base+"/servers", map[string]string{"repoOwner": lookup.Owner, "repoName": lookup.Name, "page": "1", "pageSize": "50"})
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Authorization": []string{"Bearer " + c.Token}}
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, headers, 8<<20)
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
		fallback, fallbackErr := catalogQuery(base+"/servers", map[string]string{"q": lookup.Name, "page": "1", "pageSize": "50"})
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		body, _, fallbackErr = fetchCatalogJSON(ctx, c.Client, fallback, headers, 8<<20)
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
			repository, subfolder := smitheryRepository(upstream)
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

func (c SmitheryCatalog) getSmithery(ctx context.Context, endpoint string, headers http.Header) ([]byte, error) {
	body, _, err := fetchCatalogJSON(ctx, c.Client, endpoint, headers, 8<<20)
	return body, err
}

func smitheryRepository(raw string) (string, string) {
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
			connection = mergeConnection(connection, connectionFromSchema(config, "smithery"))
			if typeName == "http" {
				launch = mergeLaunch(launch, LaunchRecipe{Transport: RemoteMCP, Endpoint: firstString(value, "deploymentUrl", "url"), Network: []string{hostOfURL(firstString(value, "deploymentUrl", "url"))}}, nil)
			} else if bundle := firstString(value, "bundleUrl"); bundle != "" {
				launch.Artifact = bundle
			}
		}
	}
	return RecipeCandidate{Repository: repository, Subfolder: subfolder, CommitSHA: commit, Launch: launch, Connection: connection}
}

func connectionFromSchema(raw any, alternative string) ConnectionRecipe {
	properties, ok := raw.(map[string]any)
	if !ok {
		return ConnectionRecipe{}
	}
	required := map[string]bool{}
	if values, ok := properties["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	}
	if nested, ok := properties["properties"].(map[string]any); ok {
		properties = nested
	}
	var recipe ConnectionRecipe
	for name, value := range properties {
		if name == "required" || name == "properties" {
			continue
		}
		fieldName := schemaFieldName(name)
		if fieldName == "" {
			continue
		}
		field := ConnectionField{Name: fieldName, Type: connectionType(fieldName), Required: required[name], Secret: secretName(fieldName), Delivery: "json", Target: name, Alternative: alternative}
		if descriptor, ok := value.(map[string]any); ok {
			if descriptor["secret"] == true || strings.EqualFold(firstString(descriptor, "format", "type"), "password") {
				field.Secret = true
			}
		}
		recipe.Fields = append(recipe.Fields, field)
	}
	return normalizeConnection(recipe)
}

func connectionFromDockerEntry(entry map[string]any) ConnectionRecipe {
	var recipe ConnectionRecipe
	if secrets, ok := entry["secrets"].([]any); ok {
		for _, raw := range secrets {
			value, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := firstString(value, "env", "name")
			if envKey(name) != "" {
				recipe.Fields = append(recipe.Fields, ConnectionField{Name: name, Type: "secret", Required: true, Secret: true, Delivery: "env", Target: name})
			}
		}
	}
	if config := entry["config"]; config != nil {
		recipe = mergeConnection(recipe, connectionFromSchema(config, "docker-mcp"))
	}
	return normalizeConnection(recipe)
}

func catalogCandidates(ctx context.Context, client *http.Client, values []json.RawMessage, sourceName string, lookup RecipeLookup) ([]RecipeCandidate, error) {
	var candidates []RecipeCandidate
	for _, raw := range values {
		var document map[string]any
		if json.Unmarshal(raw, &document) != nil {
			continue
		}
		if nested, ok := document["server"].(map[string]any); ok {
			document = nested
		}
		candidate, ok, err := serverDocumentCandidate(document, sourceName, lookup)
		if err != nil || !ok {
			continue
		}
		if completed, accepted := completeRecipeCandidate(ctx, client, candidate, lookup); accepted {
			candidates = append(candidates, completed)
		} else if candidate.CommitSHA != "" && strings.EqualFold(candidate.CommitSHA, lookup.CommitSHA) && candidate.Launch.Transport != ContainerMCP {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func serverDocumentCandidate(document map[string]any, sourceName string, lookup RecipeLookup) (RecipeCandidate, bool, error) {
	repository := ""
	subfolder := ""
	if value, ok := document["repository"].(map[string]any); ok {
		repository = firstString(value, "url", "uri")
		subfolder = firstString(value, "subfolder", "path")
	} else {
		repository = firstString(document, "repository", "repositoryUrl", "repository_url")
	}
	if !sameRepository(repository, lookup.Repository) || subfolder != lookup.Subfolder {
		return RecipeCandidate{}, false, nil
	}
	source := ArtifactSource{Repository: lookup.Repository, Subfolder: lookup.Subfolder, CommitSHA: lookup.CommitSHA}
	encoded, err := json.Marshal(document)
	if err != nil {
		return RecipeCandidate{}, false, err
	}
	launch, connection, ok, err := parseServerManifest(encoded, sourceName, source)
	if err != nil || !ok {
		return RecipeCandidate{}, false, err
	}
	commit := exactCommitMetadata(document)
	evidence := []RecipeEvidence{{Source: sourceName, Detail: "repository-matched catalog metadata"}}
	return RecipeCandidate{Repository: repository, Subfolder: subfolder, CommitSHA: commit, Launch: launch, Connection: connection, Evidence: evidence}, true, nil
}

func exactCommitMetadata(value any) string {
	var visit func(any) string
	visit = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range []string{"commit", "revision", "commitSHA", "sourceCommit"} {
				if candidate, ok := typed[key].(string); ok && gitSHAPattern.MatchString(strings.ToLower(candidate)) {
					return strings.ToLower(candidate)
				}
			}
			for _, nested := range typed {
				if candidate := visit(nested); candidate != "" {
					return candidate
				}
			}
		case []any:
			for _, nested := range typed {
				if candidate := visit(nested); candidate != "" {
					return candidate
				}
			}
		}
		return ""
	}
	return visit(value)
}

type ociProof struct {
	Image            string
	ManifestDigest   string
	Source           string
	Revision         string
	Version          string
	Entrypoint       []string
	Health           HealthProbe
	ProvenanceDigest string
	SBOMDigest       string
	Evidence         []RecipeEvidence
}

func completeRecipeCandidate(ctx context.Context, client *http.Client, candidate RecipeCandidate, lookup RecipeLookup) (RecipeCandidate, bool) {
	return completeRecipeCandidateAt(ctx, client, "", candidate, lookup)
}

func completeRecipeCandidateAt(ctx context.Context, client *http.Client, registryBase string, candidate RecipeCandidate, lookup RecipeLookup) (RecipeCandidate, bool) {
	completed, accepted, _ := verifyRecipeCandidateAt(ctx, client, registryBase, candidate, lookup)
	return completed, accepted
}

func verifyRecipeCandidateAt(ctx context.Context, client *http.Client, registryBase string, candidate RecipeCandidate, lookup RecipeLookup) (RecipeCandidate, bool, error) {
	if candidate.Launch.Transport != ContainerMCP {
		return candidate, candidate.CommitSHA != "" && strings.EqualFold(candidate.CommitSHA, lookup.CommitSHA), nil
	}
	proof, err := inspectPublishedOCIAt(ctx, client, registryBase, candidate.Launch.Artifact)
	if err != nil {
		return RecipeCandidate{}, false, err
	}
	if !sameRepository(proof.Source, lookup.Repository) || !strings.EqualFold(proof.Revision, lookup.CommitSHA) {
		return RecipeCandidate{}, false, nil
	}
	if candidate.CommitSHA != "" && !strings.EqualFold(candidate.CommitSHA, lookup.CommitSHA) {
		return RecipeCandidate{}, false, nil
	}
	if len(lookup.VersionHints) > 0 && proof.Version != "" && !containsFold(lookup.VersionHints, proof.Version) {
		return RecipeCandidate{}, false, nil
	}
	candidate.Repository = lookup.Repository
	candidate.Subfolder = lookup.Subfolder
	candidate.CommitSHA = lookup.CommitSHA
	candidate.Launch.Artifact = proof.Image
	candidate.Launch.Digest = proof.ManifestDigest
	if candidate.Launch.Version == "" {
		candidate.Launch.Version = proof.Version
	}
	if len(candidate.Launch.Entrypoint) == 0 {
		candidate.Launch.Entrypoint = proof.Entrypoint
	}
	if candidate.Launch.Health.Value == "" {
		candidate.Launch.Health = proof.Health
	}
	candidate.Launch.Evidence = append(candidate.Launch.Evidence, proof.Evidence...)
	candidate.Evidence = append(candidate.Evidence, proof.Evidence...)
	return candidate, launchReady(candidate.Launch), nil
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimPrefix(value, "v"), strings.TrimPrefix(wanted, "v")) {
			return true
		}
	}
	return false
}

func recipeImageHints(launch LaunchRecipe, files map[string][]byte) []string {
	var hints []string
	add := func(value string) {
		value = strings.Trim(value, "\"'`.,;()[]{}<>")
		if _, err := parseOCIReference(value); err == nil {
			hints = appendUnique(hints, value)
		}
	}
	add(launch.Artifact)
	for name, data := range files {
		base := strings.ToLower(path.Base(name))
		lowerName := strings.ToLower(name)
		if !strings.HasPrefix(base, "dockerfile") && base != "server.json" && base != "manifest.json" && base != "compose.yaml" && base != "compose.yml" && !strings.HasPrefix(base, "docker-compose.") && !strings.HasPrefix(lowerName, ".github/workflows/") && !strings.Contains(lowerName, "/.github/workflows/") {
			continue
		}
		for _, token := range strings.Fields(string(data)) {
			add(token)
		}
	}
	return hints
}

type ociRegistryClient struct {
	HTTP         *http.Client
	RegistryBase string
}

func inspectPublishedOCI(ctx context.Context, client *http.Client, raw string) (ociProof, error) {
	return inspectPublishedOCIAt(ctx, client, "", raw)
}

func inspectPublishedOCIAt(ctx context.Context, client *http.Client, registryBase, raw string) (ociProof, error) {
	ref, err := parseOCIReference(raw)
	if err != nil {
		return ociProof{}, err
	}
	registry := ociRegistryClient{HTTP: client, RegistryBase: registryBase}
	manifestBody, headers, err := registry.get(ctx, ref.Registry, "/v2/"+ref.Repository+"/manifests/"+url.PathEscape(ref.Reference), "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		return ociProof{}, err
	}
	manifestDigest := headers.Get("Docker-Content-Digest")
	if manifestDigest == "" {
		hash := sha256.Sum256(manifestBody)
		manifestDigest = "sha256:" + hex.EncodeToString(hash[:])
	}
	if !digestPattern.MatchString(manifestDigest) {
		return ociProof{}, fmt.Errorf("%w: OCI manifest digest", ErrInvalid)
	}
	if ref.Digest != "" && ref.Digest != manifestDigest {
		return ociProof{}, fmt.Errorf("%w: OCI digest drift", ErrStale)
	}
	manifest := map[string]any{}
	if json.Unmarshal(manifestBody, &manifest) != nil {
		return ociProof{}, fmt.Errorf("%w: OCI manifest", ErrInvalid)
	}
	configDigest := ""
	var indexManifests []any
	attestationSubject := manifestDigest
	if config, ok := manifest["config"].(map[string]any); ok {
		configDigest = firstString(config, "digest")
	}
	if manifests, ok := manifest["manifests"].([]any); ok && len(manifests) > 0 {
		indexManifests = manifests
		child := chooseOCIManifest(manifests)
		if child == "" {
			return ociProof{}, fmt.Errorf("%w: OCI platform manifest", ErrInvalid)
		}
		attestationSubject = child
		childBody, _, err := registry.get(ctx, ref.Registry, "/v2/"+ref.Repository+"/manifests/"+url.PathEscape(child), "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		if err != nil {
			return ociProof{}, err
		}
		var selected map[string]any
		if json.Unmarshal(childBody, &selected) != nil {
			return ociProof{}, fmt.Errorf("%w: OCI platform manifest", ErrInvalid)
		}
		if config, ok := selected["config"].(map[string]any); ok {
			configDigest = firstString(config, "digest")
		}
	}
	if !digestPattern.MatchString(configDigest) {
		return ociProof{}, fmt.Errorf("%w: OCI config digest", ErrInvalid)
	}
	configBody, _, err := registry.get(ctx, ref.Registry, "/v2/"+ref.Repository+"/blobs/"+url.PathEscape(configDigest), "application/json")
	if err != nil {
		return ociProof{}, err
	}
	var config struct {
		Config struct {
			Labels      map[string]string `json:"Labels"`
			Entrypoint  []string          `json:"Entrypoint"`
			Cmd         []string          `json:"Cmd"`
			Healthcheck struct {
				Test []string `json:"Test"`
			} `json:"Healthcheck"`
		} `json:"config"`
	}
	if json.Unmarshal(configBody, &config) != nil {
		return ociProof{}, fmt.Errorf("%w: OCI config", ErrInvalid)
	}
	labels := config.Config.Labels
	if labels == nil {
		return ociProof{}, fmt.Errorf("%w: OCI provenance labels", ErrUnauthorized)
	}
	provenanceDigest, sbomDigest, evidence, err := registry.attestations(ctx, ref.Registry, ref.Repository, attestationSubject)
	if err != nil && len(indexManifests) > 0 {
		provenanceDigest, sbomDigest, evidence, err = registry.embeddedAttestations(ctx, ref.Registry, ref.Repository, indexManifests, attestationSubject)
	}
	if err != nil {
		return ociProof{}, err
	}
	entrypoint := append(append([]string(nil), config.Config.Entrypoint...), config.Config.Cmd...)
	for _, arg := range entrypoint {
		if len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") || strings.Contains(arg, "${") {
			return ociProof{}, fmt.Errorf("%w: OCI entrypoint", ErrInvalid)
		}
	}
	proof := ociProof{Image: ref.Image, ManifestDigest: manifestDigest, Source: firstString(labelsAny(labels), "org.opencontainers.image.source", "org.opencontainers.image.url", "org.opencontainers.image.repository"), Revision: strings.ToLower(labels["org.opencontainers.image.revision"]), Version: labels["org.opencontainers.image.version"], Entrypoint: entrypoint, ProvenanceDigest: provenanceDigest, SBOMDigest: sbomDigest, Evidence: evidence}
	if len(config.Config.Healthcheck.Test) > 1 && strings.EqualFold(config.Config.Healthcheck.Test[0], "CMD") {
		check := config.Config.Healthcheck.Test[1:]
		if len(check) > 0 && validCommand(check[0]) {
			proof.Health = HealthProbe{Kind: "exec", Value: strings.Join(check, " "), TimeoutSeconds: 5}
		}
	}
	if !strings.HasPrefix(strings.ToLower(proof.Source), "https://github.com/") || !gitSHAPattern.MatchString(proof.Revision) {
		return ociProof{}, fmt.Errorf("%w: OCI source provenance", ErrUnauthorized)
	}
	return proof, nil
}

func chooseOCIManifest(values []any) string {
	for _, raw := range values {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		platform, _ := value["platform"].(map[string]any)
		if strings.EqualFold(firstString(platform, "os"), "linux") && (strings.EqualFold(firstString(platform, "architecture"), "amd64") || strings.EqualFold(firstString(platform, "architecture"), "arm64")) {
			return firstString(value, "digest")
		}
	}
	return ""
}

type ociReference struct {
	Registry, Repository, Reference, Digest, Image string
}

func parseOCIReference(raw string) (ociReference, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n \t") || strings.Contains(raw, "..") {
		return ociReference{}, fmt.Errorf("%w: OCI reference", ErrInvalid)
	}
	name := raw
	ref := ""
	if at := strings.LastIndex(raw, "@"); at > 0 {
		name, ref = raw[:at], raw[at+1:]
	} else if colon := strings.LastIndex(raw, ":"); colon > strings.LastIndex(raw, "/") {
		name, ref = raw[:colon], raw[colon+1:]
	}
	if name == "" || ref == "" {
		return ociReference{}, fmt.Errorf("%w: OCI tag or digest required", ErrInvalid)
	}
	parts := strings.Split(name, "/")
	registry := "registry-1.docker.io"
	repository := name
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		registry, repository = strings.ToLower(parts[0]), strings.Join(parts[1:], "/")
	}
	if repository == "" || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") {
		return ociReference{}, fmt.Errorf("%w: OCI repository", ErrInvalid)
	}
	digest := ""
	if strings.HasPrefix(ref, "sha256:") {
		if !digestPattern.MatchString(ref) {
			return ociReference{}, fmt.Errorf("%w: OCI digest", ErrInvalid)
		}
		digest = ref
	}
	image := name
	return ociReference{Registry: registry, Repository: repository, Reference: ref, Digest: digest, Image: image}, nil
}

func (c ociRegistryClient) get(ctx context.Context, registry, requestPath, accept string) ([]byte, http.Header, error) {
	if registry == "docker.io" {
		registry = "registry-1.docker.io"
	}
	client := c.HTTP
	if client == nil {
		endpointHost := registry
		if c.RegistryBase != "" {
			if parsed, err := url.Parse(c.RegistryBase); err == nil && parsed.Hostname() != "" {
				endpointHost = parsed.Hostname()
			}
		}
		client = ociHTTPClient(registry, endpointHost)
	}
	endpoint := "https://" + registry + requestPath
	if c.RegistryBase != "" {
		endpoint = strings.TrimRight(c.RegistryBase, "/") + requestPath
	}
	request := func(token string) ([]byte, http.Header, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, nil, 0, err
		}
		req.Header.Set("Accept", accept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, nil, 0, err
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
		if readErr != nil || len(body) > 8<<20 {
			return nil, response.Header, response.StatusCode, fmt.Errorf("%w: oversized OCI response", ErrInvalid)
		}
		return body, response.Header, response.StatusCode, nil
	}
	body, headers, status, err := request("")
	if err != nil {
		return nil, nil, err
	}
	if status == http.StatusUnauthorized {
		token, tokenErr := c.token(ctx, client, headers.Get("WWW-Authenticate"))
		if tokenErr != nil {
			return nil, nil, tokenErr
		}
		body, headers, status, err = request(token)
		if err != nil {
			return nil, nil, err
		}
	}
	if status != http.StatusOK {
		return nil, headers, fmt.Errorf("%w: OCI registry status %d", ErrInvalid, status)
	}
	return body, headers, nil
}

func ociHTTPClient(registry, endpointHost string) *http.Client {
	allowed := map[string]bool{strings.ToLower(endpointHost): true, strings.ToLower(registry): true}
	if registry == "registry-1.docker.io" {
		allowed["auth.docker.io"] = true
		allowed["auth.docker.com"] = true
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		host := strings.ToLower(request.URL.Hostname())
		redirectAllowed := allowed[host]
		if registry == "registry-1.docker.io" && strings.HasSuffix(host, ".cloudfront.docker.com") {
			redirectAllowed = true
		}
		if registry == "ghcr.io" && strings.HasSuffix(host, ".githubusercontent.com") {
			redirectAllowed = true
		}
		if request.URL.Scheme != "https" || !redirectAllowed {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

func (c ociRegistryClient) token(ctx context.Context, client *http.Client, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("%w: OCI registry authentication challenge", ErrUnauthorized)
	}
	values := map[string]string{}
	for _, item := range strings.Split(challenge[len("Bearer "):], ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok {
			values[key] = strings.Trim(value, "\"")
		}
	}
	realm := values["realm"]
	if realm == "" {
		return "", fmt.Errorf("%w: OCI token realm", ErrUnauthorized)
	}
	query := url.Values{}
	if values["service"] != "" {
		query.Set("service", values["service"])
	}
	if values["scope"] != "" {
		query.Set("scope", values["scope"])
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: OCI token status", ErrUnauthorized)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload) != nil {
		return "", fmt.Errorf("%w: OCI token response", ErrUnauthorized)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", fmt.Errorf("%w: OCI token missing", ErrUnauthorized)
	}
	return payload.Token, nil
}

func (c ociRegistryClient) attestations(ctx context.Context, registry, repository, subject string) (string, string, []RecipeEvidence, error) {
	body, _, err := c.get(ctx, registry, "/v2/"+repository+"/referrers/"+url.PathEscape(subject), "application/vnd.oci.image.index.v1+json")
	if err != nil {
		return "", "", nil, err
	}
	var index struct {
		Manifests []struct {
			Digest       string            `json:"digest"`
			ArtifactType string            `json:"artifactType"`
			Annotations  map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if json.Unmarshal(body, &index) != nil {
		return "", "", nil, fmt.Errorf("%w: OCI referrers", ErrInvalid)
	}
	var provenance, sbom string
	var evidence []RecipeEvidence
	for _, descriptor := range index.Manifests {
		if !digestPattern.MatchString(descriptor.Digest) {
			continue
		}
		kind := strings.ToLower(descriptor.ArtifactType)
		for _, value := range descriptor.Annotations {
			kind += " " + strings.ToLower(value)
		}
		switch {
		case strings.Contains(kind, "sbom") || strings.Contains(kind, "spdx") || strings.Contains(kind, "cyclonedx"):
			if sbom == "" {
				sbom = descriptor.Digest
				evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: sbom, Detail: "immutable SBOM referrer"})
			}
		case strings.Contains(kind, "provenance") || strings.Contains(kind, "slsa") || strings.Contains(kind, "in-toto"):
			if provenance == "" {
				provenance = descriptor.Digest
				evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: provenance, Detail: "immutable provenance referrer"})
			}
		}
	}
	if provenance == "" || sbom == "" {
		return "", "", nil, fmt.Errorf("%w: OCI provenance and SBOM are required", ErrUnauthorized)
	}
	return provenance, sbom, evidence, nil
}

func (c ociRegistryClient) embeddedAttestations(ctx context.Context, registry, repository string, manifests []any, subject string) (string, string, []RecipeEvidence, error) {
	var provenance, sbom string
	var evidence []RecipeEvidence
	for _, raw := range manifests {
		descriptor, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		annotations := anyStringMap(descriptor["annotations"])
		if firstString(annotations, "vnd.docker.reference.digest") != subject || firstString(annotations, "vnd.docker.reference.type") != "attestation-manifest" {
			continue
		}
		digest := firstString(descriptor, "digest")
		if !digestPattern.MatchString(digest) {
			continue
		}
		body, _, err := c.get(ctx, registry, "/v2/"+repository+"/manifests/"+url.PathEscape(digest), "application/vnd.oci.image.manifest.v1+json")
		if err != nil {
			continue
		}
		var document struct {
			Layers []struct {
				MediaType   string            `json:"mediaType"`
				Annotations map[string]string `json:"annotations"`
			} `json:"layers"`
		}
		if json.Unmarshal(body, &document) != nil {
			continue
		}
		kind := ""
		for _, layer := range document.Layers {
			kind += " " + strings.ToLower(layer.MediaType)
			for key, value := range layer.Annotations {
				kind += " " + strings.ToLower(key) + " " + strings.ToLower(value)
			}
		}
		if sbom == "" && (strings.Contains(kind, "sbom") || strings.Contains(kind, "spdx") || strings.Contains(kind, "cyclonedx")) {
			sbom = digest
			evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: sbom, Detail: "immutable embedded SBOM attestation"})
		}
		if provenance == "" && (strings.Contains(kind, "provenance") || strings.Contains(kind, "slsa") || strings.Contains(kind, "in-toto")) {
			provenance = digest
			evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: provenance, Detail: "immutable embedded provenance attestation"})
		}
	}
	if provenance == "" || sbom == "" {
		return "", "", nil, fmt.Errorf("%w: OCI provenance and SBOM are required", ErrUnauthorized)
	}
	return provenance, sbom, evidence, nil
}

func labelsAny(labels map[string]string) map[string]any {
	result := make(map[string]any, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}

func catalogQuery(raw string, values map[string]string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("%w: catalog endpoint", ErrInvalid)
	}
	query := parsed.Query()
	for key, value := range values {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func fetchCatalogJSON(ctx context.Context, client *http.Client, endpoint string, headers http.Header, limit int64) ([]byte, http.Header, error) {
	client = catalogHTTPClient(client)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if response.StatusCode == http.StatusNotFound {
		return nil, http.Header{"X-Catalog-Not-Found": []string{"1"}}, nil
	}
	if readErr != nil || int64(len(body)) > limit {
		return nil, response.Header, fmt.Errorf("%w: oversized catalog response", ErrInvalid)
	}
	if response.StatusCode != http.StatusOK {
		return nil, response.Header, fmt.Errorf("%w: catalog status %d", ErrInvalid, response.StatusCode)
	}
	if !json.Valid(body) {
		return nil, response.Header, fmt.Errorf("%w: invalid catalog JSON", ErrInvalid)
	}
	return body, response.Header, nil
}

func catalogHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
