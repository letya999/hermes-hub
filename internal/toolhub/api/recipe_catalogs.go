package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	officialMCPRegistryURL = "https://registry.modelcontextprotocol.io/v0.1/servers"
	toolHiveCatalogURL     = "https://raw.githubusercontent.com/stacklok/toolhive-catalog/main/registries/toolhive/servers"
	toolHiveIndexURL       = "https://raw.githubusercontent.com/stacklok/toolhive-catalog/main/pkg/catalog/toolhive/data/registry-upstream.json"
	dockerMCPCatalogURL    = "https://desktop.docker.com/mcp/catalog/v3/catalog.json"
	smitheryAPIURL         = "https://api.smithery.ai"
	dockerHubAPIURL        = "https://hub.docker.com"
	githubAPIURL           = "https://api.github.com"
)

var allRecipeCatalogNames = []string{"mcp-registry", "toolhive", "docker-mcp", "smithery", "docker-hub", "ghcr", "github-search"}

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
				if !CredentialPattern.MatchString(envName) {
					return nil, fmt.Errorf("%w: GHCR token environment", ErrInvalid)
				}
				token = os.Getenv(envName)
			}
			catalogs = append(catalogs, GitHubPackagesCatalog{Endpoint: githubAPIURL, Token: token})
		case "github-search":
			token := ""
			if envName := strings.TrimSpace(os.Getenv("HUB_GITHUB_SEARCH_TOKEN_ENV")); envName != "" {
				if !CredentialPattern.MatchString(envName) {
					return nil, fmt.Errorf("%w: GitHub search token environment", ErrInvalid)
				}
				token = os.Getenv(envName)
			}
			catalogs = append(catalogs, GitHubSearchCatalog{Endpoint: githubAPIURL, Token: token})
		case "smithery":
			envName := strings.TrimSpace(os.Getenv("HUB_SMITHERY_TOKEN_ENV"))
			if envName == "" {
				envName = "SMITHERY_API_KEY"
			}
			if !CredentialPattern.MatchString(envName) || os.Getenv(envName) == "" {
				return nil, fmt.Errorf("%w: Smithery token environment", ErrUnauthorized)
			}
			catalogs = append(catalogs, SmitheryCatalog{Endpoint: smitheryAPIURL, Token: os.Getenv(envName)})
		default:
			return nil, fmt.Errorf("%w: unknown recipe catalog %q", ErrInvalid, name)
		}
	}
	return catalogs, nil
}

func (GitHubSearchCatalog) Lookup(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
	return nil, nil
}

func ConnectionFromSchema(raw any, alternative string) ConnectionRecipe {
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
		field := ConnectionField{Name: fieldName, Type: ConnectionType(fieldName), Required: required[name], Secret: SecretName(fieldName), Delivery: "json", Target: name, Alternative: alternative}
		if descriptor, ok := value.(map[string]any); ok {
			if descriptor["secret"] == true || strings.EqualFold(firstString(descriptor, "format", "type"), "password") {
				field.Secret = true
			}
		}
		recipe.Fields = append(recipe.Fields, field)
	}
	return normalizeConnection(recipe)
}

func ConnectionFromDockerEntry(entry map[string]any) ConnectionRecipe {
	var recipe ConnectionRecipe
	if secrets, ok := entry["secrets"].([]any); ok {
		for _, raw := range secrets {
			value, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := firstString(value, "env", "name")
			if EnvKey(name) != "" {
				recipe.Fields = append(recipe.Fields, ConnectionField{Name: name, Type: "secret", Required: true, Secret: true, Delivery: "env", Target: name})
			}
		}
	}
	if config := entry["config"]; config != nil {
		recipe = mergeConnection(recipe, ConnectionFromSchema(config, "docker-mcp"))
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
		candidate, ok, err := ServerDocumentCandidate(document, sourceName, lookup)
		if err != nil || !ok {
			continue
		}
		if completed, accepted := CompleteRecipeCandidate(ctx, client, candidate, lookup); accepted {
			candidates = append(candidates, completed)
		} else if candidate.CommitSHA != "" && strings.EqualFold(candidate.CommitSHA, lookup.CommitSHA) && candidate.Launch.Transport != ContainerMCP {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func ServerDocumentCandidate(document map[string]any, sourceName string, lookup RecipeLookup) (RecipeCandidate, bool, error) {
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
	launch, connection, ok, err := ParseServerManifest(encoded, sourceName, source)
	if err != nil || !ok {
		return RecipeCandidate{}, false, err
	}
	commit := ExactCommitMetadata(document)
	evidence := []RecipeEvidence{{Source: sourceName, Detail: "repository-matched catalog metadata"}}
	return RecipeCandidate{Repository: repository, Subfolder: subfolder, CommitSHA: commit, Launch: launch, Connection: connection, Evidence: evidence}, true, nil
}

func CatalogQuery(raw string, values map[string]string) (string, error) {
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

func FetchCatalogJSON(ctx context.Context, client *http.Client, endpoint string, headers http.Header, limit int64) ([]byte, http.Header, error) {
	client = CatalogHTTPClient(client)
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

func CatalogHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
