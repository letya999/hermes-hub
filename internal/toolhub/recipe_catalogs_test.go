package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type catalogRewriteTransport struct{ host string }

func (t catalogRewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.URL.Scheme = "http"
	request.URL.Host = t.host
	return http.DefaultTransport.RoundTrip(request)
}

func TestRecipeCatalogsFromEnvIsExplicitAndDoesNotAcceptUnknownAdapters(t *testing.T) {
	t.Setenv("HUB_RECIPE_CATALOGS", "")
	if catalogs, err := RecipeCatalogsFromEnv(); err != nil || catalogs != nil {
		t.Fatalf("empty catalog configuration: %#v %v", catalogs, err)
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "mcp-registry,toolhive,docker-mcp,mcp-registry")
	catalogs, err := RecipeCatalogsFromEnv()
	if err != nil || len(catalogs) != 3 {
		t.Fatalf("fixed catalog adapters: %#v %v", catalogs, err)
	}
	t.Setenv("HUB_SMITHERY_TOKEN_ENV", "TEST_SMITHERY_TOKEN")
	t.Setenv("TEST_SMITHERY_TOKEN", "fixture-token")
	t.Setenv("HUB_RECIPE_CATALOGS", "all")
	catalogs, err = RecipeCatalogsFromEnv()
	if err != nil || len(catalogs) != 6 {
		t.Fatalf("all catalog adapters: %#v %v", catalogs, err)
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "unknown")
	if _, err := RecipeCatalogsFromEnv(); err == nil {
		t.Fatal("unknown catalog accepted")
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "smithery")
	t.Setenv("TEST_SMITHERY_TOKEN", "")
	if _, err := RecipeCatalogsFromEnv(); err == nil {
		t.Fatal("Smithery without token accepted")
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "ghcr")
	t.Setenv("HUB_GHCR_TOKEN_ENV", "not-an-env-name")
	if _, err := RecipeCatalogsFromEnv(); err == nil {
		t.Fatal("invalid GHCR token environment accepted")
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "smithery")
	t.Setenv("TEST_SMITHERY_TOKEN", "fixture-token")
	catalogs, err = RecipeCatalogsFromEnv()
	if err != nil || len(catalogs) != 1 {
		t.Fatalf("Smithery opt-in: %#v %v", catalogs, err)
	}
	encoded, _ := json.Marshal(catalogs)
	if strings.Contains(string(encoded), "fixture-token") {
		t.Fatal("catalog token entered adapter metadata")
	}
}

func TestRecipeCatalogsFindDockerHubAndGHCRImagesByOwnerName(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const manifestDigest = "sha256:" + "a" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const configDigest = "sha256:" + "b" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const provenanceDigest = "sha256:" + "c" + "ccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const sbomDigest = "sha256:" + "d" + "ddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/namespaces/acme/repositories":
			_, _ = w.Write([]byte(`{"results":[{"name":"weather"}]}`))
		case r.URL.Path == "/v2/namespaces/acme/repositories/weather/tags":
			_, _ = w.Write([]byte(`{"results":[{"name":"latest"}]}`))
		case r.URL.Path == "/orgs/acme/packages":
			_, _ = w.Write([]byte(`[{"name":"weather","package_type":"container"}]`))
		case r.URL.Path == "/orgs/acme/packages/container/weather/versions":
			_, _ = w.Write([]byte(`[{"name":"` + manifestDigest + `","metadata":{"container":{"tags":["latest"]}}}]`))
		case strings.Contains(r.URL.Path, "/referrers/"):
			_, _ = w.Write([]byte(`{"manifests":[{"digest":"` + provenanceDigest + `","artifactType":"application/vnd.in-toto+json"},{"digest":"` + sbomDigest + `","artifactType":"application/spdx+json"}]}`))
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`))
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write([]byte(`{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/acme/weather","org.opencontainers.image.revision":"` + commit + `","org.opencontainers.image.version":"1.0.0"},"Entrypoint":["/app/server"]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", CommitSHA: commit, Owner: "acme", Name: "weather", VersionHints: []string{"1.0.0"}}
	docker, err := (DockerHubCatalog{Endpoint: server.URL, RegistryBase: server.URL, Client: server.Client()}).Lookup(context.Background(), lookup)
	if err != nil || len(docker) != 1 || docker[0].Launch.Digest != manifestDigest {
		t.Fatalf("Docker Hub catalog: %#v %v", docker, err)
	}
	ghcr, err := (GitHubPackagesCatalog{Endpoint: server.URL, RegistryBase: server.URL, Client: server.Client()}).Lookup(context.Background(), lookup)
	if err != nil || len(ghcr) != 1 || ghcr[0].Launch.Artifact != "ghcr.io/acme/weather" || ghcr[0].Launch.Digest != manifestDigest {
		t.Fatalf("GHCR catalog: %#v %v", ghcr, err)
	}
}

func TestMCPRegistryCatalogMatchesRepositoryCommitAndSubfolder(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", Subfolder: "servers/weather", CommitSHA: commit}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "weather" || r.URL.Query().Get("version") != "latest" {
			t.Fatalf("registry query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"servers":[{"server":{"name":"weather","repository":{"url":"https://github.com/acme/weather","subfolder":"servers/weather"},"remotes":[{"url":"https://mcp.example/weather"}],"_meta":{"buildInfo":{"commit":"` + commit + `"}}}}]}`))
	}))
	defer server.Close()
	candidates, err := (MCPRegistryCatalog{Endpoint: server.URL, Client: server.Client()}).Lookup(context.Background(), RecipeLookup{Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: commit, Owner: "acme", Name: "weather"})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("registry candidates: %#v %v", candidates, err)
	}
	if candidates[0].Launch.Transport != RemoteMCP || candidates[0].Launch.Endpoint != "https://mcp.example/weather" || candidates[0].CommitSHA != commit {
		t.Fatalf("registry candidate: %+v", candidates[0])
	}
	wrong, ok, err := serverDocumentCandidate(map[string]any{"repository": "https://github.com/acme/other", "remotes": []any{map[string]any{"url": "https://mcp.example/weather"}}}, "registry", RecipeLookup{Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: commit})
	if err != nil || ok || wrong.Launch.Endpoint != "" {
		t.Fatalf("name-only/wrong repository candidate accepted: %+v %v %v", wrong, ok, err)
	}
}

func TestToolHiveCatalogHandlesNotFoundAndRepositoryMatchedRemote(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"repository":{"url":"https://github.com/acme/weather"},"remotes":[{"url":"https://mcp.example/weather"}],"metadata":{"commit":"` + commit + `"}}`))
	}))
	defer server.Close()
	catalog := ToolHiveCatalog{Endpoint: server.URL, Client: server.Client()}
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", CommitSHA: commit, Owner: "acme", Name: "weather"}
	if candidates, err := catalog.Lookup(context.Background(), lookup); err != nil || len(candidates) != 0 {
		t.Fatalf("ToolHive 404: %#v %v", candidates, err)
	}
	candidates, err := catalog.Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 1 || candidates[0].Launch.Endpoint == "" {
		t.Fatalf("ToolHive remote: %#v %v", candidates, err)
	}
}

func TestSmitheryCatalogUsesBearerOnlyForRequestsAndMatchesRelease(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	var sawAuth int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fixture-token" {
			sawAuth++
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/servers"):
			_, _ = w.Write([]byte(`{"servers":[{"qualifiedName":"acme/weather"}]}`))
		case strings.HasSuffix(r.URL.Path, "/releases"):
			_, _ = w.Write([]byte(`[{"id":"v1","commit":"` + commit + `","upstreamUrl":"https://github.com/acme/weather/tree/` + commit + `"}]`))
		default:
			_, _ = w.Write([]byte(`{"connections":[{"type":"http","deploymentUrl":"https://mcp.example/weather","configSchema":{"properties":{"API_TOKEN":{"type":"string"}},"required":["API_TOKEN"]}}]}`))
		}
	}))
	defer server.Close()
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", CommitSHA: commit, Owner: "acme", Name: "weather"}
	candidates, err := (SmitheryCatalog{Endpoint: server.URL, Token: "fixture-token", Client: server.Client()}).Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Smithery candidates: %#v %v", candidates, err)
	}
	if candidates[0].Launch.Transport != RemoteMCP || candidates[0].Connection.Fields[0].Name != "API_TOKEN" || candidates[0].Connection.Fields[0].Required != true {
		t.Fatalf("Smithery candidate: %+v", candidates[0])
	}
	if sawAuth != 3 {
		t.Fatalf("Smithery authorization was not scoped to catalog calls: %d", sawAuth)
	}
}

func TestInspectPublishedOCIRequiresImmutableProvenanceAndSBOM(t *testing.T) {
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const configDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const provenanceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const sbomDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	manifest := `{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`
	config := `{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/acme/weather","org.opencontainers.image.revision":"` + commit + `","org.opencontainers.image.version":"1.2.3"},"Entrypoint":["/app/server"],"Cmd":[]}}`
	referrers := `{"manifests":[{"digest":"` + provenanceDigest + `","artifactType":"application/vnd.in-toto+json"},{"digest":"` + sbomDigest + `","artifactType":"application/spdx+json"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = w.Write([]byte(manifest))
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write([]byte(config))
		case strings.Contains(r.URL.Path, "/referrers/"):
			_, _ = w.Write([]byte(referrers))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	proof, err := inspectPublishedOCIAt(context.Background(), server.Client(), server.URL, "ghcr.io/acme/weather:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if proof.ManifestDigest != manifestDigest || proof.Source != "https://github.com/acme/weather" || proof.Revision != commit || proof.ProvenanceDigest != provenanceDigest || proof.SBOMDigest != sbomDigest || proof.Image != "ghcr.io/acme/weather" || len(proof.Entrypoint) != 1 {
		t.Fatalf("OCI proof: %+v", proof)
	}
	lookup := RecipeLookup{Repository: proof.Source, CommitSHA: commit, VersionHints: []string{"1.2.3"}}
	candidate := RecipeCandidate{Repository: proof.Source, CommitSHA: commit, Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/acme/weather:1.2.3", Digest: manifestDigest}}
	verified, accepted, err := verifyRecipeCandidateAt(context.Background(), server.Client(), server.URL, candidate, lookup)
	if err != nil || !accepted || verified.Launch.Digest != manifestDigest || verified.Launch.Entrypoint[0] != "/app/server" {
		t.Fatalf("exact OCI candidate: %+v accepted=%t err=%v", verified, accepted, err)
	}
	candidate.Launch.Digest = "sha256:" + strings.Repeat("e", 64)
	if _, accepted, err := verifyRecipeCandidateAt(context.Background(), server.Client(), server.URL, candidate, lookup); accepted || !errors.Is(err, ErrStale) {
		t.Fatalf("changed OCI digest accepted: accepted=%t err=%v", accepted, err)
	}
	candidate.Launch.Digest = manifestDigest
	lookup.CommitSHA = strings.Repeat("f", 40)
	if _, accepted, err := verifyRecipeCandidateAt(context.Background(), server.Client(), server.URL, candidate, lookup); accepted || err != nil {
		t.Fatalf("foreign source revision accepted: accepted=%t err=%v", accepted, err)
	}
	lookup.CommitSHA = commit
	lookup.VersionHints = []string{"2.0.0"}
	if _, accepted, err := verifyRecipeCandidateAt(context.Background(), server.Client(), server.URL, candidate, lookup); accepted || err != nil {
		t.Fatalf("unreviewed OCI version accepted: accepted=%t err=%v", accepted, err)
	}
	lookup.VersionHints = nil
	candidate.CommitSHA = strings.Repeat("f", 40)
	if _, accepted, err := verifyRecipeCandidateAt(context.Background(), server.Client(), server.URL, candidate, lookup); accepted || err != nil {
		t.Fatalf("candidate revision drift accepted: accepted=%t err=%v", accepted, err)
	}
	if _, err := inspectPublishedOCIAt(context.Background(), server.Client(), server.URL, "ghcr.io/acme/weather@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); err == nil {
		t.Fatal("OCI digest drift accepted")
	}
}

func TestInspectPublishedOCIUsesDockerEmbeddedAttestationsAndURLLabel(t *testing.T) {
	const (
		indexDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		childDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		configDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		provenance   = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		sbom         = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		commit       = "82064568802e542c3924560aef2cb421b4ce436c"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/referrers/"):
			_, _ = w.Write([]byte(`{"manifests":[]}`))
		case strings.Contains(r.URL.Path, "/manifests/"+provenance):
			_, _ = w.Write([]byte(`{"layers":[{"mediaType":"application/vnd.in-toto+json","annotations":{"in-toto.io/predicate-type":"https://slsa.dev/provenance/v0.2"}}]}`))
		case strings.Contains(r.URL.Path, "/manifests/"+sbom):
			_, _ = w.Write([]byte(`{"layers":[{"mediaType":"application/vnd.in-toto+json","annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}}]}`))
		case strings.Contains(r.URL.Path, "/manifests/"+childDigest):
			_, _ = w.Write([]byte(`{"config":{"digest":"` + configDigest + `"}}`))
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", indexDigest)
			_, _ = w.Write([]byte(`{"manifests":[{"digest":"` + childDigest + `","platform":{"os":"linux","architecture":"amd64"}},{"digest":"` + provenance + `","platform":{"os":"unknown","architecture":"unknown"},"annotations":{"vnd.docker.reference.digest":"` + childDigest + `","vnd.docker.reference.type":"attestation-manifest"}},{"digest":"` + sbom + `","platform":{"os":"unknown","architecture":"unknown"},"annotations":{"vnd.docker.reference.digest":"` + childDigest + `","vnd.docker.reference.type":"attestation-manifest"}}]}`))
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write([]byte(`{"config":{"Labels":{"org.opencontainers.image.url":"https://github.com/modelcontextprotocol/servers","org.opencontainers.image.revision":"` + commit + `"},"Entrypoint":["node","dist/index.js"]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	proof, err := inspectPublishedOCIAt(context.Background(), server.Client(), server.URL, "docker.io/mcp/sequentialthinking:latest")
	if err != nil {
		t.Fatal(err)
	}
	if proof.Source != "https://github.com/modelcontextprotocol/servers" || proof.Revision != commit || proof.ProvenanceDigest != provenance || proof.SBOMDigest != sbom || proof.ManifestDigest != indexDigest {
		t.Fatalf("embedded OCI proof: %+v", proof)
	}
}

func TestOCIRegistryBearerChallengeDoesNotLeakToken(t *testing.T) {
	const token = "fixture-bearer-token"
	called := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
			return
		}
		called++
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="fixture",scope="repository:acme/weather:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
	}))
	defer server.Close()
	registry := ociRegistryClient{HTTP: server.Client(), RegistryBase: server.URL}
	_, _, err := registry.get(context.Background(), "ghcr.io", "/v2/acme/weather/manifests/latest", "application/json")
	if err != nil || called != 2 {
		t.Fatalf("OCI bearer flow: calls=%d err=%v", called, err)
	}
	if err != nil && strings.Contains(err.Error(), token) {
		t.Fatal("OCI token leaked in error")
	}
}

func TestDockerMCPCatalogRejectsUncorrelatedEntriesAndHelpersStayBounded(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"registry":{"wrong":{"source":"https://github.com/other/weather/tree/` + commit + `","image":"ghcr.io/other/weather:latest"},"invalid":{"source":"not-a-github-url","image":"ghcr.io/acme/weather:latest"}}}`))
	}))
	defer server.Close()
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", CommitSHA: commit, Owner: "acme", Name: "weather"}
	candidates, err := (DockerMCPCatalog{Endpoint: server.URL, Client: server.Client()}).Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("uncorrelated Docker catalog entry accepted: %#v %v", candidates, err)
	}
	recipe := connectionFromDockerEntry(map[string]any{
		"secrets": []any{map[string]any{"env": "API_TOKEN"}},
		"config":  map[string]any{"properties": map[string]any{"API_REGION": map[string]any{"type": "string"}}, "required": []any{"API_REGION"}},
	})
	if len(recipe.Fields) != 2 || recipe.Fields[0].Name != "API_REGION" || recipe.Fields[1].Name != "API_TOKEN" {
		t.Fatalf("Docker connection metadata: %+v", recipe)
	}
	if _, err := parseOCIReference("ghcr.io/acme/../weather:latest"); err == nil {
		t.Fatal("unsafe OCI path accepted")
	}
	if got := chooseOCIManifest([]any{map[string]any{"digest": "sha256:" + strings.Repeat("a", 64), "platform": map[string]any{"os": "windows", "architecture": "amd64"}}}); got != "" {
		t.Fatalf("non-Linux OCI platform selected: %q", got)
	}
	commitLookup := RecipeLookup{Repository: lookup.Repository, CommitSHA: commit}
	remote := RecipeCandidate{Repository: lookup.Repository, CommitSHA: commit, Launch: LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example/weather"}}
	if completed, ok := completeRecipeCandidate(context.Background(), server.Client(), remote, commitLookup); !ok || completed.Launch.Endpoint == "" {
		t.Fatalf("verified remote candidate rejected: %+v %v", completed, ok)
	}
	container := remote
	container.Launch = LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/acme/weather:latest"}
	if _, ok := completeRecipeCandidate(context.Background(), server.Client(), container, commitLookup); ok {
		t.Fatal("container without OCI proof accepted")
	}
}

func TestRecipeCatalogHTTPBoundsAndManifestVariants(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", Subfolder: "pkg", CommitSHA: commit}
	manifest := map[string]any{
		"repository": map[string]any{"url": lookup.Repository, "subfolder": "pkg"},
		"packages":   []any{map[string]any{"registryType": "npm", "identifier": "weather", "transport": map[string]any{"type": "stdio"}, "runtimeHint": "node", "runtimeArguments": []any{map[string]any{"value": "server.js"}}, "environmentVariables": []any{map[string]any{"name": "API_TOKEN", "isRequired": true, "isSecret": true}}}},
	}
	data, _ := json.Marshal(manifest)
	launch, connection, ok, err := parseServerManifest(data, "server.json", ArtifactSource{Repository: lookup.Repository, Subfolder: lookup.Subfolder, CommitSHA: commit})
	if err != nil || !ok || launch.Transport != BoundedCLI || len(launch.Args) != 1 || len(connection.Fields) != 1 || !connection.Fields[0].Secret {
		t.Fatalf("official manifest variant: %+v %+v %v %v", launch, connection, ok, err)
	}
	if _, err := catalogQuery("https://example.com", map[string]string{"q": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogQuery("file:///tmp/catalog", nil); err == nil {
		t.Fatal("non-HTTP catalog accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			_, _ = w.Write([]byte("not-json"))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	if _, _, err := fetchCatalogJSON(context.Background(), server.Client(), server.URL+"/bad", nil, 1024); err == nil {
		t.Fatal("invalid catalog JSON accepted")
	}
	if _, _, err := fetchCatalogJSON(context.Background(), server.Client(), server.URL+"/status", nil, 1024); err == nil {
		t.Fatal("bad catalog status accepted")
	}
}

func TestRecipeCatalogMetadataHelpersStayConservative(t *testing.T) {
	recipe := connectionFromSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"API_TOKEN": map[string]any{"format": "password"}, "REGION": map[string]any{"type": "string"}},
		"required":   []any{"API_TOKEN"},
	}, "smithery")
	if len(recipe.Fields) != 2 {
		t.Fatalf("schema fields: %+v", recipe)
	}
	for _, field := range recipe.Fields {
		if field.Name == "API_TOKEN" && (!field.Required || !field.Secret || field.Alternative != "smithery") {
			t.Fatalf("secret schema field: %+v", field)
		}
	}
	camel := connectionFromSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"braveApiKey": map[string]any{"type": "string"}},
		"required":   []any{"braveApiKey"},
	}, "smithery")
	if len(camel.Fields) != 1 || camel.Fields[0].Name != "BRAVE_API_KEY" || camel.Fields[0].Target != "braveApiKey" || !camel.Fields[0].Secret || !camel.Fields[0].Required {
		t.Fatalf("camelCase schema field: %+v", camel)
	}
	if repository, subfolder := smitheryRepository("https://github.com/acme/weather/tree/0123456789abcdef0123456789abcdef01234567/pkg"); repository != "https://github.com/acme/weather" || subfolder != "pkg" {
		t.Fatalf("GitHub source parsing: %q %q", repository, subfolder)
	}
	if repository, subfolder := smitheryRepository("not-a-repository"); repository != "" || subfolder != "" {
		t.Fatalf("invalid source accepted: %q %q", repository, subfolder)
	}
	if got := exactCommitMetadata(map[string]any{"nested": []any{map[string]any{"revision": "0123456789abcdef0123456789abcdef01234567"}}}); got == "" {
		t.Fatal("nested commit metadata was not found")
	}
}

func TestInspectPublishedOCIRejectsMalformedReferenceBeforeNetwork(t *testing.T) {
	if _, err := inspectPublishedOCI(context.Background(), nil, "not an image"); err == nil {
		t.Fatal("malformed OCI reference accepted")
	}
}

func TestOCIHTTPClientRedirectPolicy(t *testing.T) {
	fallback := catalogHTTPClient(nil)
	if fallback == nil || fallback.CheckRedirect(httptest.NewRequest(http.MethodGet, "https://example.com", nil), nil) != http.ErrUseLastResponse || catalogHTTPClient(http.DefaultClient) != http.DefaultClient {
		t.Fatal("catalog HTTP client fallback is not stable")
	}
	namespace, name := splitDockerHubRepository("weather", "acme")
	if !containsFold([]string{"v1.2.3"}, "1.2.3") || namespace != "acme" || name != "weather" {
		t.Fatal("catalog helper normalization is not stable")
	}
	if got := appendUnique([]string{"latest"}, "latest"); len(got) != 1 || orderedTags([]string{"bad tag", "latest"}, []string{"latest"})[0] != "latest" {
		t.Fatal("catalog tag normalization is not stable")
	}
	tests := []struct {
		name     string
		registry string
		url      string
		allowed  bool
	}{
		{name: "registry", registry: "ghcr.io", url: "https://ghcr.io/v2/acme/weather", allowed: true},
		{name: "docker auth", registry: "registry-1.docker.io", url: "https://auth.docker.io/token", allowed: true},
		{name: "docker blob", registry: "registry-1.docker.io", url: "https://abc.cloudfront.docker.com/blob", allowed: true},
		{name: "ghcr blob", registry: "ghcr.io", url: "https://objects.githubusercontent.com/blob", allowed: true},
		{name: "wrong host", registry: "ghcr.io", url: "https://example.com/blob", allowed: false},
		{name: "wrong scheme", registry: "ghcr.io", url: "http://ghcr.io/blob", allowed: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := ociHTTPClient(tc.registry, tc.registry)
			err := client.CheckRedirect(httptest.NewRequest(http.MethodGet, tc.url, nil), nil)
			if tc.allowed && err != nil {
				t.Fatalf("allowed redirect rejected: %v", err)
			}
			if !tc.allowed && err != http.ErrUseLastResponse {
				t.Fatalf("unsafe redirect accepted: %v", err)
			}
		})
	}
}

func TestCatalogAdaptersRejectMalformedRemoteDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "catalog.json"):
			_, _ = w.Write([]byte(`{"registry":[]}`))
		case strings.Contains(r.URL.Path, "/servers"):
			_, _ = w.Write([]byte("not-json"))
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer server.Close()
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("a", 40), Owner: "acme", Name: "weather"}
	if _, err := (MCPRegistryCatalog{Endpoint: server.URL, Client: server.Client()}).Lookup(context.Background(), lookup); err == nil {
		t.Fatal("malformed MCP Registry response accepted")
	}
	if _, err := (DockerMCPCatalog{Endpoint: server.URL + "/catalog.json", Client: server.Client()}).Lookup(context.Background(), lookup); err == nil {
		t.Fatal("malformed Docker catalog accepted")
	}
	if _, err := (SmitheryCatalog{Endpoint: server.URL, Token: "token", Client: server.Client()}).Lookup(context.Background(), lookup); err == nil {
		t.Fatal("malformed Smithery response accepted")
	}
	if _, err := (ToolHiveCatalog{Endpoint: server.URL + "/status", Client: server.Client()}).Lookup(context.Background(), lookup); err == nil {
		t.Fatal("bad ToolHive catalog status accepted")
	}
}

func TestInspectPublishedOCIRejectsMissingRuntimeProof(t *testing.T) {
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const configDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name   string
		config string
		refs   string
	}{
		{name: "labels", config: `{"config":{"Labels":{},"Entrypoint":["/app/server"]}}`, refs: `{"manifests":[]}`},
		{name: "attestations", config: `{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/acme/weather","org.opencontainers.image.revision":"` + commit + `"},"Entrypoint":["/app/server"]}}`, refs: `{"manifests":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/manifests/"):
					w.Header().Set("Docker-Content-Digest", manifestDigest)
					_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`))
				case strings.Contains(r.URL.Path, "/blobs/"):
					_, _ = w.Write([]byte(tc.config))
				case strings.Contains(r.URL.Path, "/referrers/"):
					_, _ = w.Write([]byte(tc.refs))
				}
			}))
			defer server.Close()
			if _, err := inspectPublishedOCIAt(context.Background(), server.Client(), server.URL, "ghcr.io/acme/weather:latest"); err == nil {
				t.Fatal("incomplete OCI proof accepted")
			}
		})
	}
}

func TestOCIRegistryTokenChallengeFailClosed(t *testing.T) {
	registry := ociRegistryClient{}
	if _, err := registry.token(context.Background(), http.DefaultClient, `Basic realm="x"`); err == nil {
		t.Fatal("non-bearer OCI challenge accepted")
	}
	if _, err := registry.token(context.Background(), http.DefaultClient, `Bearer service="s"`); err == nil {
		t.Fatal("OCI challenge without realm accepted")
	}
	if _, err := registry.token(context.Background(), http.DefaultClient, `Bearer realm="://bad"`); err == nil {
		t.Fatal("OCI challenge with malformed realm accepted")
	}
	status := http.StatusOK
	body := `{"token":"tok"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	challenge := `Bearer realm="` + server.URL + `/realm",service="s",scope="repository:acme/weather:pull"`
	status = http.StatusInternalServerError
	if _, err := registry.token(context.Background(), server.Client(), challenge); err == nil {
		t.Fatal("OCI token error status accepted")
	}
	status, body = http.StatusOK, "not-json"
	if _, err := registry.token(context.Background(), server.Client(), challenge); err == nil {
		t.Fatal("malformed OCI token response accepted")
	}
	body = `{"token":""}`
	if _, err := registry.token(context.Background(), server.Client(), challenge); err == nil {
		t.Fatal("empty OCI token accepted")
	}
	body = `{"access_token":"fallback-token"}`
	token, err := registry.token(context.Background(), server.Client(), challenge)
	if err != nil || token != "fallback-token" {
		t.Fatalf("OCI access_token fallback: %q %v", token, err)
	}
}

func TestOCIAttestationsAndEmbeddedAttestationsFailClosed(t *testing.T) {
	const child = "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body := `{"manifests":[{"digest":"bad-digest","artifactType":"sbom"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	registry := ociRegistryClient{HTTP: server.Client(), RegistryBase: server.URL}
	if _, _, _, err := registry.attestations(context.Background(), "ghcr.io", "acme/weather", child); err == nil {
		t.Fatal("referrers without provenance and SBOM accepted")
	}
	body = "{"
	if _, _, _, err := registry.attestations(context.Background(), "ghcr.io", "acme/weather", child); err == nil {
		t.Fatal("malformed OCI referrers accepted")
	}
	if _, _, _, err := registry.embeddedAttestations(context.Background(), "ghcr.io", "acme/weather", []any{
		1,
		"descriptor",
		map[string]any{"digest": child},
		map[string]any{"digest": "bad", "annotations": map[string]any{"vnd.docker.reference.digest": child, "vnd.docker.reference.type": "attestation-manifest"}},
		map[string]any{"digest": child, "annotations": map[string]any{"vnd.docker.reference.digest": child, "vnd.docker.reference.type": "attestation-manifest"}},
	}, child); err == nil {
		t.Fatal("embedded attestation descriptors without provenance and SBOM accepted")
	}
}

func TestOCIReferenceAndCatalogFetchEdgeCases(t *testing.T) {
	for _, ref := range []string{":latest", "acme/", "/repo:latest", "ghcr.io/acme/weather@sha256:zz"} {
		if _, err := parseOCIReference(ref); err == nil {
			t.Fatalf("malformed OCI reference accepted: %q", ref)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/not-found":
			http.NotFound(w, r)
		case "/oversized":
			_, _ = w.Write(make([]byte, 8<<20+8))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer server.Close()
	if _, headers, err := fetchCatalogJSON(context.Background(), server.Client(), server.URL+"/not-found", nil, 1024); err != nil || headers.Get("X-Catalog-Not-Found") == "" {
		t.Fatalf("catalog 404 marker: %v %v", headers, err)
	}
	registry := ociRegistryClient{HTTP: server.Client(), RegistryBase: server.URL}
	if _, _, err := registry.get(context.Background(), "ghcr.io", "/oversized", "application/json"); err == nil {
		t.Fatal("oversized OCI response accepted")
	}
	if _, _, err := registry.get(context.Background(), "ghcr.io", "/status", "application/json"); err == nil {
		t.Fatal("non-OK OCI status accepted")
	}
}

func TestDockerMCPCatalogVerifiesSourceMatchedImageEntry(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const configDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const provenanceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const sbomDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "catalog.json"):
			_, _ = w.Write([]byte(`{"registry":{"weather":{"source":"https://github.com/acme/weather/tree/` + commit + `","image":"ghcr.io/acme/weather:latest","command":["/app/server"]},"empty":{"source":"https://github.com/acme/weather/tree/` + commit + `"},"not-a-map":1}}`))
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`))
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write([]byte(`{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/acme/weather","org.opencontainers.image.revision":"` + commit + `"},"Entrypoint":["/app/server"]}}`))
		case strings.Contains(r.URL.Path, "/referrers/"):
			_, _ = w.Write([]byte(`{"manifests":[{"digest":"` + provenanceDigest + `","artifactType":"application/vnd.in-toto+json"},{"digest":"` + sbomDigest + `","artifactType":"application/spdx+json"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	lookup := RecipeLookup{Repository: "https://github.com/acme/weather", CommitSHA: commit, Owner: "acme", Name: "weather"}
	client := &http.Client{Transport: catalogRewriteTransport{host: server.Listener.Addr().String()}}
	candidates, err := (DockerMCPCatalog{Endpoint: server.URL + "/catalog.json", Client: client}).Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Docker MCP matched entry: %#v %v", candidates, err)
	}
	if candidates[0].Launch.Digest != manifestDigest || len(candidates[0].Launch.Entrypoint) == 0 {
		t.Fatalf("Docker MCP verified candidate: %+v", candidates[0].Launch)
	}
}

func TestDockerHubCatalogFallsBackToRepositoryImageHints(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const configDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const provenanceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const sbomDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`))
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write([]byte(`{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/acme/weather","org.opencontainers.image.revision":"` + commit + `","org.opencontainers.image.version":"1.0.0"},"Entrypoint":["/app/server"]}}`))
		case strings.Contains(r.URL.Path, "/referrers/"):
			_, _ = w.Write([]byte(`{"manifests":[{"digest":"` + provenanceDigest + `","artifactType":"application/vnd.in-toto+json"},{"digest":"` + sbomDigest + `","artifactType":"application/spdx+json"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	lookup := RecipeLookup{
		Repository:   "https://github.com/acme/weather",
		CommitSHA:    commit,
		Owner:        "acme",
		Name:         "weather",
		ImageHints:   []string{"not-an-image", "ghcr.io/acme/weather:latest", "docker.io/acme/weather:1.0.0"},
		VersionHints: []string{"1.0.0"},
	}
	candidates, err := (DockerHubCatalog{Endpoint: server.URL, RegistryBase: server.URL, Client: server.Client()}).Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Docker Hub image hint fallback: %#v %v", candidates, err)
	}
	if candidates[0].Launch.Digest != manifestDigest || candidates[0].Launch.Artifact != "docker.io/acme/weather" {
		t.Fatalf("Docker Hub hint candidate: %+v", candidates[0].Launch)
	}
}

func TestInspectPublishedOCIRejectsMalformedManifestConfigAndLabels(t *testing.T) {
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const configDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, mode := range []string{"manifest", "header", "config-digest", "config-json", "entrypoint", "source", "revision"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/manifests/"):
					if mode == "header" {
						w.Header().Set("Docker-Content-Digest", "bad")
					} else {
						w.Header().Set("Docker-Content-Digest", manifestDigest)
					}
					if mode == "manifest" {
						_, _ = w.Write([]byte("{"))
					} else if mode == "config-digest" {
						_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"bad"}}`))
					} else {
						_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`))
					}
				case strings.Contains(r.URL.Path, "/blobs/"):
					if mode == "config-json" {
						_, _ = w.Write([]byte("{"))
						return
					}
					source := "https://github.com/acme/weather"
					revision := commit
					entrypoint := `"/app/server"`
					if mode == "source" {
						source = "https://github.com/other/weather"
					}
					if mode == "revision" {
						revision = "bad"
					}
					if mode == "entrypoint" {
						entrypoint = `"${UNSAFE}"`
					}
					_, _ = w.Write([]byte(`{"config":{"Labels":{"org.opencontainers.image.source":"` + source + `","org.opencontainers.image.revision":"` + revision + `"},"Entrypoint":[` + entrypoint + `]}}`))
				case strings.Contains(r.URL.Path, "/referrers/"):
					_, _ = w.Write([]byte(`{"manifests":[{"digest":"sha256:` + strings.Repeat("c", 64) + `","artifactType":"in-toto"},{"digest":"sha256:` + strings.Repeat("d", 64) + `","artifactType":"spdx"}]}`))
				}
			}))
			defer server.Close()
			proof, err := inspectPublishedOCIAt(context.Background(), server.Client(), server.URL, "ghcr.io/acme/weather:latest")
			if mode == "source" {
				candidate := RecipeCandidate{Repository: "https://github.com/acme/weather", CommitSHA: commit, Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/acme/weather:latest"}}
				if _, accepted := completeRecipeCandidate(context.Background(), server.Client(), candidate, RecipeLookup{Repository: candidate.Repository, CommitSHA: commit}); accepted {
					t.Fatal("wrong OCI source accepted")
				}
				return
			}
			if err == nil {
				t.Fatalf("malformed OCI metadata accepted: %+v", proof)
			}
		})
	}
}
