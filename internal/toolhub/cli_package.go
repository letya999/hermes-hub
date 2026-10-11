package toolhub

// Package install source: pypi:NAME@VER and npm:NAME@VER (uvx:/npx: aliases).
// The package name and exact version are the pin; the restricted build runs
// the installer inside the egress-allowlisted sandbox and the resulting image
// manifest digest carries the verified outcome, same as a release artifact.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	pypiNamePattern    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	npmNamePattern     = regexp.MustCompile(`^(@[a-z0-9._-]+/)?[a-z0-9._-]+$`)
	packageVersPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+!~-]{0,63}$`)
)

// parsePackageSource parses "pypi:NAME@VER" / "npm:NAME@VER" plus the
// toolchain-flavoured aliases "uvx:" and "npx:". "latest" resolves against
// the registry at import time and is then pinned to the resolved version.
func parsePackageSource(raw string) (ArtifactSource, error) {
	var registry, rest string
	for _, scheme := range []struct{ prefix, registry string }{
		{"pypi:", "pypi"}, {"uvx:", "pypi"}, {"npm:", "npm"}, {"npx:", "npm"},
	} {
		if strings.HasPrefix(raw, scheme.prefix) {
			registry, rest = scheme.registry, strings.TrimPrefix(raw, scheme.prefix)
			break
		}
	}
	if registry == "" {
		return ArtifactSource{}, fmt.Errorf("%w: package source must be pypi or npm", ErrInvalid)
	}
	at := strings.LastIndex(rest, "@")
	if at <= 0 || at == len(rest)-1 || strings.Contains(rest, "..") {
		return ArtifactSource{}, fmt.Errorf("%w: package source must be %s:NAME@VERSION", ErrInvalid, registry)
	}
	name, version := rest[:at], rest[at+1:]
	if !validPackageName(registry, name) {
		return ArtifactSource{}, fmt.Errorf("%w: invalid %s package name", ErrInvalid, registry)
	}
	if version != "latest" && !packageVersPattern.MatchString(version) {
		return ArtifactSource{}, fmt.Errorf("%w: package version must be exact or latest", ErrInvalid)
	}
	return ArtifactSource{PackageRegistry: registry, PackageName: name, PackageVersion: version}, nil
}

func validPackageName(registry, name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	switch registry {
	case "pypi":
		return pypiNamePattern.MatchString(name)
	case "npm":
		return npmNamePattern.MatchString(name)
	}
	return false
}

// packageHTTPClient follows only same-registry https hops — registry metadata
// does not redirect anywhere the package sandbox would not already permit.
func packageHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			host := req.URL.Hostname()
			if host != "pypi.org" && host != "files.pythonhosted.org" && host != "registry.npmjs.org" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func fetchPackageJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: package metadata request", ErrInvalid)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hermes-hub/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("package metadata request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("package metadata status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// Metadata endpoints are vars so tests can pin them to an httptest server.
var (
	pypiMetadataURL = "https://pypi.org/pypi/%s/json"
	npmMetadataURL  = "https://registry.npmjs.org/%s/latest"
)

// resolvePackageVersion pins a "latest" spec to the exact current version the
// registry reports; explicit versions pass through unchanged.
func resolvePackageVersion(ctx context.Context, source ArtifactSource, client *http.Client) (string, error) {
	if source.PackageVersion != "latest" {
		return source.PackageVersion, nil
	}
	var url string
	var pick func(body []byte) (string, error)
	switch source.PackageRegistry {
	case "pypi":
		url = fmt.Sprintf(pypiMetadataURL, source.PackageName)
		pick = func(body []byte) (string, error) {
			var parsed struct {
				Info struct {
					Version string `json:"version"`
				} `json:"info"`
			}
			return parsed.Info.Version, json.Unmarshal(body, &parsed)
		}
	case "npm":
		url = fmt.Sprintf(npmMetadataURL, source.PackageName)
		pick = func(body []byte) (string, error) {
			var parsed struct {
				Version string `json:"version"`
			}
			return parsed.Version, json.Unmarshal(body, &parsed)
		}
	default:
		return "", fmt.Errorf("%w: unknown package registry %q", ErrInvalid, source.PackageRegistry)
	}
	var body json.RawMessage
	if err := fetchPackageJSON(ctx, client, url, &body); err != nil {
		return "", err
	}
	version, err := pick(body)
	if err != nil || !packageVersPattern.MatchString(version) {
		return "", fmt.Errorf("%w: registry returned an invalid version", ErrStale)
	}
	return version, nil
}

// generateCLIPackageRecipe renders the toolchain Dockerfile for a registry
// package. Base images are the same digest-pinned toolchain images the cli
// recipe uses; the HTTP(S)_PROXY args reach RUN steps because the restricted
// build injects them as build args.
func generateCLIPackageRecipe(registry, name, version, binary string) (ArtifactRecipe, []byte, error) {
	if !validGuestBinaryPath(binary) {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: cli binary guest path required", ErrInvalid)
	}
	base := languageBaseImages["python"]
	install := "RUN pip install --no-cache-dir --disable-pip-version-check '" + name + "==" + version + "'"
	if registry == "npm" {
		base = languageBaseImages["node"]
		install = "RUN npm install --global --omit=dev '" + name + "@" + version + "'"
	} else if registry != "pypi" {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: unknown package registry %q", ErrInvalid, registry)
	}
	argv, _ := json.Marshal([]string{binary})
	dockerfile := "FROM " + base + "\n" +
		"ARG HTTP_PROXY\nARG HTTPS_PROXY\nARG NO_PROXY\n" +
		install + "\n" +
		"USER 10001:10001\n" +
		"ENTRYPOINT " + string(argv) + "\nCMD []\n"
	lock := []byte(registry + ":" + name + "==" + version + "\n")
	files := map[string][]byte{
		".hub/Dockerfile":  []byte(dockerfile),
		"package-lock.txt": lock,
	}
	recipe := ArtifactRecipe{Format: "cli-package-v1", Dockerfile: ".hub/Dockerfile", DependencyLocks: []string{"package-lock.txt"}, BaseImages: []string{base}, Entrypoint: []string{binary}}
	output, err := writeRecipeContext(&recipe, files)
	if err != nil {
		return ArtifactRecipe{}, nil, err
	}
	return recipe, output, recipe.VerifyContext(output)
}

// ImportCLIPackageArtifact resolves the package pin, runs the generated
// toolchain recipe through the restricted build, and records the registry
// coordinates as the definition's source provenance.
func ImportCLIPackageArtifact(ctx context.Context, source ArtifactSource, config ArtifactImportConfig, build RestrictedBuildConfig, client *http.Client) (ImportedArtifact, error) {
	if !validPackageName(source.PackageRegistry, source.PackageName) || source.PackageVersion == "" {
		return ImportedArtifact{}, fmt.Errorf("%w: package source requires registry, name and version", ErrInvalid)
	}
	config = normalizeArtifactImportConfig(config)
	if !validCommand(config.Image) || strings.Contains(config.Image, "@") {
		return ImportedArtifact{}, fmt.Errorf("%w: local immutable image name required", ErrInvalid)
	}
	if config.BaseImage != "" {
		return ImportedArtifact{}, fmt.Errorf("%w: cli base applies to release sources only; the registry picks the toolchain", ErrInvalid)
	}
	if len(config.Entrypoint) != 1 || !validGuestBinaryPath(config.Entrypoint[0]) {
		return ImportedArtifact{}, fmt.Errorf("%w: cli package entrypoint must be one guest binary path", ErrInvalid)
	}
	version, err := resolvePackageVersion(ctx, source, client)
	if err != nil {
		return ImportedArtifact{}, err
	}
	recipe, contextBytes, err := generateCLIPackageRecipe(source.PackageRegistry, source.PackageName, version, config.Entrypoint[0])
	if err != nil {
		return ImportedArtifact{}, err
	}
	artifact, err := BuildRestrictedOCI(ctx, recipe, contextBytes, build)
	if err != nil {
		return ImportedArtifact{}, err
	}
	if _, err := LoadStoredOCIArtifact(ctx, build.ArtifactDirectory, artifact.ArchiveDigest, config.Image, build.MaxArtifactBytes); err != nil {
		return ImportedArtifact{}, fmt.Errorf("%w: local artifact materialization failed", err)
	}
	recipeDigest, err := recipeDigest(recipe)
	if err != nil {
		return ImportedArtifact{}, err
	}
	reviewSource := source
	reviewSource.PackageVersion = version
	reviewDigest, err := reviewDigest(reviewSource, recipe, artifact, config)
	if err != nil {
		return ImportedArtifact{}, err
	}
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: config.DefinitionID, Version: config.Version,
		Transport: BoundedCLI,
		Source: DefinitionSource{
			Image: config.Image, Digest: artifact.Evidence.ImageManifestDigest,
			Command: firstArg(recipe.Entrypoint), Args: remainingArgs(recipe.Entrypoint),
			PackageRegistry: source.PackageRegistry, PackageName: source.PackageName, PackageVersion: version,
			ArchiveDigest: artifact.ArchiveDigest, ProvenanceDigest: artifact.Evidence.ProvenanceDigest,
			SBOMDigest: artifact.Evidence.SBOMDigest, RecipeDigest: recipeDigest, ReviewDigest: reviewDigest,
			ContextSources: append([]ArtifactOverlay(nil), config.ContextSources...),
		},
		Tools: append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...),
		RuntimeEnvironment: config.RuntimeEnvironment, Workload: config.Workload, Execution: config.Execution, Health: config.Health,
	}
	if err := definition.Validate(); err != nil {
		return ImportedArtifact{}, err
	}
	return ImportedArtifact{Definition: definition, Recipe: recipe, Artifact: artifact}, nil
}
