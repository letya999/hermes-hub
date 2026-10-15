package api

import (
	"archive/tar"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCLIPackageSourceParse(t *testing.T) {
	// source grammar: registry:name@version, exact or latest
	src, err := ParsePackageSource("pypi:mcp-server@1.2.3")
	if err != nil || src.PackageRegistry != "pypi" || src.PackageName != "mcp-server" {
		t.Fatalf("pypi: %+v %v", src, err)
	}
	if s, err := ParsePackageSource("npx:@scope/tool@latest"); err != nil || s.PackageRegistry != "npm" || s.PackageName != "@scope/tool" || s.PackageVersion != "latest" {
		t.Fatalf("npm: %+v %v", s, err)
	}
	for _, raw := range []string{"gem:x@1", "pypi:noname", "pypi:n@1..2", "pypi:bad name@1", "pypi:n@bad version!"} {
		if _, err := ParsePackageSource(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}

func TestPackageVersionResolution(t *testing.T) {
	// latest resolves against the pinned registry metadata URL (httptest seam)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"info":{"version":"9.9.9"},"version":"8.8.8"}`))
	}))
	defer srv.Close()
	oldPy, oldNpm := PypiMetadataURL, NpmMetadataURL
	defer func() { PypiMetadataURL, NpmMetadataURL = oldPy, oldNpm }()
	PypiMetadataURL = srv.URL + "/pypi/%s"
	NpmMetadataURL = srv.URL + "/npm/%s"
	v, err := ResolvePackageVersion(t.Context(), ArtifactSource{PackageRegistry: "pypi", PackageName: "x", PackageVersion: "latest"}, srv.Client())
	if err != nil || v != "9.9.9" {
		t.Fatalf("pypi resolve: %q %v", v, err)
	}
	v, err = ResolvePackageVersion(t.Context(), ArtifactSource{PackageRegistry: "npm", PackageName: "x", PackageVersion: "latest"}, srv.Client())
	if err != nil || v != "8.8.8" {
		t.Fatalf("npm resolve: %q %v", v, err)
	}
	// explicit versions never hit the registry
	v, err = ResolvePackageVersion(t.Context(), ArtifactSource{PackageRegistry: "pypi", PackageName: "x", PackageVersion: "1.0"}, nil)
	if err != nil || v != "1.0" {
		t.Fatalf("pinned: %q %v", v, err)
	}
	// registry returning garbage fails stale, not silently accepted
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"info":{"version":"not a version!"}}`))
	}))
	defer bad.Close()
	PypiMetadataURL = bad.URL + "/%s"
	if _, err := ResolvePackageVersion(t.Context(), ArtifactSource{PackageRegistry: "pypi", PackageName: "x", PackageVersion: "latest"}, bad.Client()); !errors.Is(err, ErrStale) {
		t.Fatalf("bad version: %v", err)
	}
}

func TestCLIPackageRecipeGeneration(t *testing.T) {
	// recipe generation pins the toolchain base and records the lock
	recipe, contextBytes, err := GenerateCLIPackageRecipe("pypi", "mcp-server", "1.2.3", "/tool")
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Format != "cli-package-v1" || len(recipe.BaseImages) != 1 || recipe.Entrypoint[0] != "/tool" || len(contextBytes) == 0 {
		t.Fatalf("recipe: %+v", recipe)
	}
	if _, _, err := GenerateCLIPackageRecipe("gem", "x", "1", "/t"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown registry")
	}
	if _, _, err := GenerateCLIPackageRecipe("pypi", "x", "1", "rel/path"); !errors.Is(err, ErrInvalid) {
		t.Fatal("guest path")
	}
}

func TestContextDockerfileDetection(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0600, Size: 4})
	_, _ = tw.Write([]byte("FROM"))
	_ = tw.Close()
	if !ContextHasDockerfile(buf.Bytes()) || ContextHasDockerfile([]byte("not tar")) {
		t.Fatal("dockerfile detection")
	}
}
