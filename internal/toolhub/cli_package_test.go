package toolhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParsePackageSource(t *testing.T) {
	cases := []struct {
		raw, registry, name, version string
	}{
		{"pypi:ruff@0.6.9", "pypi", "ruff", "0.6.9"},
		{"pypi:sqlfluff@latest", "pypi", "sqlfluff", "latest"},
		{"uvx:ruff@0.6.9", "pypi", "ruff", "0.6.9"},
		{"npm:prettier@3.3.3", "npm", "prettier", "3.3.3"},
		{"npm:@biomejs/biome@1.9.4", "npm", "@biomejs/biome", "1.9.4"},
		{"npx:typescript@5.6.3", "npm", "typescript", "5.6.3"},
		{"pypi:requests@2.32.3+local.1", "pypi", "requests", "2.32.3+local.1"},
	}
	for _, tc := range cases {
		source, err := parsePackageSource(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if source.PackageRegistry != tc.registry || source.PackageName != tc.name || source.PackageVersion != tc.version {
			t.Fatalf("parse %q: %+v", tc.raw, source)
		}
		if source.Repository != "" || source.Tag != "" || source.Asset != "" {
			t.Fatalf("parse %q leaked other-source fields: %+v", tc.raw, source)
		}
	}
}

func TestParsePackageSourceRejects(t *testing.T) {
	for _, raw := range []string{
		"", "pypi:", "pypi:ruff", "pypi:@0.1.0", "pypi:ruff@", "pypi:ruff@@1",
		"pypi:ruff@1.0..0", "pypi:na me@1.0", "pypi:ruff@latest;rm",
		"npm:UPPER@1.0.0", "npm:@scope@1.0.0", "npm:@scope/@1.0.0",
		"pypi:" + strings.Repeat("a", 200) + "@1.0", "go:mod@1.0",
		"pypi:ruff@*", "npm:pkg@^1.0.0", "npm:pkg@>=1",
	} {
		if _, err := parsePackageSource(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("source %q admitted: %v", raw, err)
		}
	}
}

func TestResolvePackageVersion(t *testing.T) {
	ctx := context.Background()
	if v, err := resolvePackageVersion(ctx, ArtifactSource{PackageRegistry: "pypi", PackageName: "ruff", PackageVersion: "0.6.9"}, nil); err != nil || v != "0.6.9" {
		t.Fatalf("explicit version: %q %v", v, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "pypi") || strings.HasPrefix(r.URL.Path, "/pypi/") {
			_, _ = w.Write([]byte(`{"info":{"version":"9.9.9"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":"7.7.7"}`))
	}))
	defer server.Close()
	oldPy, oldNpm := pypiMetadataURL, npmMetadataURL
	pypiMetadataURL, npmMetadataURL = server.URL+"/pypi/%s", server.URL+"/npm/%s"
	defer func() { pypiMetadataURL, npmMetadataURL = oldPy, oldNpm }()

	v, err := resolvePackageVersion(ctx, ArtifactSource{PackageRegistry: "pypi", PackageName: "ruff", PackageVersion: "latest"}, server.Client())
	if err != nil || v != "9.9.9" {
		t.Fatalf("pypi latest: %q %v", v, err)
	}
	v, err = resolvePackageVersion(ctx, ArtifactSource{PackageRegistry: "npm", PackageName: "prettier", PackageVersion: "latest"}, server.Client())
	if err != nil || v != "7.7.7" {
		t.Fatalf("npm latest: %q %v", v, err)
	}
}

func TestResolvePackageVersionRejectsGarbage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1.0;evil"}`))
	}))
	defer server.Close()
	old := npmMetadataURL
	npmMetadataURL = server.URL + "/%s"
	defer func() { npmMetadataURL = old }()
	if _, err := resolvePackageVersion(context.Background(), ArtifactSource{PackageRegistry: "npm", PackageName: "p", PackageVersion: "latest"}, server.Client()); !errors.Is(err, ErrStale) {
		t.Fatalf("garbage version admitted: %v", err)
	}
	if _, err := resolvePackageVersion(context.Background(), ArtifactSource{PackageRegistry: "cargo", PackageName: "p", PackageVersion: "latest"}, server.Client()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown registry admitted: %v", err)
	}
}

func TestGenerateCLIPackageRecipe(t *testing.T) {
	recipe, ctx, err := generateCLIPackageRecipe("pypi", "ruff", "0.6.9", "/usr/local/bin/ruff")
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Format != "cli-package-v1" || len(recipe.BaseImages) != 1 || !strings.HasPrefix(recipe.BaseImages[0], "python@sha256:") {
		t.Fatalf("pypi recipe: %+v", recipe)
	}
	if len(recipe.DependencyLocks) != 1 || recipe.Entrypoint[0] != "/usr/local/bin/ruff" {
		t.Fatalf("recipe pins: %+v", recipe)
	}
	dockerfile := string(ctx)
	if !strings.Contains(dockerfile, "pip install --no-cache-dir --disable-pip-version-check 'ruff==0.6.9'") || !strings.Contains(dockerfile, "USER 10001:10001") {
		t.Fatalf("pypi dockerfile:\n%s", dockerfile)
	}
	recipe, ctx, err = generateCLIPackageRecipe("npm", "prettier", "3.3.3", "/usr/local/bin/prettier")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recipe.BaseImages[0], "node@sha256:") || !strings.Contains(string(ctx), "npm install --global --omit=dev 'prettier@3.3.3'") {
		t.Fatalf("npm recipe: %+v\n%s", recipe, ctx)
	}
	if _, _, err := generateCLIPackageRecipe("cargo", "x", "1.0", "/usr/bin/x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown registry recipe admitted: %v", err)
	}
	if _, _, err := generateCLIPackageRecipe("pypi", "x", "1.0", "relative/path"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad binary path admitted: %v", err)
	}
}

func TestDefinitionSourcePackageValidation(t *testing.T) {
	valid := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "pkg-tool", Version: "1.0.0", Transport: BoundedCLI,
		Source: DefinitionSource{
			Image: "hermes-cli-artifact/pkg-tool", Digest: "sha256:" + strings.Repeat("1", 64),
			Command:         "/usr/local/bin/ruff",
			PackageRegistry: "pypi", PackageName: "ruff", PackageVersion: "0.6.9",
			ArchiveDigest:    "sha256:" + strings.Repeat("2", 64),
			ProvenanceDigest: "sha256:" + strings.Repeat("3", 64),
			SBOMDigest:       "sha256:" + strings.Repeat("4", 64),
			RecipeDigest:     "sha256:" + strings.Repeat("5", 64),
			ReviewDigest:     "sha256:" + strings.Repeat("6", 64),
		},
		Tools:     []ToolSpec{{Name: "lint", Description: "lint files", Effect: ReadEffect, CapabilityID: "cli.ruff", Uses: []CapabilityUse{{Action: "read", Resource: "files"}}}},
		Workload:  WorkloadPolicy{Class: PerUser, WorkspaceScope: "none", Rationale: "registry package tool; per-binding workspace"},
		Execution: ExecutionPolicy{TimeoutSeconds: 10, OutputBytes: 1024, CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"pypi.org"}},
		Health:    HealthProbe{Kind: "exec", Value: "/usr/local/bin/ruff", TimeoutSeconds: 5},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid package definition rejected: %v", err)
	}
	mixed := valid
	mixed.Source.CommitSHA = strings.Repeat("a", 40)
	if err := mixed.Validate(); err == nil {
		t.Fatal("package + commit provenance admitted")
	}
	released := valid
	released.Source.ReleaseAsset = "tool.tgz"
	if err := released.Validate(); err == nil {
		t.Fatal("package + release provenance admitted")
	}
	badRegistry := valid
	badRegistry.Source.PackageRegistry = "cargo"
	if err := badRegistry.Validate(); err == nil {
		t.Fatal("unknown registry admitted")
	}
	badVersion := valid
	badVersion.Source.PackageVersion = "^1.0"
	if err := badVersion.Validate(); err == nil {
		t.Fatal("non-exact version admitted")
	}
	partial := valid
	partial.Source.PackageName = ""
	if err := partial.Validate(); err == nil {
		t.Fatal("registry+version without package name admitted")
	}
}

func TestCliArtifactSourcePackageDispatch(t *testing.T) {
	c := &ControlPlane{}
	source, err := c.cliArtifactSource(context.Background(), map[string]any{"source": "pypi:ruff@0.6.9"})
	if err != nil || source.PackageRegistry != "pypi" || source.PackageName != "ruff" {
		t.Fatalf("pypi dispatch: %+v %v", source, err)
	}
	source, err = c.cliArtifactSource(context.Background(), map[string]any{"source": "npm:@scope/pkg@1.0.0"})
	if err != nil || source.PackageRegistry != "npm" || source.PackageName != "@scope/pkg" {
		t.Fatalf("npm dispatch: %+v %v", source, err)
	}
}

func TestImportCLIPackageArtifactValidation(t *testing.T) {
	ctx := context.Background()
	source := ArtifactSource{PackageRegistry: "pypi", PackageName: "ruff", PackageVersion: "0.6.9"}
	config := ArtifactImportConfig{Image: "hermes-cli-artifact/ruff-check", Entrypoint: []string{"/usr/local/bin/ruff"}}
	if _, err := ImportCLIPackageArtifact(ctx, ArtifactSource{}, config, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing package coordinates: %v", err)
	}
	bad := config
	bad.Image = "registry.example.com/img@sha256:" + strings.Repeat("1", 64)
	if _, err := ImportCLIPackageArtifact(ctx, source, bad, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest-pinned local image admitted: %v", err)
	}
	bad = config
	bad.BaseImage = "python"
	if _, err := ImportCLIPackageArtifact(ctx, source, bad, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cli.base on a package source admitted: %v", err)
	}
	bad = config
	bad.Entrypoint = nil
	if _, err := ImportCLIPackageArtifact(ctx, source, bad, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing entrypoint admitted: %v", err)
	}
	bad = config
	bad.Entrypoint = []string{"relative/ruff"}
	if _, err := ImportCLIPackageArtifact(ctx, source, bad, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative entrypoint admitted: %v", err)
	}
	bad = config
	bad.Entrypoint = []string{"/usr/bin/a", "/usr/bin/b"}
	if _, err := ImportCLIPackageArtifact(ctx, source, bad, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multi entrypoint admitted: %v", err)
	}
}

func TestPackageHTTPClientAndFetchErrors(t *testing.T) {
	client := packageHTTPClient()
	check := client.CheckRedirect
	mk := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if err := check(mk("http://pypi.org/x"), nil); err == nil {
		t.Fatal("http redirect admitted")
	}
	if err := check(mk("https://evil.example/x"), nil); err == nil {
		t.Fatal("off-registry redirect admitted")
	}
	if err := check(mk("https://pypi.org/x"), nil); err != nil {
		t.Fatalf("pypi redirect denied: %v", err)
	}
	if err := check(mk("https://files.pythonhosted.org/x"), nil); err != nil {
		t.Fatalf("files redirect denied: %v", err)
	}
	via := []*http.Request{mk("https://pypi.org/a"), mk("https://pypi.org/b"), mk("https://pypi.org/c")}
	if err := check(mk("https://pypi.org/d"), via); err == nil {
		t.Fatal("redirect chain over the bound admitted")
	}
	// fetchPackageJSON: non-200 and bad JSON are both errors.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	var out map[string]any
	if err := fetchPackageJSON(context.Background(), server.Client(), server.URL, &out); err == nil {
		t.Fatal("non-200 metadata accepted")
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not-json")) })
	if err := fetchPackageJSON(context.Background(), server.Client(), server.URL, &out); err == nil {
		t.Fatal("garbage metadata accepted")
	}
}
