//go:build integration

package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestRealMCPRegistryNameSearch(t *testing.T) {
	if os.Getenv("HUB_REAL_REGISTRY_SEARCH") != "1" {
		t.Skip("set HUB_REAL_REGISTRY_SEARCH=1 for public MCP Registry name search")
	}
	control := &ControlPlane{Now: time.Now, RecipeCatalogs: []RecipeCatalog{MCPRegistryCatalog{Endpoint: officialMCPRegistryURL}}}
	result, err := control.discover(t.Context(), aliceAuth(), "weather")
	if err != nil {
		t.Fatal(err)
	}
	candidates := result["candidates"].([]DiscoveryCandidate)
	if len(candidates) == 0 || len(candidates) > 5 {
		t.Fatalf("public Registry returned %d usable source candidates", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.PreparedID != "" || candidate.Status != "registry-source" {
			t.Fatalf("unprepared Registry candidate was mislabeled: %+v", candidate)
		}
		if _, err := control.selectedRepository(aliceAuth(), candidate.ID); err != nil {
			t.Fatalf("selected Registry candidate was unavailable: %v", err)
		}
	}
}

func TestRealPublishedDockerCatalogImageHasExactOCIProof(t *testing.T) {
	if os.Getenv("HUB_REAL_PUBLISHED_MCP") != "1" {
		t.Skip("set HUB_REAL_PUBLISHED_MCP=1 for the public Docker MCP catalog acceptance")
	}
	const (
		repository = "https://github.com/modelcontextprotocol/servers"
		subfolder  = "src/sequentialthinking"
		commit     = "82064568802e542c3924560aef2cb421b4ce436c"
		image      = "mcp/sequentialthinking@sha256:cd3174b2ecf37738654cf7671fb1b719a225c40a78274817da00c4241f465e5f"
	)
	lookup := RecipeLookup{Repository: repository, Subfolder: subfolder, CommitSHA: commit}
	candidate := RecipeCandidate{Repository: repository, Subfolder: subfolder, CommitSHA: commit, Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: image}}
	if proof, err := inspectPublishedOCI(context.Background(), &http.Client{}, image); err != nil {
		t.Fatalf("public OCI proof: %v", err)
	} else if proof.Source != repository || proof.Revision != commit {
		t.Fatalf("public OCI provenance: %+v", proof)
	}
	completed, ok := completeRecipeCandidate(context.Background(), &http.Client{}, candidate, lookup)
	if !ok {
		t.Fatal("public Docker MCP catalog image failed exact OCI correlation")
	}
	if completed.Launch.Digest != "sha256:cd3174b2ecf37738654cf7671fb1b719a225c40a78274817da00c4241f465e5f" || len(completed.Launch.Entrypoint) == 0 {
		t.Fatalf("public OCI recipe: %+v", completed.Launch)
	}
}

func TestRealDockerHubCatalogFindsPublishedMCP(t *testing.T) {
	if os.Getenv("HUB_REAL_PUBLISHED_MCP") != "1" {
		t.Skip("set HUB_REAL_PUBLISHED_MCP=1 for the public Docker Hub catalog acceptance")
	}
	const (
		repository = "https://github.com/modelcontextprotocol/servers"
		subfolder  = "src/sequentialthinking"
		commit     = "82064568802e542c3924560aef2cb421b4ce436c"
	)
	lookup := RecipeLookup{Repository: repository, Subfolder: subfolder, CommitSHA: commit, Owner: "modelcontextprotocol", Name: "sequentialthinking"}
	candidates, err := (DockerHubCatalog{}).Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("public Docker Hub lookup: %#v %v", candidates, err)
	}
	if candidates[0].Launch.Digest != "sha256:cd3174b2ecf37738654cf7671fb1b719a225c40a78274817da00c4241f465e5f" || len(candidates[0].Launch.Entrypoint) == 0 {
		t.Fatalf("public Docker Hub recipe: %+v", candidates[0].Launch)
	}
}

func TestRealSmitheryPublicConnectionSchema(t *testing.T) {
	if os.Getenv("HUB_REAL_SMITHERY") != "1" {
		t.Skip("set HUB_REAL_SMITHERY=1 for the public Smithery schema acceptance")
	}
	body, _, err := fetchCatalogJSON(context.Background(), &http.Client{}, smitheryAPIURL+"/servers/brave", nil, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	connections, ok := document["connections"].([]any)
	if !ok || len(connections) == 0 {
		t.Fatalf("Smithery connections missing: %s", body)
	}
	connection, ok := connections[0].(map[string]any)
	if !ok {
		t.Fatalf("Smithery connection malformed: %#v", connections[0])
	}
	recipe := connectionFromSchema(connection["configSchema"], "smithery")
	if len(recipe.Fields) == 0 || recipe.Fields[0].Name != "BRAVE_API_KEY" || recipe.Fields[0].Target != "braveApiKey" || !recipe.Fields[0].Secret {
		t.Fatalf("Smithery connection recipe: %+v", recipe)
	}
}

func TestRealGHCRImageHintIsVerified(t *testing.T) {
	if os.Getenv("HUB_REAL_GHCR") != "1" {
		t.Skip("set HUB_REAL_GHCR=1 for the public GHCR OCI acceptance")
	}
	const (
		repository = "https://github.com/stacklok/dockyard"
		commit     = "6ea5ab457cfa4f9c9c63b3ae2ad73ae2df610a32"
		image      = "ghcr.io/stacklok/dockyard/npx/server-sequential-thinking:2026.8.31"
		digest     = "sha256:ce5f033bfdb04c95c0873fa8c98ebcd50e41ebe03155077bab602072136e6efb"
	)
	lookup := RecipeLookup{Repository: repository, CommitSHA: commit, Owner: "stacklok", Name: "dockyard", ImageHints: []string{image}, VersionHints: []string{"2026.8.31"}}
	candidates, err := (GitHubPackagesCatalog{}).Lookup(context.Background(), lookup)
	if err != nil || len(candidates) != 1 || candidates[0].Launch.Digest != digest {
		t.Fatalf("public GHCR lookup: %#v %v", candidates, err)
	}
}
