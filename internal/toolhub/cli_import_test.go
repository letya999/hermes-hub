package toolhub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

// httptestNewRelease serves a release listing the named assets plus one
// checksums.txt; asset downloads return a fixed payload so ambiguity and
// missing-asset paths can be exercised without real digests.
func httptestNewRelease(t *testing.T, tag string, assets []string, payload string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/tags/"+tag, func(w http.ResponseWriter, _ *http.Request) {
		var names strings.Builder
		for _, a := range assets {
			fmt.Fprintf(&names, `{"name":%q,"browser_download_url":%q},`, a, server.URL+"/dl/"+a)
		}
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[%s{"name":"checksums.txt","browser_download_url":%q}]}`, tag, names.String(), server.URL+"/dl/checksums")
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(payload)) })
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func cliImportConfig() ArtifactImportConfig {
	return ArtifactImportConfig{
		DefinitionID: "rg", Version: "1.0.0", Image: "hermes-cli-artifact/rg",
		Entrypoint: []string{"/usr/local/bin/rg"},
		Tools:      []ToolSpec{{Name: "run", Effect: ReadEffect}},
		Workload:   WorkloadPolicy{Class: PerUser, Rationale: "r"},
		Execution:  ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"127.0.0.1"}},
		Health:     HealthProbe{Kind: "exec", Value: "/usr/local/bin/rg", TimeoutSeconds: 5},
	}
}

// The release import runs end to end up to the restricted build: fetch,
// checksum verify, extraction, recipe and context verification all happen;
// only the docker call is out of scope for the unit path.
func TestImportCLIReleaseArtifactPipeline(t *testing.T) {
	asset := tarGzFixture(t, map[string]string{"rg-14/rg": "ELF"})
	server := releaseServer(t, "rg-14.tar.gz", asset, nil, "")
	defer server.Close()
	source, err := parseGitHubReleaseSource("github-release:BurntSushi/ripgrep@14.1.1", "rg-*", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Non-absolute builder paths fail closed before any docker call — that is
	// the observable boundary of this unit test.
	build := RestrictedBuildConfig{SeccompPath: "relative", ArtifactDirectory: t.TempDir(), MaxArtifactBytes: 1 << 20}
	_, err = ImportCLIReleaseArtifact(context.Background(), source, cliImportConfig(), build, server.Client(), server.URL)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected builder-path rejection after full release pipeline, got %v", err)
	}
}

func TestImportCLIReleaseArtifactRejects(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0", Asset: "tool"}
	config := cliImportConfig()
	for _, tc := range []struct {
		name   string
		source ArtifactSource
		mutate func(*ArtifactImportConfig)
	}{
		{"no-tag", ArtifactSource{Repository: "https://github.com/o/r", Asset: "tool"}, nil},
		{"no-asset", ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0"}, nil},
		{"mutable-image", source, func(c *ArtifactImportConfig) { c.Image = "img@sha256:x" }},
		{"no-entrypoint", source, func(c *ArtifactImportConfig) { c.Entrypoint = nil }},
		{"relative-entrypoint", source, func(c *ArtifactImportConfig) { c.Entrypoint = []string{"tool"} }},
		{"two-token-entrypoint", source, func(c *ArtifactImportConfig) { c.Entrypoint = []string{"/a", "/b"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			if _, err := ImportCLIReleaseArtifact(context.Background(), tc.source, c, RestrictedBuildConfig{}, nil, ""); !errors.Is(err, ErrInvalid) {
				t.Fatalf("admitted: %v", err)
			}
		})
	}
}

func TestImportCLIArtifactRejects(t *testing.T) {
	config := cliImportConfig()
	source := ArtifactSource{Repository: "https://github.com/o/r", CommitSHA: strings.Repeat("a", 40)}
	for _, tc := range []struct {
		name   string
		source ArtifactSource
		mutate func(*ArtifactImportConfig)
	}{
		{"unpinned-source", ArtifactSource{Repository: "https://github.com/o/r"}, nil},
		{"release-source", ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0", Asset: "x"}, nil},
		{"mutable-image", source, func(c *ArtifactImportConfig) { c.Image = "img@sha256:x" }},
		{"no-entrypoint", source, func(c *ArtifactImportConfig) { c.Entrypoint = nil }},
		{"relative-entrypoint", source, func(c *ArtifactImportConfig) { c.Entrypoint = []string{"tool"} }},
		{"two-token-entrypoint", source, func(c *ArtifactImportConfig) { c.Entrypoint = []string{"/a", "/b"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			if _, err := ImportCLIArtifact(context.Background(), tc.source, c, RestrictedBuildConfig{}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("admitted: %v", err)
			}
		})
	}
}

func TestCLIArtifactSourceRelease(t *testing.T) {
	c := &ControlPlane{Store: NewStore()}
	source, err := c.cliArtifactSource(context.Background(), map[string]any{
		"source": "github-release:o/r@1.2.3", "asset": "tool-*", "digest": strings.Repeat("b", 64), "member": "dir/tool",
	})
	if err != nil || source.Tag != "1.2.3" || source.Asset != "tool-*" || source.AssetDigest != "sha256:"+strings.Repeat("b", 64) || source.AssetMember != "dir/tool" {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	// Release fields on a non-release source fail closed.
	if _, err := c.cliArtifactSource(context.Background(), map[string]any{
		"source": githubCommitURL(), "asset": "tool",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("commit source carrying release fields admitted: %v", err)
	}
}

func TestReleaseHelpers(t *testing.T) {
	if client := releaseHTTPClient(); client == nil || client.Timeout == 0 {
		t.Fatal("releaseHTTPClient")
	}
	// The redirect policy follows github-owned https hops only.
	client := releaseHTTPClient()
	req := func(raw string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, tc := range []struct {
		url  string
		via  int
		want bool
	}{
		{"https://github.com/a/b", 0, true},
		{"https://objects.githubusercontent.com/x", 0, true},
		{"http://github.com/a/b", 0, false},
		{"https://evil.example/x", 0, false},
		{"https://github.com/a/b", 5, false},
	} {
		err := client.CheckRedirect(req(tc.url), make([]*http.Request, tc.via))
		if allowed := err == nil; allowed != tc.want {
			t.Fatalf("redirect %s via=%d: allowed=%v want=%v", tc.url, tc.via, allowed, tc.want)
		}
	}
	if githubAPIBase() != "https://api.github.com" {
		t.Fatal("apiBase")
	}
	if got := normalizeDigestPrefix(strings.Repeat("a", 64)); got != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("normalize=%q", got)
	}
	if got := normalizeDigestPrefix(""); got != "" {
		t.Fatalf("empty=%q", got)
	}
	if !isChecksumAsset("checksums.txt") || !isChecksumAsset("SHA256SUMS") || isChecksumAsset("tool.tar.gz") {
		t.Fatal("isChecksumAsset")
	}
}

func TestMemberMatch(t *testing.T) {
	for _, tc := range []struct {
		member, want string
		ok           bool
	}{
		{"dir/tool", "dir/tool", true},
		{"dir/tool", "tool", true},
		{"dir/tool", "", false},
		{"tool", "dir/tool", false},
		{"dir/x", "tool", false},
	} {
		if got := memberMatch(tc.member, tc.want); got != tc.ok {
			t.Fatalf("memberMatch(%q,%q)=%v", tc.member, tc.want, got)
		}
	}
}

func TestGenerateCLIReleaseRecipeValidation(t *testing.T) {
	if _, _, err := generateCLIReleaseRecipe("tool", []byte("ELF"), nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative binary admitted: %v", err)
	}
	if _, _, err := generateCLIReleaseRecipe("/usr/local/bin/tool", nil, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty binary admitted: %v", err)
	}
	// An oversized checksum record degrades to no lock entry; the recipe then
	// fails its own verification — still fail-closed.
	if _, _, err := generateCLIReleaseRecipe("/usr/local/bin/tool", []byte("ELF"), make([]byte, (4<<20)+1), ""); err == nil {
		t.Fatal("oversized checksum record admitted")
	}
}

func TestReleaseFetchErrors(t *testing.T) {
	server := releaseServer(t, "a", []byte("x"), nil, "")
	defer server.Close()
	source := ArtifactSource{Repository: "https://github.com/BurntSushi/ripgrep", Tag: "missing", Asset: "a"}
	if _, _, _, _, _, err := fetchReleaseBinary(context.Background(), source, server.Client(), server.URL, "x"); err == nil {
		t.Fatal("missing tag admitted")
	}
	// Non-2xx statuses surface as plain fetch errors.
	if err := fetchReleaseJSON(context.Background(), server.Client(), server.URL+"/nope", new(any)); err == nil {
		t.Fatal("404 metadata admitted")
	}
	if _, err := fetchReleaseBytes(context.Background(), server.Client(), server.URL+"/nope", 10); err == nil {
		t.Fatal("404 download admitted")
	}
	// Asset pattern matching zero and many assets both fail closed.
	server2 := httptestNewRelease(t, "1.0", []string{"a-1", "a-2"}, "")
	defer server2.Close()
	if _, _, _, _, _, err := fetchReleaseBinary(context.Background(), ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0", Asset: "a-*"}, server2.Client(), server2.URL, "a"); !errors.Is(err, ErrStale) {
		t.Fatalf("ambiguous asset admitted: %v", err)
	}
	if _, _, _, _, _, err := fetchReleaseBinary(context.Background(), ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0", Asset: "missing"}, server2.Client(), server2.URL, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing asset admitted: %v", err)
	}
}

func TestContextHasDockerfile(t *testing.T) {
	if contextHasDockerfile(languageContext(t, map[string]string{"Dockerfile": "FROM x"})) != true {
		t.Fatal("dockerfile missed")
	}
	if contextHasDockerfile(languageContext(t, map[string]string{"go.mod": "module m", "Dockerfile.bak": "FROM x"})) {
		t.Fatal("false positive")
	}
	if contextHasDockerfile([]byte("not a tar")) {
		t.Fatal("malformed context")
	}
}

// Shared artifact installs promote to the catalog after the definition is
// registered — the same gate the direct-command path uses.
func TestFinishCLIArtifactSharedPromotes(t *testing.T) {
	fix := controlCLIFixture(t, true)
	fix.control.PrepareSyncWindow = -1
	operator := identity.TelegramEnvelope("operator", 3, "runtime", "policy-1")
	if err := putGrant(t, fix.store, OperatorControlGrant("operator", "install_shared")); err != nil {
		t.Fatal(err)
	}
	fix.control.CLIArtifacts = &CLIArtifactPipeline{
		Build: func(context.Context, ArtifactSource, ArtifactImportConfig) (ImportedArtifact, error) {
			return ImportedArtifact{
				Recipe: ArtifactRecipe{Entrypoint: []string{"/usr/local/bin/tool"}},
				Definition: ToolDefinition{Source: DefinitionSource{
					Image:        "hermes-cli-artifact/shared-tool",
					RecipeDigest: "sha256:" + strings.Repeat("4", 64), ReviewDigest: "sha256:" + strings.Repeat("5", 64),
				}},
				Artifact: StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("1", 64), Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("9", 64)}},
			}, nil
		},
	}
	spec := map[string]any{
		"name": "shared-tool", "source": githubCommitURL(), "binary": "/usr/local/bin/tool", "scope": "shared",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	// The shared gate lives in prepareCLI; calling the artifact path with
	// shared=true exercises the promote half of it.
	if _, err := fix.control.prepareCLIArtifact(context.Background(), operator, spec, "sh-1", true); err != nil {
		t.Fatalf("shared artifact install denied: %v", err)
	}
	if pub := fix.store.publications[definitionKey("shared-tool", "1.0.0")]; pub.Visibility != PublicationCatalog {
		t.Fatalf("publication=%+v", pub)
	}
	definition := fix.store.definitions[definitionKey("shared-tool", "1.0.0")]
	if definition.Workload.Class != Shared || definition.Source.Command != "/usr/local/bin/tool" {
		t.Fatalf("definition=%+v", definition)
	}
}
