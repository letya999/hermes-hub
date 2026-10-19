package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type testRecipeCatalogFunc func(context.Context, api.RecipeLookup) ([]api.RecipeCandidate, error)

func (f testRecipeCatalogFunc) Lookup(ctx context.Context, lookup api.RecipeLookup) ([]api.RecipeCandidate, error) {
	return f(ctx, lookup)
}

func resolverContext(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestRecipeResolverUsesRepositoryMatchedManifestAndHidesValues(t *testing.T) {
	const imageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	data := resolverContext(t, map[string]string{
		"server.json": `{"repository":"https://github.com/acme/weather","packages":[{"registryType":"oci","identifier":"ghcr.io/acme/weather@` + imageDigest + `","version":"1.2.3","command":"/app/server","environment":{"WEATHER_API_KEY":"required"}}]}`,
		"README.md":   "Set WEATHER_API_KEY=SECRET_VALUE_DO_NOT_LEAK",
	})
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, data)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.State != "ready" || resolution.Launch.Artifact != "ghcr.io/acme/weather" || resolution.Launch.Digest != imageDigest || resolution.Launch.Version != "1.2.3" {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
	if len(resolution.Connection.Fields) != 1 || resolution.Connection.Fields[0].Name != "WEATHER_API_KEY" || !resolution.Connection.Fields[0].Required || !resolution.Connection.Fields[0].Secret {
		t.Fatalf("unexpected connection recipe: %+v", resolution.Connection)
	}
	encoded, _ := json.Marshal(resolution)
	if strings.Contains(string(encoded), "SECRET_VALUE_DO_NOT_LEAK") {
		t.Fatal("recipe leaked a repository credential value")
	}
}

func TestRecipeResolverRejectsUnsafeComposeAndRecordsSidecars(t *testing.T) {
	const digest = "sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	compose := "services:\n  mcp:\n    image: ghcr.io/acme/weather@" + digest + "\n    command: [\"/app/server\"]\n    environment:\n      - WEATHER_API_KEY\n    volumes:\n      - state:/state:rw\n  postgres:\n    image: postgres@" + digest + "\n"
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"compose.yaml": compose}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Launch.Sidecars) != 1 || resolution.Launch.Sidecars[0] != "postgres" || resolution.Launch.Mounts[0].Source != "state" {
		t.Fatalf("compose sidecar or state was not recorded: %+v", resolution.Launch)
	}
	unsafe := "services:\n  mcp:\n    image: ghcr.io/acme/weather@" + digest + "\n    privileged: true\n    volumes:\n      - /host:/state\n"
	unsafeResolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"compose.yml": unsafe}))
	if err != nil || api.HasLaunch(unsafeResolution.Launch) || len(unsafeResolution.Warnings) == 0 {
		t.Fatalf("unsafe compose was not rejected for safe fallback: resolution=%+v err=%v", unsafeResolution, err)
	}
}

func TestRecipeResolverIgnoresComposeShellHealthcheck(t *testing.T) {
	const digest = "sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	compose := "services:\n  mcp:\n    image: ghcr.io/acme/weather@" + digest + "\n    healthcheck:\n      test: [\"CMD-SHELL\", \"curl localhost || exit 1\"]\n"
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"compose.yaml": compose}))
	if err != nil || resolution.Launch.Health.Kind != "" || len(resolution.Warnings) == 0 {
		t.Fatalf("shell healthcheck was not safely ignored: resolution=%+v err=%v", resolution, err)
	}
}

func TestRecipeResolverIgnoresNameOnlyCatalogAndRunsCatalogsConcurrently(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	var entered atomic.Int32
	var once sync.Once
	release := make(chan struct{})
	catalog := func(candidate api.RecipeCandidate) api.RecipeCatalog {
		return testRecipeCatalogFunc(func(context.Context, api.RecipeLookup) ([]api.RecipeCandidate, error) {
			if entered.Add(1) == 2 {
				once.Do(func() { close(release) })
			}
			<-release
			return []api.RecipeCandidate{candidate}, nil
		})
	}
	resolver := api.RecipeResolver{Catalogs: []api.RecipeCatalog{
		catalog(api.RecipeCandidate{Repository: "https://github.com/other/weather", CommitSHA: commit, Launch: api.LaunchRecipe{Artifact: "ghcr.io/other/weather", Digest: digest, Transport: api.ContainerMCP, Entrypoint: []string{"/server"}}}),
		catalog(api.RecipeCandidate{Repository: source.Repository, CommitSHA: commit, Launch: api.LaunchRecipe{Artifact: "ghcr.io/acme/weather", Digest: digest, Transport: api.ContainerMCP, Entrypoint: []string{"/server"}}}),
	}}
	resolution, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"README.md": "weather"}))
	if err != nil || entered.Load() != 2 {
		t.Fatalf("catalog lookup was not parallel: entered=%d err=%v", entered.Load(), err)
	}
	if resolution.State != "ready" || resolution.Launch.Artifact != "ghcr.io/acme/weather" {
		t.Fatalf("wrong catalog selected: %+v", resolution)
	}
	if len(resolution.Warnings) == 0 {
		t.Fatal("name-only candidate was not rejected with evidence")
	}
}

func TestRecipeResolverQueriesCatalogsWithSourceOnlyLookup(t *testing.T) {
	// The catalog answer must not depend on repository files: image/version
	// hints parsed out of repo metadata are low-rank and never reach the
	// lookup — only source-derived identity does.
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	var seen api.RecipeLookup
	resolver := api.RecipeResolver{Catalogs: []api.RecipeCatalog{testRecipeCatalogFunc(func(_ context.Context, lookup api.RecipeLookup) ([]api.RecipeCandidate, error) {
		seen = lookup
		return []api.RecipeCandidate{{Repository: source.Repository, CommitSHA: commit, Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.example/weather"}}}, nil
	})}}
	_, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{
		"server.json": `{"repository":"https://github.com/acme/weather","packages":[{"registryType":"oci","identifier":"ghcr.io/acme/weather:1.0.0","version":"1.0.0"}]}`,
	}))
	if err != nil || seen.Repository != source.Repository || seen.CommitSHA != commit || seen.Owner != "acme" || seen.Name != "weather" {
		t.Fatalf("catalog lookup lost source identity: %+v err=%v", seen, err)
	}
	if len(seen.ImageHints) != 0 || len(seen.VersionHints) != 0 {
		t.Fatalf("catalog lookup received repo-derived hints: %+v", seen)
	}
}

func TestParseGitHubSourceKeepsPinnedSubfolder(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source, err := api.ParseGitHubSource("https://github.com/acme/weather/tree/" + commit + "/packages/mcp")
	if err != nil || source.Subfolder != "packages/mcp" || source.CommitSHA != commit {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	if _, err := source.ArchiveURL(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSourcePublishesRecipeOnlyToItsOwner(t *testing.T) {
	definition := userMCPDefinition()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	recipe := &api.RecipeResolution{
		Source:     api.ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: commit},
		State:      "draft",
		Launch:     api.LaunchRecipe{Transport: api.ContainerMCP, Entrypoint: []string{"/app/server"}},
		Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "API_TOKEN", Type: "secret", Required: true, Secret: true, Delivery: "env", Target: "API_TOKEN"}}},
	}
	fix := newControlFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review", Recipe: recipe}, nil
	})
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "recipe-owner"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := fix.control.Invoke(context.Background(), aliceAuth(), "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if status["commit_sha"] != commit || status["recipe"] == nil {
		t.Fatalf("recipe or pinned source missing from owner status: %+v", status)
	}
	if _, err := fix.control.Invoke(context.Background(), bobAuth(), "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); !errors.Is(err, api.ErrNotFound) && !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("recipe crossed principal boundary: %v", err)
	}
}

func TestRecipeResolverParsesDockerfileMCPBAndScopedConnectionMetadata(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	dockerfile := "FROM ghcr.io/acme/base@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nENV API_TOKEN\nVOLUME [\"/state\"]\nHEALTHCHECK CMD [\"/app/health\"]\nENTRYPOINT [\"/app/server\"]\n"
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
	if err != nil || len(resolution.Launch.Entrypoint) != 1 || resolution.Launch.Mounts[0].Target != "/state" || resolution.Launch.Health.Kind != "exec" {
		t.Fatalf("Dockerfile recipe=%+v err=%v", resolution, err)
	}
	if len(resolution.Connection.Fields) != 1 || resolution.Connection.Fields[0].Name != "API_TOKEN" {
		t.Fatalf("Dockerfile connection=%+v", resolution.Connection)
	}

	mcpb := `{"manifest_version":"0.1","version":"1.0.0","entry_point":"/app/server","args":["--stdio"],"environment":{"API_TOKEN":{"delivery":"http_header","target":"Authorization","required":true}},"auth":{"oauth":{"url":"https://auth.example/authorize","env":{"CLIENT_ID":""}}}}`
	resolution, err = (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"manifest.json": mcpb}))
	if err != nil || resolution.Launch.Transport != api.BoundedCLI || len(resolution.Connection.Alternatives) != 1 {
		t.Fatalf("MCPB recipe=%+v err=%v", resolution, err)
	}
	if resolution.Connection.Fields[0].Delivery != "http_header" || resolution.Connection.Alternatives[0].Fields[0].Delivery != "oauth" {
		t.Fatalf("MCPB connection delivery=%+v", resolution.Connection)
	}

	scoped := api.ArtifactSource{Repository: source.Repository, CommitSHA: commit, Subfolder: "packages/mcp"}
	resolution, err = (api.RecipeResolver{}).Resolve(context.Background(), scoped, resolverContext(t, map[string]string{"packages/mcp/Dockerfile": dockerfile}))
	if err != nil || len(resolution.Launch.Entrypoint) != 1 {
		t.Fatalf("scoped recipe=%+v err=%v", resolution, err)
	}
}

func TestRecipeResolverDockerfileCacheMountsAreMetadataOnly(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/github/github-mcp-server", CommitSHA: commit}
	dockerfile := "FROM golang:1.25 AS build\n" +
		"WORKDIR /build\n" +
		"RUN --mount=type=cache,target=/go/pkg/mod \\\n" +
		"    --mount=type=cache,target=/root/.cache/go-build \\\n" +
		"    go build ./cmd/server\n" +
		"FROM gcr.io/distroless/base\n" +
		"COPY --from=build /build/server /server\n" +
		"RUN --mount=type=cache,target=/tmp/cache true\n" +
		"ENTRYPOINT [\"/server\"]\n"
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Launch.Entrypoint) != 1 || resolution.Launch.Entrypoint[0] != "/server" {
		t.Fatalf("cache mounts destroyed the recipe: %+v", resolution.Launch)
	}
	if len(resolution.Launch.Mounts) != 0 {
		t.Fatalf("cache mount became a runtime mount: %+v", resolution.Launch.Mounts)
	}
	for _, warning := range resolution.Warnings {
		if strings.Contains(warning, "unsafe") {
			t.Fatalf("cache-only mounts rejected: %v", resolution.Warnings)
		}
	}
}

func TestRecipeResolverUnsafeDockerfileDirectivesForceGeneratedFallback(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/github/github-mcp-server", CommitSHA: commit}
	cases := map[string]string{
		"secret mount":       "RUN --mount=type=secret,id=oauth_client_id go build\nENTRYPOINT [\"/server\"]\n",
		"oauth secrets":      "RUN --mount=type=secret,id=oauth_client_id \\\n    --mount=type=secret,id=oauth_client_secret \\\n    go build\nENTRYPOINT [\"/server\"]\n",
		"mixed cache+secret": "RUN --mount=type=cache,target=/go/pkg/mod \\\n    --mount=type=secret,id=oauth_client_id \\\n    go build\nENTRYPOINT [\"/server\"]\n",
		"bind mount":         "RUN --mount=type=bind,source=/x go build\n",
		"ssh mount":          "RUN --mount=type=ssh go build\n",
		"tmpfs mount":        "RUN --mount=type=tmpfs,target=/tmp go build\n",
		"unknown mount":      "RUN --mount=type=future,target=/x go build\n",
		"typeless mount":     "RUN --mount=target=/x go build\n",
		"bare mount flag":    "RUN --mount type=cache go build\n",
		"uppercase secret":   "RUN --MOUNT=TYPE=SECRET,ID=x go build\n",
		"spaced secret":      "RUN    --mount=type=secret,id=x    go build\n",
		"privileged":         "RUN --privileged go build\n",
		"insecure security":  "RUN --security=insecure go build\n",
		"host network":       "RUN --network=host go build\n",
		"unknown option":     "RUN --device=/dev/fuse go build\n",
		"docker socket":      "RUN --mount=type=bind,source=/var/run/docker.sock go build\n",
		"socket volume":      "VOLUME /var/run/docker.sock\n",
		"socket volume json": "VOLUME [\"/run/docker.sock\"]\n",
	}
	for name, dockerfile := range cases {
		resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
		if err != nil {
			t.Fatalf("%s: unsafe metadata became a fatal resolver error: %v", name, err)
		}
		if api.HasLaunch(resolution.Launch) || resolution.State == "ready" {
			t.Fatalf("%s: unsafe directive produced an admissible recipe: %+v", name, resolution)
		}
		unsafe := false
		for _, warning := range resolution.Warnings {
			if strings.Contains(warning, "unsafe Dockerfile directive") {
				unsafe = true
			}
		}
		if !unsafe || resolution.State != "review" {
			t.Fatalf("%s: unsafe directive did not force review/fallback: %+v", name, resolution)
		}
	}
}

func TestRecipeResolverDockerfileDirectiveLikeTextIsNotAFlag(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	dockerfile := "# RUN --mount=type=secret,id=x looks unsafe but is a comment\n" +
		"FROM ghcr.io/acme/base@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"RUN echo \"--mount=type=secret\" && echo --privileged\n" +
		"RUN printf '# --network=host'\n" +
		"RUN echo /var/run/docker.sock\n" +
		"ENTRYPOINT [\"/server\"]\n"
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
	if err != nil || len(resolution.Launch.Entrypoint) != 1 {
		t.Fatalf("directive-like text caused a false positive: %+v err=%v", resolution, err)
	}
	for _, warning := range resolution.Warnings {
		if strings.Contains(warning, "unsafe Dockerfile") {
			t.Fatalf("directive-like text flagged unsafe: %v", resolution.Warnings)
		}
	}
}

func TestRecipeResolverUnsafeDockerfileCannotBecomeReadyWithCatalog(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	source := api.ArtifactSource{Repository: "https://github.com/github/github-mcp-server", CommitSHA: commit}
	resolver := api.RecipeResolver{Catalogs: []api.RecipeCatalog{testRecipeCatalogFunc(func(context.Context, api.RecipeLookup) ([]api.RecipeCandidate, error) {
		return []api.RecipeCandidate{{Repository: source.Repository, CommitSHA: commit, Launch: api.LaunchRecipe{Artifact: "ghcr.io/github/github-mcp-server", Digest: digest, Transport: api.ContainerMCP, Entrypoint: []string{"/server"}}}}, nil
	})}}
	dockerfile := "RUN --mount=type=secret,id=oauth_client_secret go build\nENTRYPOINT [\"/server\"]\n"
	resolution, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
	if err != nil || resolution.State != "review" {
		t.Fatalf("unsafe source metadata reached the ready/published boundary: %+v err=%v", resolution, err)
	}
}

func TestConnectionRecipeValidationAndDefinitionProjection(t *testing.T) {
	recipe := api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "API_TOKEN", Required: true, Delivery: "env"}}}
	if err := recipe.Validate(); err != nil || len(recipe.CredentialInputs()) != 1 || !recipe.CredentialInputs()[0].Required {
		t.Fatalf("valid connection recipe=%+v err=%v", recipe, err)
	}
	bad := recipe
	bad.Fields[0].Delivery = "shell"
	if err := bad.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("unsupported delivery accepted: %v", err)
	}
	definition := userMCPDefinition()
	api.MergeConnectionForDefinition(recipe, &definition)
	if len(definition.Credentials) != 1 || definition.Credentials[0].Name != "API_TOKEN" {
		t.Fatalf("definition projection=%+v", definition.Credentials)
	}
}

func TestRecipeResolverRejectsInvalidInputs(t *testing.T) {
	if err := (api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "bad-name", Delivery: "env"}}}).Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("invalid field accepted: %v", err)
	}
	if err := (api.ConnectionRecipe{Alternatives: []api.ConnectionAlternative{{Name: "oauth", URL: "http://auth.example"}}}).Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("insecure alternative accepted: %v", err)
	}
	if _, err := (api.RecipeResolver{}).Resolve(context.Background(), api.ArtifactSource{Repository: "bad", CommitSHA: "bad"}, nil); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("invalid source accepted: %v", err)
	}
	if _, err := api.ScopeRecipeContext(map[string][]byte{"README.md": []byte("ok")}, "missing"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("missing subfolder accepted: %v", err)
	}
	if _, err := api.ReadRecipeContext([]byte("not-a-tar"), 64<<20); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("malformed context accepted: %v", err)
	}
	if _, err := api.LookupRecipe(api.ArtifactSource{Repository: "https://github.com/only-owner", CommitSHA: strings.Repeat("a", 40)}); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("malformed repository lookup accepted: %v", err)
	}
	if _, _, _, err := api.ParseServerManifest([]byte("{"), "server.json", api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("a", 40)}); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("malformed server manifest accepted: %v", err)
	}
}

func TestRecipeResolverAcceptsVerifiedRemoteCandidateAndBoundedContext(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	resolver := api.RecipeResolver{Catalogs: []api.RecipeCatalog{testRecipeCatalogFunc(func(context.Context, api.RecipeLookup) ([]api.RecipeCandidate, error) {
		return []api.RecipeCandidate{{Repository: source.Repository, CommitSHA: commit, Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.example/sse"}, Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "AUTH_JSON", Type: "json", Delivery: "json", Target: "request"}}}}}, nil
	})}}
	resolution, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"README.md": "remote"}))
	if err != nil || resolution.State != "ready" || resolution.Launch.Endpoint == "" || resolution.Connection.Fields[0].Delivery != "json" {
		t.Fatalf("remote resolution=%+v err=%v", resolution, err)
	}
	if _, err := (api.RecipeResolver{MaxContextBytes: 1}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"README.md": "too large"})); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("oversized bounded context accepted: %v", err)
	}
}

func TestRecipeResolverMarksConflictingLocalLaunchesForReview(t *testing.T) {
	warnings := []string{}
	left := api.LaunchRecipe{Transport: api.ContainerMCP, Entrypoint: []string{"/one"}}
	right := api.LaunchRecipe{Transport: api.ContainerMCP, Entrypoint: []string{"/two"}}
	merged := api.MergeLaunch(left, right, &warnings)
	if !api.SameLaunch(merged, left) || len(warnings) != 1 {
		t.Fatalf("conflict was guessed instead of recorded: merged=%+v warnings=%v", merged, warnings)
	}
}

func TestRecipeResolverReadsMCPConfigAndExampleEnvWithoutValues(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	contextBytes := resolverContext(t, map[string]string{
		".mcp.json":    `{"mcpServers":{"weather":{"command":"node","args":["server.js"],"env":{"WEATHER_API_KEY":"do-not-copy"}}}}`,
		".env.example": "WEATHER_API_KEY=example-value\nWEATHER_REGION=\n",
	})
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, contextBytes)
	if err != nil || resolution.Launch.Transport != api.BoundedCLI || len(resolution.Launch.Entrypoint) != 1 || resolution.Launch.Entrypoint[0] != "node" {
		t.Fatalf("MCP config launch=%+v err=%v", resolution.Launch, err)
	}
	if len(resolution.Connection.Fields) != 2 || resolution.Connection.Fields[0].Name != "WEATHER_API_KEY" || resolution.Connection.Fields[1].Name != "WEATHER_REGION" {
		t.Fatalf("MCP config connection=%+v", resolution.Connection)
	}
	encoded, _ := json.Marshal(resolution)
	if strings.Contains(string(encoded), "do-not-copy") || strings.Contains(string(encoded), "example-value") {
		t.Fatal("metadata value leaked into recipe")
	}
}

func TestParseExampleEnvKeepsCredentialsAndSkipsRuntimeDefaults(t *testing.T) {
	recipe := api.ParseExampleEnv([]byte("TRANSPORT=stdio\nPORT=3000\nHOST=127.0.0.1\nGOOGLE_OAUTH_CREDENTIALS=./gcp-oauth.keys.json\n"), ".env.example")
	if len(recipe.Fields) != 1 {
		t.Fatalf("runtime defaults became credential fields: %+v", recipe.Fields)
	}
	field := recipe.Fields[0]
	if field.Name != "GOOGLE_OAUTH_CREDENTIALS" || field.Type != "json" || field.Delivery != "json" || !field.Secret {
		t.Fatalf("OAuth JSON credential was misclassified: %+v", field)
	}
	recipe = api.ParseExampleEnv([]byte("GITLAB_API_URL=https://gitlab.com\nGITLAB_TOKEN=placeholder\nGITLAB_TOKEN_TEST=test-only\nGITLAB_ALLOWED_PROJECT_IDS=\nHTTP_PROXY=\nHTTPS_PROXY=\nNO_PROXY=localhost\nTEST_PROJECT_ID=1\n"), ".env.example")
	byName := map[string]api.ConnectionField{}
	for _, f := range recipe.Fields {
		byName[f.Name] = f
	}
	if len(recipe.Fields) != 2 || !byName["GITLAB_TOKEN"].Required || byName["GITLAB_ALLOWED_PROJECT_IDS"].Required {
		t.Fatalf("proxy variables, test fixtures or optional knobs became required credentials: %+v", recipe.Fields)
	}
}

// mcp-atlassian ships placeholders ("your-company.atlassian.net") for the
// connection config its tools/list depends on. Those values are the author's
// replace-me markers — the fields are required inputs even though the names
// are not secret-shaped. Real defaults ("https://gitlab.com") stay metadata.
func TestParseExampleEnvTreatsPlaceholderValuesAsRequiredInputs(t *testing.T) {
	recipe := api.ParseExampleEnv([]byte("JIRA_URL=https://your-company.atlassian.net\nJIRA_USERNAME=your.email@example.com\nJIRA_API_TOKEN=your_jira_api_token_here\nCONFLUENCE_URL=https://your-company.atlassian.net/wiki\nMCP_VERY_VERBOSE=true\nGITLAB_API_URL=https://gitlab.com\n"), ".env.example")
	byName := map[string]api.ConnectionField{}
	for _, field := range recipe.Fields {
		byName[field.Name] = field
	}
	for _, name := range []string{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN", "CONFLUENCE_URL"} {
		field, ok := byName[name]
		if !ok || !field.Required {
			t.Fatalf("placeholder field %s missing or optional: %+v", name, recipe.Fields)
		}
	}
	if byName["JIRA_URL"].Secret || byName["JIRA_URL"].Type != "string" {
		t.Fatalf("non-secret placeholder misclassified: %+v", byName["JIRA_URL"])
	}
	if _, ok := byName["MCP_VERY_VERBOSE"]; ok {
		t.Fatal("runtime default became a credential field")
	}
	if _, ok := byName["GITLAB_API_URL"]; ok {
		t.Fatal("usable default became a credential field")
	}
}

// mcp-atlassian carries tests/e2e/docker/docker-compose.yml which wires
// throwaway Atlassian containers (CONFLUENCE_DB_PASSWORD, ATL_LICENSE_KEY).
// Those fixtures must not leak into the server's connection recipe.
func TestResolveLocalRecipeSkipsTestFixtureFiles(t *testing.T) {
	files := map[string][]byte{
		".env.example":                        []byte("JIRA_URL=https://your-company.atlassian.net\nJIRA_API_TOKEN=your_jira_api_token_here\n"),
		"tests/e2e/docker/docker-compose.yml": []byte("services:\n  confluence:\n    environment:\n      ATL_JDBC_PASSWORD: ${CONFLUENCE_DB_PASSWORD}\n      ATL_LICENSE_KEY: ${CONFLUENCE_LICENSE_KEY:-}\n"),
		"fixtures/Dockerfile":                 []byte("FROM scratch\nENV FIXTURE_TOKEN=abc\n"),
	}
	_, connection, _, err := api.ResolveLocalRecipe(files, api.ArtifactSource{Repository: "https://github.com/acme/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range connection.Fields {
		if strings.Contains(field.Name, "DB_PASSWORD") || strings.Contains(field.Name, "LICENSE") || field.Name == "FIXTURE_TOKEN" {
			t.Fatalf("test fixture env leaked into recipe: %+v", connection.Fields)
		}
	}
	if len(connection.Fields) != 2 {
		t.Fatalf("root env fields lost: %+v", connection.Fields)
	}
}

func TestRecipeResolverMetadataHelpersDoNotGuessValues(t *testing.T) {
	if got := api.AnyStringMap(map[any]any{"API_TOKEN": "x"})["API_TOKEN"]; got != "x" {
		t.Fatalf("string map conversion: %#v", got)
	}
	if api.ComposeArgs("shell form") != nil || len(api.ComposeNames(map[any]any{"API_TOKEN": nil})) != 1 || api.ComposeHealth(map[string]any{"test": []any{"CMD", "/health"}}) != "CMD /health" {
		t.Fatal("compose metadata helpers lost their safe declarations")
	}
	documentation := api.ParseDocumentationCredentials([]byte("API_TOKEN=do-not-copy\nCLIENT_ID\nignored"), "README.md")
	if len(documentation.Fields) != 2 || documentation.Fields[0].Name == "" {
		t.Fatalf("documentation credentials: %+v", documentation)
	}
	if strings.Contains(documentation.Fields[0].Name, "do-not-copy") {
		t.Fatal("documentation value entered recipe")
	}
	fromMap := api.ConnectionFromMap(map[string]any{"auth": map[string]any{
		"API_TOKEN": map[string]any{"delivery": "file", "path": "/run/secrets/API_TOKEN", "required": false, "secret": true},
	}}, "auth")
	if len(fromMap.Fields) != 1 || fromMap.Fields[0].Delivery != "file" || fromMap.Fields[0].Target != "/run/secrets/API_TOKEN" || fromMap.Fields[0].Required {
		t.Fatalf("map connection metadata: %+v", fromMap)
	}
	alternatives := api.ConnectionAlternatives(map[string]any{"oauth": map[string]any{
		"browser": map[string]any{"authorization_url": "https://auth.example/authorize", "env": map[string]any{"CLIENT_ID": ""}},
	}})
	if len(alternatives.Alternatives) != 1 || alternatives.Alternatives[0].URL == "" || len(alternatives.Alternatives[0].Fields) != 1 {
		t.Fatalf("OAuth alternative metadata: %+v", alternatives)
	}
	if !api.LaunchReady(api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.example/sse"}) || api.LaunchReady(api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "not-a-url"}) {
		t.Fatal("remote launch readiness was guessed")
	}
	if name, digest, ok := api.SplitImmutableImage("ghcr.io/acme/weather@sha256:" + strings.Repeat("a", 64)); !ok || name == "" || digest == "" {
		t.Fatal("valid immutable image rejected")
	}
	if _, _, ok := api.SplitImmutableImage("ghcr.io/acme/weather:latest"); ok || api.StringArray(json.RawMessage(`["/server","--stdio"]`))[0] != "/server" || api.StringArray([]any{"bad\nvalue"}) != nil {
		t.Fatal("unsafe or mutable metadata accepted")
	}
	if api.EnvKey(" API_TOKEN=example") != "API_TOKEN" || api.ConnectionType("CLIENT_ID") != "oauth" || !api.SecretName("PRIVATE_KEY") {
		t.Fatal("credential metadata classification failed")
	}
}

func TestRecipeResolverParserBranches(t *testing.T) {
	if _, _, err := api.ParseMCPJSON([]byte("{"), ".mcp.json"); err == nil {
		t.Fatal("malformed .mcp.json accepted")
	}
	for name, document := range map[string]string{
		"http endpoint":  `{"mcpServers":{"a":{"url":"http://mcp.example/sse"}}}`,
		"query endpoint": `{"mcpServers":{"a":{"url":"https://mcp.example/sse?x=1"}}}`,
		"unsafe command": `{"mcpServers":{"a":{"command":"rm -rf /"}}}`,
	} {
		if _, _, err := api.ParseMCPJSON([]byte(document), ".mcp.json"); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	multi, _, err := api.ParseMCPJSON([]byte(`{"mcpServers":{"a":{"command":"node"},"b":{"url":"https://mcp.example/sse"}}}`), ".mcp.json")
	if err != nil || api.HasLaunch(multi) {
		t.Fatalf("multi-server .mcp.json guessed a launch: %+v err=%v", multi, err)
	}
	if _, _, err := api.ParseMCPBManifest([]byte("{"), "manifest.json"); err == nil {
		t.Fatal("malformed MCPB manifest accepted")
	}
	if _, _, err := api.ParseMCPBManifest([]byte(`{"entry_point":"a;b"}`), "manifest.json"); err == nil {
		t.Fatal("unsafe MCPB entrypoint accepted")
	}
	launch := api.ManifestPackageLaunch(map[string]any{
		"identifier": "ghcr.io/acme/weather", "digest": "sha256:" + strings.Repeat("b", 64),
		"transportType": "stdio", "runtimeHint": "node",
		"runtimeArguments": []any{map[string]any{"value": "--flag"}, map[string]any{"value": "bad\narg"}, "junk"},
		"command":          "node",
	})
	if launch.Digest == "" || len(launch.Args) != 1 || launch.Entrypoint[0] != "node" {
		t.Fatalf("manifest package launch=%+v", launch)
	}
	remote := api.ManifestPackageLaunch(map[string]any{"transport": map[string]any{"type": "http", "url": "https://mcp.example/sse"}})
	if remote.Transport != api.RemoteMCP || remote.Endpoint == "" {
		t.Fatalf("remote manifest launch=%+v", remote)
	}
	fields := api.ConnectionFromOfficialPackage(map[string]any{"environmentVariables": []any{
		map[string]any{"name": "API_TOKEN", "isRequired": true, "isSecret": true},
		map[string]any{"name": "not a key"},
		"skip",
	}})
	if len(fields.Fields) != 1 || !fields.Fields[0].Required || !fields.Fields[0].Secret {
		t.Fatalf("official package fields=%+v", fields)
	}
}

func TestComposeLaunchBranches(t *testing.T) {
	const digest = "sha256:" + "ab"
	document := map[string]any{"services": map[string]any{
		"mcp-a": map[string]any{"image": "a@mcp"},
		"mcp-b": map[string]any{"image": "b@mcp"},
	}}
	if _, _, _, err := api.ParseComposeDocument(document, "compose.yaml"); err == nil {
		t.Fatal("ambiguous MCP services accepted")
	}
	host := map[string]any{"services": map[string]any{"mcp": map[string]any{
		"image":   "ghcr.io/acme/weather@" + digest + strings.Repeat("c", 62),
		"volumes": []any{"/host:/state"},
	}}}
	if _, _, _, err := api.ParseComposeDocument(host, "compose.yaml"); err == nil {
		t.Fatal("compose host mount accepted")
	}
	shell := map[string]any{"services": map[string]any{"mcp": map[string]any{
		"image":       "ghcr.io/acme/weather@" + digest + strings.Repeat("c", 62),
		"build":       true,
		"healthcheck": map[string]any{"test": []any{"CMD-SHELL", "curl localhost"}},
		"secrets":     []any{"API_TOKEN"},
	}}}
	launch, connection, warnings, err := api.ParseComposeDocument(shell, "compose.yaml")
	if err != nil || len(warnings) == 0 || len(connection.Fields) != 1 || !connection.Fields[0].Secret || launch.Health.Kind != "" {
		t.Fatalf("shell health/build branches: %+v %+v %v %v", launch, connection, warnings, err)
	}
	list := api.ComposeEnvironmentFields([]any{"API_TOKEN=example", "OTHER_KEY"})
	if len(list) != 2 {
		t.Fatalf("env list form=%+v", list)
	}
}

func TestResolveQueriesRegistriesBeforeRepoFiles(t *testing.T) {
	// Repo-derived image/version hints reach the lookup only after the first
	// catalog pass; a catalog observing them at call time proves repository
	// metadata was consulted before the registry answer existed.
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	var sawRepoHints atomic.Bool
	resolver := api.RecipeResolver{Catalogs: []api.RecipeCatalog{testRecipeCatalogFunc(func(_ context.Context, lookup api.RecipeLookup) ([]api.RecipeCandidate, error) {
		if len(lookup.ImageHints) > 0 || len(lookup.VersionHints) > 0 {
			sawRepoHints.Store(true)
		}
		return nil, nil
	})}}
	dockerfile := "FROM ghcr.io/acme/base@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nENTRYPOINT [\"/server\"]\n"
	if _, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile})); err != nil {
		t.Fatal(err)
	}
	if sawRepoHints.Load() {
		t.Fatal("catalog lookup received repo-derived hints: repository files were consulted before the catalog answer existed")
	}
}

func TestResolveLedgerPersistsFetches(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	resolver := api.RecipeResolver{Catalogs: []api.RecipeCatalog{testRecipeCatalogFunc(func(context.Context, api.RecipeLookup) ([]api.RecipeCandidate, error) {
		return []api.RecipeCandidate{
			{Source: "mcp-registry", Repository: source.Repository, CommitSHA: commit, LaunchRank: api.RankL3, CredentialRank: api.RankR2, Launch: api.LaunchRecipe{Artifact: "ghcr.io/acme/weather", Digest: digest, Transport: api.ContainerMCP}, Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{{Name: "API_TOKEN", Required: true, Secret: true, Delivery: "env"}}}},
			{Source: "mcp-registry", Repository: "https://github.com/evil/weather", CommitSHA: commit, LaunchRank: api.RankL3, Launch: api.LaunchRecipe{Artifact: "ghcr.io/evil/weather", Digest: digest, Transport: api.ContainerMCP}},
		}, nil
	})}}
	resolution, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"README.md": "needs API_TOKEN"}))
	if err != nil {
		t.Fatal(err)
	}
	var kept, ignored int
	for _, entry := range resolution.Ledger {
		if entry.Source != "mcp-registry" {
			continue
		}
		if entry.Digest == "" || entry.FetchedAt == "" {
			t.Fatalf("fetched claim lacks digest/fetched_at: %+v", entry)
		}
		if entry.Ignored {
			ignored++
		} else {
			kept++
		}
	}
	if kept == 0 {
		t.Fatalf("matched catalog candidate left no ledger entries: %+v", resolution.Ledger)
	}
	if ignored == 0 {
		t.Fatalf("mismatched catalog candidate was not recorded as ignored: %+v", resolution.Ledger)
	}
}

func TestResolveDisagreeingRepoClaimsStayDraft(t *testing.T) {
	// Ranked resolution makes "credential sources disagree" an explanation,
	// not a review trigger: the higher-rank file decides deterministically.
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := api.ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	resolution, err := (api.RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{
		"server.json":  `{"repository":"https://github.com/acme/weather","remotes":[{"url":"https://mcp.example/weather"}],"environment":{"API_TOKEN":"required"}}`,
		".env.example": "# API_TOKEN is optional here\nAPI_TOKEN=\n",
		"README.md":    "docs",
		"main.go":      "package main",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resolution.State == "review" {
		t.Fatalf("rank-resolvable disagreement forced review: %+v", resolution)
	}
}
