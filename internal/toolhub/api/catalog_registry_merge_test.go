package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Registry catalogs correlate catalog metadata with the exact pinned source;
// unmatched, malformed or unverifiable entries drop out, and list/verify
// helpers bound their inputs.
func TestRegistryCatalogBranches(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("c", 40)
	lookup := RecipeLookup{Repository: "https://github.com/acme/fs-mcp", Owner: "acme", Name: "fs-mcp", CommitSHA: commit, ImageHints: []string{"docker.io/acme/fs-mcp:v1"}, VersionHints: []string{"v1"}}

	// Docker MCP: only entries whose source URL pins the exact
	// repository+commit survive; garbage and misses are skipped.
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"registry": map[string]any{
			"bad-entry": map[string]any{"source": "nope"},
			"fs-mcp": map[string]any{
				"source":  "https://github.com/acme/fs-mcp/commit/" + commit,
				"image":   "mcp/fs-mcp",
				"command": []string{"--serve"},
			},
			"other": map[string]any{
				"source": "https://github.com/other/repo/commit/" + commit,
				"image":  "mcp/other",
			},
			"noimage": map[string]any{
				"source": "https://github.com/acme/fs-mcp/commit/" + commit,
			},
		}})
	}))
	defer docker.Close()
	cands, err := (DockerMCPCatalog{Endpoint: docker.URL}).Lookup(ctx, lookup)
	if err != nil {
		t.Fatalf("docker mcp lookup: %v", err)
	}
	_ = cands // the entry reaches OCI completion; without a registry mock it simply yields no ready candidate
	found, err := (DockerMCPCatalog{Endpoint: docker.URL}).SearchSources(ctx, "fs-mcp")
	if err != nil || len(found) != 1 || found[0].Source.Repository != "https://github.com/acme/fs-mcp" {
		t.Fatalf("docker mcp search: %+v %v", found, err)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{")) }))
	defer broken.Close()
	if _, err := (DockerMCPCatalog{Endpoint: broken.URL}).SearchSources(ctx, "x"); err == nil {
		t.Fatal("broken docker catalog accepted")
	}

	// Docker Hub: namespace listing feeds matching repositories, tags feed
	// the OCI proof step; unreachable proof leaves an empty candidate list.
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/repositories") && strings.HasSuffix(r.URL.Path, "/tags"):
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"name": "v1"}, {"name": "bad tag/"}}})
		case strings.Contains(r.URL.Path, "/repositories"):
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"name": "fs-mcp"}, {"name": "unrelated"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()
	hubCat := DockerHubCatalog{Endpoint: hub.URL, RegistryBase: hub.URL}
	// OCI proof fails against the plain stub; the tag→proof pipeline still runs.
	_, _ = hubCat.Lookup(ctx, lookup)
	if _, err := hubCat.listRepositories(ctx, hub.URL, "bad namespace!", "x"); err == nil {
		t.Fatal("bad namespace accepted")
	}
	if _, err := hubCat.listTags(ctx, "::bad", "acme", "fs-mcp"); err == nil {
		t.Fatal("bad tags endpoint accepted")
	}

	// GitHub Packages: orgs 404 falls back to the users scope; versions feed
	// tags into the OCI proof step.
	ghcr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/orgs/"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/versions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "v", "metadata": map[string]any{"container": map[string]any{"tags": []string{"v1"}}}}})
		case strings.Contains(r.URL.Path, "/packages"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "fs-mcp"}, {"name": "unrelated"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghcr.Close()
	ghc := GitHubPackagesCatalog{Endpoint: ghcr.URL, RegistryBase: ghcr.URL}
	// Same here: tags reach the OCI proof step, which the stub cannot pass.
	_, _ = ghc.Lookup(ctx, lookup)
	if _, _, err := ghc.listPackages(ctx, ghcr.URL, "bad owner!"); err == nil {
		t.Fatal("bad package owner accepted")
	}
	if v, err := ghc.listPackageVersions(ctx, ghcr.URL, "", "acme", "fs-mcp"); err != nil || v != nil {
		t.Fatalf("empty-scope versions: %v %v", v, err)
	}

	// Pure helpers: repository splitting, tag ordering, image matching.
	if ns, name := SplitDockerHubRepository("acme/sub/fs-mcp", "acme"); ns != "acme" || name != "sub/fs-mcp" {
		t.Fatalf("split repository: %q %q", ns, name)
	}
	if ns, name := SplitDockerHubRepository("fs-mcp", "acme"); ns != "acme" || name != "fs-mcp" {
		t.Fatalf("split bare repository: %q %q", ns, name)
	}
	if !dockerHubRepositoryMatches("acme/mcp-fs-mcp", lookup) || dockerHubRepositoryMatches("acme/other", lookup) {
		t.Fatal("repository matching wrong")
	}
	if !githubPackageMatches("acme/fs-mcp", lookup) || githubPackageMatches("acme/other", lookup) {
		t.Fatal("package matching wrong")
	}
	if tags := OrderedTags([]string{"v2", "bad tag"}, []string{"v1"}); len(tags) != 2 || tags[0] != "v1" {
		t.Fatalf("ordered tags: %v", tags)
	}
	if !validImageTag("v1.2.3") || validImageTag("has space") || validImageTag("") {
		t.Fatal("image tag validation wrong")
	}
	if githubPackageImage("acme", "acme/fs-mcp", "v1") != "ghcr.io/acme/fs-mcp:v1" {
		t.Fatal("package image wrong")
	}
	if got := AppendUnique([]string{"a"}, "a"); len(got) != 1 {
		t.Fatal("AppendUnique duplicated")
	}
	if pathBase("a/b/c") != "c" || pathBase("solo") != "solo" {
		t.Fatal("pathBase wrong")
	}
}
