package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type testRecipeCatalogFunc func(context.Context, RecipeLookup) ([]RecipeCandidate, error)

func (f testRecipeCatalogFunc) Lookup(ctx context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
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
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	data := resolverContext(t, map[string]string{
		"server.json": `{"repository":"https://github.com/acme/weather","packages":[{"registryType":"oci","identifier":"ghcr.io/acme/weather@` + imageDigest + `","version":"1.2.3","command":"/app/server","environment":{"WEATHER_API_KEY":"required"}}]}`,
		"README.md":   "Set WEATHER_API_KEY=SECRET_VALUE_DO_NOT_LEAK",
	})
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, data)
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
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	compose := "services:\n  mcp:\n    image: ghcr.io/acme/weather@" + digest + "\n    command: [\"/app/server\"]\n    environment:\n      - WEATHER_API_KEY\n    volumes:\n      - state:/state:rw\n  postgres:\n    image: postgres@" + digest + "\n"
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"compose.yaml": compose}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Launch.Sidecars) != 1 || resolution.Launch.Sidecars[0] != "postgres" || resolution.Launch.Mounts[0].Source != "state" {
		t.Fatalf("compose sidecar or state was not recorded: %+v", resolution.Launch)
	}
	unsafe := "services:\n  mcp:\n    image: ghcr.io/acme/weather@" + digest + "\n    privileged: true\n    volumes:\n      - /host:/state\n"
	unsafeResolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"compose.yml": unsafe}))
	if err != nil || hasLaunch(unsafeResolution.Launch) || len(unsafeResolution.Warnings) == 0 {
		t.Fatalf("unsafe compose was not rejected for safe fallback: resolution=%+v err=%v", unsafeResolution, err)
	}
}

func TestRecipeResolverIgnoresComposeShellHealthcheck(t *testing.T) {
	const digest = "sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}
	compose := "services:\n  mcp:\n    image: ghcr.io/acme/weather@" + digest + "\n    healthcheck:\n      test: [\"CMD-SHELL\", \"curl localhost || exit 1\"]\n"
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"compose.yaml": compose}))
	if err != nil || resolution.Launch.Health.Kind != "" || len(resolution.Warnings) == 0 {
		t.Fatalf("shell healthcheck was not safely ignored: resolution=%+v err=%v", resolution, err)
	}
}

func TestRecipeResolverIgnoresNameOnlyCatalogAndRunsCatalogsConcurrently(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	var entered atomic.Int32
	var once sync.Once
	release := make(chan struct{})
	catalog := func(candidate RecipeCandidate) RecipeCatalog {
		return testRecipeCatalogFunc(func(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
			if entered.Add(1) == 2 {
				once.Do(func() { close(release) })
			}
			<-release
			return []RecipeCandidate{candidate}, nil
		})
	}
	resolver := RecipeResolver{Catalogs: []RecipeCatalog{
		catalog(RecipeCandidate{Repository: "https://github.com/other/weather", CommitSHA: commit, Launch: LaunchRecipe{Artifact: "ghcr.io/other/weather", Digest: digest, Transport: ContainerMCP, Entrypoint: []string{"/server"}}}),
		catalog(RecipeCandidate{Repository: source.Repository, CommitSHA: commit, Launch: LaunchRecipe{Artifact: "ghcr.io/acme/weather", Digest: digest, Transport: ContainerMCP, Entrypoint: []string{"/server"}}}),
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

func TestRecipeResolverPassesImageAndVersionHintsToCatalogs(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	var seen RecipeLookup
	resolver := RecipeResolver{Catalogs: []RecipeCatalog{testRecipeCatalogFunc(func(_ context.Context, lookup RecipeLookup) ([]RecipeCandidate, error) {
		seen = lookup
		return []RecipeCandidate{{Repository: source.Repository, CommitSHA: commit, Launch: LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example/weather"}}}, nil
	})}}
	_, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{
		"server.json": `{"repository":"https://github.com/acme/weather","packages":[{"registryType":"oci","identifier":"ghcr.io/acme/weather:1.0.0","version":"1.0.0"}]}`,
	}))
	if err != nil || len(seen.ImageHints) != 1 || seen.ImageHints[0] != "ghcr.io/acme/weather:1.0.0" || len(seen.VersionHints) != 1 || seen.VersionHints[0] != "1.0.0" {
		t.Fatalf("catalog hints=%+v err=%v", seen, err)
	}
}

func TestParseGitHubSourceKeepsPinnedSubfolder(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source, err := ParseGitHubSource("https://github.com/acme/weather/tree/" + commit + "/packages/mcp")
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
	recipe := &RecipeResolution{
		Source:     ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: commit},
		State:      "draft",
		Launch:     LaunchRecipe{Transport: ContainerMCP, Entrypoint: []string{"/app/server"}},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "API_TOKEN", Type: "secret", Required: true, Secret: true, Delivery: "env", Target: "API_TOKEN"}}},
	}
	fix := newControlFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		return SourceReview{Definition: definition, Permissions: toolNames(definition), Effects: effectNames(definition), ReviewDigest: "sha256:review", Recipe: recipe}, nil
	})
	if err := fix.store.PutGrant(OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
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
	if _, err := fix.control.Invoke(context.Background(), bobAuth(), "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("recipe crossed principal boundary: %v", err)
	}
}

func TestRecipeResolverParsesDockerfileMCPBAndScopedConnectionMetadata(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	dockerfile := "FROM ghcr.io/acme/base@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nENV API_TOKEN\nVOLUME [\"/state\"]\nHEALTHCHECK CMD [\"/app/health\"]\nENTRYPOINT [\"/app/server\"]\n"
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
	if err != nil || len(resolution.Launch.Entrypoint) != 1 || resolution.Launch.Mounts[0].Target != "/state" || resolution.Launch.Health.Kind != "exec" {
		t.Fatalf("Dockerfile recipe=%+v err=%v", resolution, err)
	}
	if len(resolution.Connection.Fields) != 1 || resolution.Connection.Fields[0].Name != "API_TOKEN" {
		t.Fatalf("Dockerfile connection=%+v", resolution.Connection)
	}

	mcpb := `{"manifest_version":"0.1","version":"1.0.0","entry_point":"/app/server","args":["--stdio"],"environment":{"API_TOKEN":{"delivery":"http_header","target":"Authorization","required":true}},"auth":{"oauth":{"url":"https://auth.example/authorize","env":{"CLIENT_ID":""}}}}`
	resolution, err = (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"manifest.json": mcpb}))
	if err != nil || resolution.Launch.Transport != BoundedCLI || len(resolution.Connection.Alternatives) != 1 {
		t.Fatalf("MCPB recipe=%+v err=%v", resolution, err)
	}
	if resolution.Connection.Fields[0].Delivery != "http_header" || resolution.Connection.Alternatives[0].Fields[0].Delivery != "oauth" {
		t.Fatalf("MCPB connection delivery=%+v", resolution.Connection)
	}

	scoped := ArtifactSource{Repository: source.Repository, CommitSHA: commit, Subfolder: "packages/mcp"}
	resolution, err = (RecipeResolver{}).Resolve(context.Background(), scoped, resolverContext(t, map[string]string{"packages/mcp/Dockerfile": dockerfile}))
	if err != nil || len(resolution.Launch.Entrypoint) != 1 {
		t.Fatalf("scoped recipe=%+v err=%v", resolution, err)
	}
}

func TestRecipeResolverDockerfileCacheMountsAreMetadataOnly(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/github/github-mcp-server", CommitSHA: commit}
	dockerfile := "FROM golang:1.25 AS build\n" +
		"WORKDIR /build\n" +
		"RUN --mount=type=cache,target=/go/pkg/mod \\\n" +
		"    --mount=type=cache,target=/root/.cache/go-build \\\n" +
		"    go build ./cmd/server\n" +
		"FROM gcr.io/distroless/base\n" +
		"COPY --from=build /build/server /server\n" +
		"RUN --mount=type=cache,target=/tmp/cache true\n" +
		"ENTRYPOINT [\"/server\"]\n"
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
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
	source := ArtifactSource{Repository: "https://github.com/github/github-mcp-server", CommitSHA: commit}
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
		resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
		if err != nil {
			t.Fatalf("%s: unsafe metadata became a fatal resolver error: %v", name, err)
		}
		if hasLaunch(resolution.Launch) || resolution.State == "ready" {
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
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	dockerfile := "# RUN --mount=type=secret,id=x looks unsafe but is a comment\n" +
		"FROM ghcr.io/acme/base@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"RUN echo \"--mount=type=secret\" && echo --privileged\n" +
		"RUN printf '# --network=host'\n" +
		"RUN echo /var/run/docker.sock\n" +
		"ENTRYPOINT [\"/server\"]\n"
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
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
	source := ArtifactSource{Repository: "https://github.com/github/github-mcp-server", CommitSHA: commit}
	resolver := RecipeResolver{Catalogs: []RecipeCatalog{testRecipeCatalogFunc(func(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
		return []RecipeCandidate{{Repository: source.Repository, CommitSHA: commit, Launch: LaunchRecipe{Artifact: "ghcr.io/github/github-mcp-server", Digest: digest, Transport: ContainerMCP, Entrypoint: []string{"/server"}}}}, nil
	})}}
	dockerfile := "RUN --mount=type=secret,id=oauth_client_secret go build\nENTRYPOINT [\"/server\"]\n"
	resolution, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"Dockerfile": dockerfile}))
	if err != nil || resolution.State != "review" {
		t.Fatalf("unsafe source metadata reached the ready/published boundary: %+v err=%v", resolution, err)
	}
}

func TestConnectionRecipeValidationAndDefinitionProjection(t *testing.T) {
	recipe := ConnectionRecipe{Fields: []ConnectionField{{Name: "API_TOKEN", Required: true, Delivery: "env"}}}
	if err := recipe.Validate(); err != nil || len(recipe.CredentialInputs()) != 1 || !recipe.CredentialInputs()[0].Required {
		t.Fatalf("valid connection recipe=%+v err=%v", recipe, err)
	}
	bad := recipe
	bad.Fields[0].Delivery = "shell"
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported delivery accepted: %v", err)
	}
	definition := userMCPDefinition()
	mergeConnectionForDefinition(recipe, &definition)
	if len(definition.Credentials) != 1 || definition.Credentials[0].Name != "API_TOKEN" {
		t.Fatalf("definition projection=%+v", definition.Credentials)
	}
}

func TestRecipeResolverRejectsInvalidInputs(t *testing.T) {
	if err := (ConnectionRecipe{Fields: []ConnectionField{{Name: "bad-name", Delivery: "env"}}}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid field accepted: %v", err)
	}
	if err := (ConnectionRecipe{Alternatives: []ConnectionAlternative{{Name: "oauth", URL: "http://auth.example"}}}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("insecure alternative accepted: %v", err)
	}
	if _, err := (RecipeResolver{}).Resolve(context.Background(), ArtifactSource{Repository: "bad", CommitSHA: "bad"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid source accepted: %v", err)
	}
	if _, err := scopeRecipeContext(map[string][]byte{"README.md": []byte("ok")}, "missing"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing subfolder accepted: %v", err)
	}
	if _, err := readRecipeContext([]byte("not-a-tar"), 64<<20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed context accepted: %v", err)
	}
	if _, err := recipeLookup(ArtifactSource{Repository: "https://github.com/only-owner", CommitSHA: strings.Repeat("a", 40)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed repository lookup accepted: %v", err)
	}
	if _, _, _, err := parseServerManifest([]byte("{"), "server.json", ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: strings.Repeat("a", 40)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed server manifest accepted: %v", err)
	}
}

func TestRecipeResolverAcceptsVerifiedRemoteCandidateAndBoundedContext(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	resolver := RecipeResolver{Catalogs: []RecipeCatalog{testRecipeCatalogFunc(func(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
		return []RecipeCandidate{{Repository: source.Repository, CommitSHA: commit, Launch: LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example/sse"}, Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "AUTH_JSON", Type: "json", Delivery: "json", Target: "request"}}}}}, nil
	})}}
	resolution, err := resolver.Resolve(context.Background(), source, resolverContext(t, map[string]string{"README.md": "remote"}))
	if err != nil || resolution.State != "ready" || resolution.Launch.Endpoint == "" || resolution.Connection.Fields[0].Delivery != "json" {
		t.Fatalf("remote resolution=%+v err=%v", resolution, err)
	}
	if _, err := (RecipeResolver{MaxContextBytes: 1}).Resolve(context.Background(), source, resolverContext(t, map[string]string{"README.md": "too large"})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized bounded context accepted: %v", err)
	}
}

func TestRecipeResolverMarksConflictingLocalLaunchesForReview(t *testing.T) {
	warnings := []string{}
	left := LaunchRecipe{Transport: ContainerMCP, Entrypoint: []string{"/one"}}
	right := LaunchRecipe{Transport: ContainerMCP, Entrypoint: []string{"/two"}}
	merged := mergeLaunch(left, right, &warnings)
	if !sameLaunch(merged, left) || len(warnings) != 1 {
		t.Fatalf("conflict was guessed instead of recorded: merged=%+v warnings=%v", merged, warnings)
	}
}

func TestRecipeResolverReadsMCPConfigAndExampleEnvWithoutValues(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/weather", CommitSHA: commit}
	contextBytes := resolverContext(t, map[string]string{
		".mcp.json":    `{"mcpServers":{"weather":{"command":"node","args":["server.js"],"env":{"WEATHER_API_KEY":"do-not-copy"}}}}`,
		".env.example": "WEATHER_API_KEY=example-value\nWEATHER_REGION=\n",
	})
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, contextBytes)
	if err != nil || resolution.Launch.Transport != BoundedCLI || len(resolution.Launch.Entrypoint) != 1 || resolution.Launch.Entrypoint[0] != "node" {
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
	recipe := parseExampleEnv([]byte("TRANSPORT=stdio\nPORT=3000\nHOST=127.0.0.1\nGOOGLE_OAUTH_CREDENTIALS=./gcp-oauth.keys.json\n"), ".env.example")
	if len(recipe.Fields) != 1 {
		t.Fatalf("runtime defaults became credential fields: %+v", recipe.Fields)
	}
	field := recipe.Fields[0]
	if field.Name != "GOOGLE_OAUTH_CREDENTIALS" || field.Type != "json" || field.Delivery != "json" || !field.Secret {
		t.Fatalf("OAuth JSON credential was misclassified: %+v", field)
	}
	recipe = parseExampleEnv([]byte("GITLAB_API_URL=https://gitlab.com\nGITLAB_TOKEN=placeholder\nGITLAB_TOKEN_TEST=test-only\nGITLAB_ALLOWED_PROJECT_IDS=\nHTTP_PROXY=\nHTTPS_PROXY=\nNO_PROXY=localhost\nTEST_PROJECT_ID=1\n"), ".env.example")
	byName := map[string]ConnectionField{}
	for _, f := range recipe.Fields {
		byName[f.Name] = f
	}
	if len(recipe.Fields) != 2 || !byName["GITLAB_TOKEN"].Required || byName["GITLAB_ALLOWED_PROJECT_IDS"].Required {
		t.Fatalf("proxy variables, test fixtures or optional knobs became required credentials: %+v", recipe.Fields)
	}
}

func TestRecipeResolverMetadataHelpersDoNotGuessValues(t *testing.T) {
	if got := anyStringMap(map[any]any{"API_TOKEN": "x"})["API_TOKEN"]; got != "x" {
		t.Fatalf("string map conversion: %#v", got)
	}
	if composeArgs("shell form") != nil || len(composeNames(map[any]any{"API_TOKEN": nil})) != 1 || composeHealth(map[string]any{"test": []any{"CMD", "/health"}}) != "CMD /health" {
		t.Fatal("compose metadata helpers lost their safe declarations")
	}
	documentation := parseDocumentationCredentials([]byte("API_TOKEN=do-not-copy\nCLIENT_ID\nignored"), "README.md")
	if len(documentation.Fields) != 2 || documentation.Fields[0].Name == "" {
		t.Fatalf("documentation credentials: %+v", documentation)
	}
	if strings.Contains(documentation.Fields[0].Name, "do-not-copy") {
		t.Fatal("documentation value entered recipe")
	}
	fromMap := connectionFromMap(map[string]any{"auth": map[string]any{
		"API_TOKEN": map[string]any{"delivery": "file", "path": "/run/secrets/API_TOKEN", "required": false, "secret": true},
	}}, "auth")
	if len(fromMap.Fields) != 1 || fromMap.Fields[0].Delivery != "file" || fromMap.Fields[0].Target != "/run/secrets/API_TOKEN" || fromMap.Fields[0].Required {
		t.Fatalf("map connection metadata: %+v", fromMap)
	}
	alternatives := connectionAlternatives(map[string]any{"oauth": map[string]any{
		"browser": map[string]any{"authorization_url": "https://auth.example/authorize", "env": map[string]any{"CLIENT_ID": ""}},
	}})
	if len(alternatives.Alternatives) != 1 || alternatives.Alternatives[0].URL == "" || len(alternatives.Alternatives[0].Fields) != 1 {
		t.Fatalf("OAuth alternative metadata: %+v", alternatives)
	}
	if !launchReady(LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example/sse"}) || launchReady(LaunchRecipe{Transport: RemoteMCP, Endpoint: "not-a-url"}) {
		t.Fatal("remote launch readiness was guessed")
	}
	if name, digest, ok := splitImmutableImage("ghcr.io/acme/weather@sha256:" + strings.Repeat("a", 64)); !ok || name == "" || digest == "" {
		t.Fatal("valid immutable image rejected")
	}
	if _, _, ok := splitImmutableImage("ghcr.io/acme/weather:latest"); ok || stringArray(json.RawMessage(`["/server","--stdio"]`))[0] != "/server" || stringArray([]any{"bad\nvalue"}) != nil {
		t.Fatal("unsafe or mutable metadata accepted")
	}
	if envKey(" API_TOKEN=example") != "API_TOKEN" || connectionType("CLIENT_ID") != "oauth" || !secretName("PRIVATE_KEY") {
		t.Fatal("credential metadata classification failed")
	}
}

func TestRecipeResolverParserBranches(t *testing.T) {
	if _, _, err := parseMCPJSON([]byte("{"), ".mcp.json"); err == nil {
		t.Fatal("malformed .mcp.json accepted")
	}
	for name, document := range map[string]string{
		"http endpoint":  `{"mcpServers":{"a":{"url":"http://mcp.example/sse"}}}`,
		"query endpoint": `{"mcpServers":{"a":{"url":"https://mcp.example/sse?x=1"}}}`,
		"unsafe command": `{"mcpServers":{"a":{"command":"rm -rf /"}}}`,
	} {
		if _, _, err := parseMCPJSON([]byte(document), ".mcp.json"); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	multi, _, err := parseMCPJSON([]byte(`{"mcpServers":{"a":{"command":"node"},"b":{"url":"https://mcp.example/sse"}}}`), ".mcp.json")
	if err != nil || hasLaunch(multi) {
		t.Fatalf("multi-server .mcp.json guessed a launch: %+v err=%v", multi, err)
	}
	if _, _, err := parseMCPBManifest([]byte("{"), "manifest.json"); err == nil {
		t.Fatal("malformed MCPB manifest accepted")
	}
	if _, _, err := parseMCPBManifest([]byte(`{"entry_point":"a;b"}`), "manifest.json"); err == nil {
		t.Fatal("unsafe MCPB entrypoint accepted")
	}
	launch := manifestPackageLaunch(map[string]any{
		"identifier": "ghcr.io/acme/weather", "digest": "sha256:" + strings.Repeat("b", 64),
		"transportType": "stdio", "runtimeHint": "node",
		"runtimeArguments": []any{map[string]any{"value": "--flag"}, map[string]any{"value": "bad\narg"}, "junk"},
		"command":          "node",
	})
	if launch.Digest == "" || len(launch.Args) != 1 || launch.Entrypoint[0] != "node" {
		t.Fatalf("manifest package launch=%+v", launch)
	}
	remote := manifestPackageLaunch(map[string]any{"transport": map[string]any{"type": "http", "url": "https://mcp.example/sse"}})
	if remote.Transport != RemoteMCP || remote.Endpoint == "" {
		t.Fatalf("remote manifest launch=%+v", remote)
	}
	fields := connectionFromOfficialPackage(map[string]any{"environmentVariables": []any{
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
	if _, _, _, err := parseComposeDocument(document, "compose.yaml"); err == nil {
		t.Fatal("ambiguous MCP services accepted")
	}
	host := map[string]any{"services": map[string]any{"mcp": map[string]any{
		"image":   "ghcr.io/acme/weather@" + digest + strings.Repeat("c", 62),
		"volumes": []any{"/host:/state"},
	}}}
	if _, _, _, err := parseComposeDocument(host, "compose.yaml"); err == nil {
		t.Fatal("compose host mount accepted")
	}
	shell := map[string]any{"services": map[string]any{"mcp": map[string]any{
		"image":       "ghcr.io/acme/weather@" + digest + strings.Repeat("c", 62),
		"build":       true,
		"healthcheck": map[string]any{"test": []any{"CMD-SHELL", "curl localhost"}},
		"secrets":     []any{"API_TOKEN"},
	}}}
	launch, connection, warnings, err := parseComposeDocument(shell, "compose.yaml")
	if err != nil || len(warnings) == 0 || len(connection.Fields) != 1 || !connection.Fields[0].Secret || launch.Health.Kind != "" {
		t.Fatalf("shell health/build branches: %+v %+v %v %v", launch, connection, warnings, err)
	}
	list := composeEnvironmentFields([]any{"API_TOKEN=example", "OTHER_KEY"})
	if len(list) != 2 {
		t.Fatalf("env list form=%+v", list)
	}
}
