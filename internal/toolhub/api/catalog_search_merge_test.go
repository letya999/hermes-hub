package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Catalog adapters reject malformed endpoints, oversized or invalid
// responses, and entries that cannot be pinned to a repository — only
// reviewable, repository-bound metadata survives.
func TestCatalogSearchEdgeBranches(t *testing.T) {
	ctx := context.Background()
	lookup := RecipeLookup{Repository: "https://github.com/acme/fs-mcp", Owner: "acme", Name: "fs-mcp", CommitSHA: strings.Repeat("a", 40)}

	bad := MCPRegistryCatalog{Endpoint: "::bad"}
	if _, err := bad.Lookup(ctx, lookup); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	var status int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch status {
		case http.StatusTeapot:
			http.Error(w, "nope", http.StatusTeapot)
		default:
			w.Write([]byte("not json"))
		}
	}))
	reg := MCPRegistryCatalog{Endpoint: server.URL}
	status = http.StatusTeapot
	if _, err := reg.Lookup(ctx, lookup); err == nil {
		t.Fatal("non-200 catalog accepted")
	}
	status = http.StatusOK
	if _, err := reg.Lookup(ctx, lookup); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := reg.SearchSources(ctx, "fs-mcp"); err == nil {
		t.Fatal("invalid search JSON accepted")
	}
	server.Close()

	// SearchSources: >32 entries and unbindable/mismatched entries drop out.
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servers := make([]map[string]any, 0, 33)
		for i := 0; i < 33; i++ {
			servers = append(servers, map[string]any{"name": "fs-mcp"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": servers})
	}))
	defer oversized.Close()
	if _, err := (MCPRegistryCatalog{Endpoint: oversized.URL}).SearchSources(ctx, "fs-mcp"); err == nil {
		t.Fatal("oversized registry response accepted")
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": []any{
			"garbage",
			map[string]any{"name": "acme/fs-mcp"}, // no repository
			map[string]any{"name": "acme/fs-mcp", "repository": map[string]any{"url": "not-a-url"}},                       // bad repository
			map[string]any{"name": "other/server", "repository": map[string]any{"url": "https://github.com/acme/fs-mcp"}}, // name mismatch
			map[string]any{"server": map[string]any{"name": "acme/fs-mcp", "repository": map[string]any{"url": "https://github.com/acme/fs-mcp", "subfolder": "srv"}}},
		}})
	}))
	defer server.Close()
	found, err := (MCPRegistryCatalog{Endpoint: server.URL}).SearchSources(ctx, "fs-mcp")
	if err != nil || len(found) != 1 || found[0].Source.Subfolder != "srv" || found[0].Source.Repository != "https://github.com/acme/fs-mcp" {
		t.Fatalf("registry search: %+v %v", found, err)
	}
	// Lookup over a non-matching document returns empty, not an error.
	if _, err := (MCPRegistryCatalog{Endpoint: server.URL}).Lookup(ctx, lookup); err != nil {
		t.Fatalf("registry lookup: %v", err)
	}

	// GitHub source search drops forks, private and mislabeled results.
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{
			map[string]any{"full_name": "acme/fs-mcp", "html_url": "https://github.com/acme/fs-mcp", "fork": true},
			map[string]any{"full_name": "acme/fs-mcp", "html_url": "https://github.com/acme/fs-mcp", "private": true},
			map[string]any{"full_name": "acme/fs-mcp", "html_url": "https://gitlab.com/acme/fs-mcp"},
			map[string]any{"full_name": "acme/other", "html_url": "https://github.com/acme/fs-mcp"},
			map[string]any{"full_name": "acme/fs-mcp", "html_url": "https://github.com/acme/fs-mcp"},
		}})
	}))
	defer gh.Close()
	found, err = (GitHubSearchCatalog{Endpoint: gh.URL, Token: "t"}).SearchSources(ctx, "fs-mcp")
	if err != nil || len(found) != 1 || found[0].Name != "acme/fs-mcp" {
		t.Fatalf("github search: %+v %v", found, err)
	}

	// ToolHive: catalog 404 is a quiet miss; the index keeps matching entries.
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	cands, err := (ToolHiveCatalog{Endpoint: notFound.URL}).Lookup(ctx, lookup)
	if err != nil || cands != nil {
		t.Fatalf("toolhive 404: %+v %v", cands, err)
	}
	hive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"servers": []any{
			map[string]any{"title": "Unrelated", "name": "x/y"},
			map[string]any{"title": "fs-mcp server", "name": "acme/fs-mcp"},
			map[string]any{"title": "fs-mcp real", "name": "acme/fs-mcp", "repository": map[string]any{"url": "https://github.com/acme/fs-mcp"}},
		}}})
	}))
	defer hive.Close()
	found, err = (ToolHiveCatalog{IndexEndpoint: hive.URL}).SearchSources(ctx, "fs-mcp")
	if err != nil || len(found) != 1 || found[0].Name != "fs-mcp real" {
		t.Fatalf("toolhive search: %+v %v", found, err)
	}

	// Smithery: the bearer token is mandatory; release matching pins commit.
	if _, err := (SmitheryCatalog{}).Lookup(ctx, lookup); err == nil {
		t.Fatal("smithery lookup without token")
	}
	if _, err := (SmitheryCatalog{}).SearchSources(ctx, "x"); err == nil {
		t.Fatal("smithery search without token")
	}
	smith := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("repoOwner") == "acme" {
			_ = json.NewEncoder(w).Encode(map[string]any{"servers": []any{
				map[string]any{"qualifiedName": "acme/fs-mcp"},
				map[string]any{}, // no qualifiedName
				map[string]any{"qualifiedName": "acme/broken"},
			}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/releases") {
			switch {
			case strings.Contains(r.URL.Path, "broken"):
				http.Error(w, "boom", http.StatusInternalServerError)
			default:
				_ = json.NewEncoder(w).Encode([]any{
					map[string]any{"commit": "not-a-sha"},
					map[string]any{"commit": strings.Repeat("b", 40), "upstreamUrl": "https://github.com/acme/other"},
					map[string]any{"commit": lookup.CommitSHA, "upstreamUrl": "https://github.com/acme/fs-mcp", "version": "1.2.3"},
				})
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"connections": []any{
			map[string]any{"type": "http", "deploymentUrl": "https://mcp.acme.dev/x"},
			map[string]any{"type": "stdio", "bundleUrl": "oci://img@sha256:abc"},
		}})
	}))
	defer smith.Close()
	cands, err = (SmitheryCatalog{Endpoint: smith.URL, Token: "tok"}).Lookup(ctx, lookup)
	if err != nil || len(cands) != 1 || cands[0].CommitSHA != lookup.CommitSHA {
		t.Fatalf("smithery lookup: %+v %v", cands, err)
	}
	sources, err := (SmitheryCatalog{Endpoint: smith.URL, Token: "tok"}).SearchSources(ctx, "fs-mcp")
	if err != nil {
		t.Fatalf("smithery search: %v", err)
	}
	_ = sources

	if _, ok := discoverySource("not a repository"); ok {
		t.Fatal("garbage repository accepted")
	}
	repo, sub := SmitheryRepository("https://github.com/acme/fs-mcp/tree/" + lookup.CommitSHA + "/srv")
	if repo != "https://github.com/acme/fs-mcp" || sub != "srv" {
		t.Fatalf("smithery repository: %q %q", repo, sub)
	}
	if repo, _ := SmitheryRepository("://bad"); repo != "" {
		t.Fatalf("invalid smithery repository: %q", repo)
	}
	candidate := smitheryCandidate([]byte(`{"connections":[{"type":"http","deploymentUrl":"https://dep.example/mcp"}]}`), map[string]any{"version": "2"}, "https://github.com/acme/fs-mcp", "", lookup.CommitSHA)
	if candidate.Launch.Transport != RemoteMCP || candidate.Launch.Endpoint == "" {
		t.Fatalf("smithery candidate: %+v", candidate)
	}
	if fmt.Sprint(candidate) == "" {
		t.Fatal("smithery candidate formats empty")
	}
}

func TestProducerRanks(t *testing.T) {
	lookup := RecipeLookup{Repository: "https://github.com/acme/fs-mcp"}
	document := map[string]any{"name": "acme/fs-mcp", "repository": map[string]any{"url": "https://github.com/acme/fs-mcp"}, "remotes": []any{map[string]any{"url": "https://mcp.example/fs"}}}
	for _, tc := range []struct {
		source    string
		launch    Rank
		credential Rank
	}{
		{"mcp-registry", RankL3, RankR2},
		{"toolhive", RankL2, RankR4},
		{"docker-mcp", RankL2, RankR4},
	} {
		candidate, ok, err := ServerDocumentCandidate(document, tc.source, lookup)
		if err != nil || !ok {
			t.Fatalf("%s: candidate lost: ok=%v err=%v", tc.source, ok, err)
		}
		if candidate.Source != tc.source || candidate.LaunchRank != tc.launch || candidate.CredentialRank != tc.credential {
			t.Fatalf("%s: source=%q launch=%s credential=%s", tc.source, candidate.Source, candidate.LaunchRank, candidate.CredentialRank)
		}
	}
}
