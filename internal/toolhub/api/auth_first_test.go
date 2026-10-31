package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericAuthFirstStopsBeforeArtifactExecution(t *testing.T) {
	var recipe ConnectionRecipe
	if err := json.Unmarshal([]byte(`{"auth_before_tools_list":true,"fields":[{"name":"CUSTOM_TOKEN","type":"token","required":true,"secret":true,"delivery":"env"}]}`), &recipe); err != nil {
		t.Fatal(err)
	}
	oldLoad, oldPull := StoredArtifactLoader, PublishedImagePull
	t.Cleanup(func() { StoredArtifactLoader, PublishedImagePull = oldLoad, oldPull })
	loads, pulls := 0, 0
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		loads++
		return "", errors.New("artifact must not load before auth")
	}
	PublishedImagePull = func(context.Context, string, string) error {
		pulls++
		return errors.New("artifact must not pull before auth")
	}
	ctx := context.WithValue(t.Context(), ConnectionRecipeContextKey{}, recipe)
	imported := ImportedArtifact{Definition: ToolDefinition{Source: DefinitionSource{Image: "ghcr.io/example/mcp", Digest: "sha256:" + strings.Repeat("a", 64), Repository: "https://github.com/example/mcp"}, Credentials: recipe.CredentialInputs()}}
	for _, published := range []bool{false, true} {
		var err error
		if published {
			_, _, err = PreflightPublishedArtifact(ctx, imported)
		} else {
			_, _, err = PreflightImportedArtifact(ctx, imported, t.TempDir())
		}
		var gate *CredentialGate
		if !errors.As(err, &gate) || len(gate.Names) != 1 || gate.Names[0] != "CUSTOM_TOKEN" {
			t.Errorf("published=%v missing declared credential gate: %v", published, err)
		}
	}
	if loads != 0 || pulls != 0 {
		t.Errorf("artifact executed before auth: loads=%d pulls=%d", loads, pulls)
	}
}

func TestAuthFirstRecipeSurvivesMergeAndFilter(t *testing.T) {
	var recipe ConnectionRecipe
	if err := json.Unmarshal([]byte(`{"auth_before_tools_list":true,"fields":[{"name":"CUSTOM_TOKEN","required":true,"delivery":"env","source":"server.json"}]}`), &recipe); err != nil {
		t.Fatal(err)
	}
	filtered, _ := FilterConnectionSource(recipe, "server.json")
	for _, got := range []ConnectionRecipe{mergeConnection(ConnectionRecipe{}, recipe), mergeConnection(recipe, ConnectionRecipe{}), filtered} {
		body, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"auth_before_tools_list":true`) {
			t.Error("auth-first requirement lost")
		}
	}
}

func TestInteractiveRecipeClassifiesUnsupported(t *testing.T) {
	var recipe ConnectionRecipe
	if err := json.Unmarshal([]byte(`{"interactive":true,"fields":[{"name":"CUSTOM_TOKEN","required":true,"delivery":"env","source":"server.json"}]}`), &recipe); err != nil {
		t.Fatal(err)
	}
	if err := recipe.Validate(); err != nil {
		t.Fatalf("declared interactive recipe rejected: %v", err)
	}
	filtered, _ := FilterConnectionSource(recipe, "server.json")
	for _, got := range []ConnectionRecipe{mergeConnection(ConnectionRecipe{}, recipe), mergeConnection(recipe, ConnectionRecipe{}), filtered} {
		if !got.Interactive {
			t.Error("interactive marker lost in merge/filter")
		}
	}
}

func TestSourceReviewInteractiveFailsUnsupported(t *testing.T) {
	oldFetch, oldBuild := fetchRecipeContext, restrictedBuild
	t.Cleanup(func() { fetchRecipeContext, restrictedBuild = oldFetch, oldBuild })
	body := recipeContextTar(t, map[string]string{"package.json": `{"name":"interactive-mcp","bin":"index.js"}`, "index.js": "// fixture"})
	fetchRecipeContext = func(context.Context, ArtifactSource, int64) ([]byte, error) { return body, nil }
	builds := 0
	restrictedBuild = func(context.Context, ArtifactRecipe, []byte, RestrictedBuildConfig) (StoredOCIArtifact, error) {
		builds++
		return StoredOCIArtifact{}, nil
	}
	ctx := context.WithValue(t.Context(), ConnectionRecipeContextKey{}, ConnectionRecipe{Interactive: true})
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := ArtifactSource{Repository: "https://github.com/example/interactive-mcp", CommitSHA: strings.Repeat("a", 40)}
	review, err := DefaultSourceReviewer(t.TempDir(), seccomp)(ctx, source, nil)
	if !errors.Is(err, ErrUnsupportedAuth) {
		t.Fatalf("interactive declaration did not classify unsupported: review=%+v err=%v", review, err)
	}
	if builds != 0 {
		t.Error("interactive server reached the builder")
	}
}

func TestSourceReviewAuthFirstDefersBuild(t *testing.T) {
	oldFetch, oldBuildFetch := fetchRecipeContext, fetchArtifactContext
	oldBuild, oldLoad, oldList := restrictedBuild, StoredArtifactLoader, LocalMCPToolList
	oldPrepared := preparedLookup
	t.Cleanup(func() {
		fetchRecipeContext, fetchArtifactContext = oldFetch, oldBuildFetch
		restrictedBuild, StoredArtifactLoader, LocalMCPToolList = oldBuild, oldLoad, oldList
		preparedLookup = oldPrepared
	})
	body := recipeContextTar(t, map[string]string{"package.json": `{"name":"custom-mcp","bin":"index.js"}`, "package-lock.json": `{"lockfileVersion":3,"packages":{}}`, "index.js": "// fixture"})
	fetchRecipeContext = func(context.Context, ArtifactSource, int64) ([]byte, error) { return body, nil }
	buildFetches := 0
	afterAuth := false
	fetchArtifactContext = func(context.Context, ArtifactSource, int64) ([]byte, error) {
		buildFetches++
		if afterAuth {
			return body, nil
		}
		return nil, errors.New("build must wait for credentials")
	}
	source := ArtifactSource{Repository: "https://github.com/example/custom-mcp", CommitSHA: strings.Repeat("a", 40)}
	recipe := ConnectionRecipe{AuthBeforeToolsList: true, Fields: []ConnectionField{{Name: "CUSTOM_TOKEN", Required: true, Secret: true, Delivery: "env"}}}
	preparedLookup = func(ArtifactSource) (PreparedEntry, bool, error) {
		return PreparedEntry{Source: source, Language: "node", Entrypoint: []string{"node", "/app/index.js"}, Connection: recipe, ContractID: "custom-token", ContractRevision: 1, RuntimeEnvironment: map[string]string{"CUSTOM_MODE": "read"}, ProxyEnvironment: []string{"CUSTOM_PROXY"}, Egress: []string{"api.example.com"}}, true, nil
	}
	ctx := context.WithValue(t.Context(), ConnectionRecipeContextKey{}, recipe)
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	review, err := DefaultSourceReviewer(t.TempDir(), seccomp)(ctx, source, nil)
	if err != nil {
		t.Errorf("declared auth-first review failed: %v", err)
	}
	if buildFetches != 0 {
		t.Error("build started before credential collection")
	}
	if !review.AdmissionPending || len(review.Definition.Tools) != 0 || review.Definition.Source.Digest != "" || len(review.Definition.Credentials) != 1 || review.Definition.Source.CommitSHA != source.CommitSHA {
		t.Error("missing unbuilt, pinned credential draft")
	}
	if review.Definition.CredentialContractID != "custom-token" || review.Definition.CredentialContractRevision != 1 || review.Definition.CredentialContractEnv["CUSTOM_TOKEN"] != "CUSTOM_TOKEN" || review.Definition.RuntimeEnvironment["CUSTOM_MODE"] != "read" || len(review.Definition.ProxyEnvironment) != 1 {
		t.Error("auth-first draft lost reviewed contract or runtime metadata")
	}
	afterAuth = true
	restrictedBuild = func(context.Context, ArtifactRecipe, []byte, RestrictedBuildConfig) (StoredOCIArtifact, error) {
		return StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("b", 64), Size: 1, Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("c", 64)}}, nil
	}
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) { return "synthetic-image", nil }
	listed := false
	LocalMCPToolList = func(ctx context.Context, imported ImportedArtifact) ([]ToolSpec, error) {
		listed = true
		opts := ProbeOptionsFrom(ctx)
		if opts.Values["CUSTOM_TOKEN"] != "synthetic-secret" || !opts.AllowNetwork {
			t.Error("authenticated probe lost credentials")
		}
		return []ToolSpec{{Name: "search", Effect: ReadEffect}}, nil
	}
	recipe.AuthBeforeToolsList = false
	ctx = context.WithValue(t.Context(), ConnectionRecipeContextKey{}, recipe)
	ctx = WithProbeOptions(ctx, ProbeOptions{Values: map[string]string{"CUSTOM_TOKEN": "synthetic-secret"}, AllowNetwork: true})
	review, err = DefaultSourceReviewer(t.TempDir(), seccomp)(ctx, source, nil)
	if err != nil || !listed || review.AdmissionPending || len(review.Definition.Tools) != 1 || review.Definition.Source.Digest == "" || buildFetches != 1 {
		t.Errorf("authenticated build/list did not complete: listed=%v builds=%d error=%v", listed, buildFetches, err)
	}
}

func TestSourceReviewCarriesBuiltEvidenceOnProbeFailure(t *testing.T) {
	oldFetch, oldBuildFetch := fetchRecipeContext, fetchArtifactContext
	oldBuild, oldLoad, oldList := restrictedBuild, StoredArtifactLoader, LocalMCPToolList
	oldPrepared := preparedLookup
	t.Cleanup(func() {
		fetchRecipeContext, fetchArtifactContext = oldFetch, oldBuildFetch
		restrictedBuild, StoredArtifactLoader, LocalMCPToolList = oldBuild, oldLoad, oldList
		preparedLookup = oldPrepared
	})
	body := recipeContextTar(t, map[string]string{"package.json": `{"name":"custom-mcp","bin":"index.js"}`, "package-lock.json": `{"lockfileVersion":3,"packages":{}}`, "index.js": "// fixture"})
	fetchRecipeContext = func(context.Context, ArtifactSource, int64) ([]byte, error) { return body, nil }
	fetchArtifactContext = func(context.Context, ArtifactSource, int64) ([]byte, error) { return body, nil }
	restrictedBuild = func(context.Context, ArtifactRecipe, []byte, RestrictedBuildConfig) (StoredOCIArtifact, error) {
		return StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("b", 64), Size: 1, Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("c", 64)}}, nil
	}
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) { return "synthetic-image", nil }
	// An ordinary startup failure — not a credential gate — still reports the
	// built artifact so a retry reuses it instead of rebuilding.
	LocalMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, errors.New("server exited with status 1")
	}
	source := ArtifactSource{Repository: "https://github.com/example/custom-mcp", CommitSHA: strings.Repeat("a", 40)}
	preparedLookup = func(ArtifactSource) (PreparedEntry, bool, error) { return PreparedEntry{}, false, nil }
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	review, err := DefaultSourceReviewer(t.TempDir(), seccomp)(t.Context(), source, nil)
	if err == nil {
		t.Fatal("ordinary probe failure was reported as success")
	}
	var gate *CredentialGate
	if errors.As(err, &gate) {
		t.Fatal("ordinary startup failure was masked as an auth gate")
	}
	if review.Definition.Source.Digest == "" || review.Definition.Source.ArchiveDigest == "" {
		t.Fatalf("built evidence was discarded: %+v", review.Definition.Source)
	}
}
