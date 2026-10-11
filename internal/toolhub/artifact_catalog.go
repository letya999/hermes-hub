package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

var repositoryHTTPSURL = regexp.MustCompile(`https://[A-Za-z0-9.-]+`)

// ArtifactImportConfig is the small operator-facing part of automatic import.
// Source, recipe and OCI evidence are produced by the pipeline; the operator
// supplies only the MCP's declared contract (tools, effects and env names).
type ArtifactImportConfig struct {
	DefinitionID               string
	Version                    string
	Language                   string
	BaseImage                  string
	Entrypoint                 []string
	Image                      string
	Tools                      []ToolSpec
	Credentials                []CredentialInput
	CredentialGroups           [][]string
	CredentialContractID       string
	CredentialContractRevision int
	CredentialContractEnv      map[string]string
	Environment                []string
	RuntimeEnvironment         map[string]string
	ProxyEnvironment           []string
	Workload                   WorkloadPolicy
	Execution                  ExecutionPolicy
	Health                     HealthProbe
	// ContextSources merges additional pinned repositories into the build
	// context for upstream vendoring steps a tree fetch cannot reproduce.
	ContextSources []ArtifactOverlay
}

// ArtifactOverlay is a reviewed second source merged under a clean prefix.
type ArtifactOverlay struct {
	Source ArtifactSource `json:"source"`
	Into   string         `json:"into"`
}

type ImportedArtifact struct {
	Definition ToolDefinition    `json:"definition"`
	Recipe     ArtifactRecipe    `json:"recipe"`
	Artifact   StoredOCIArtifact `json:"artifact"`
}

type artifactReviewContract struct {
	DefinitionID               string
	Version                    string
	Image                      string
	Tools                      []ToolSpec
	Credentials                []CredentialInput
	CredentialGroups           [][]string
	CredentialContractID       string
	CredentialContractRevision int
	CredentialContractEnv      map[string]string
	Environment                []string
	RuntimeEnvironment         map[string]string
	ProxyEnvironment           []string
	Workload                   WorkloadPolicy
	Execution                  ExecutionPolicy
	Health                     HealthProbe
	ContextSources             []ArtifactOverlay
}

// ImportGitHubArtifact performs the complete source→recipe→isolated OCI path.
// It intentionally stops at quarantine: callers must review the returned
// secret-free definition before RegisterTrustedArtifact.
func ImportGitHubArtifact(ctx context.Context, source ArtifactSource, config ArtifactImportConfig, build RestrictedBuildConfig) (ImportedArtifact, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return ImportedArtifact{}, err
	}
	config = normalizeArtifactImportConfig(config)
	if !validCommand(config.Image) || strings.Contains(config.Image, "@") {
		return ImportedArtifact{}, fmt.Errorf("%w: local immutable image name required", ErrInvalid)
	}
	contextBytes, err := FetchRepositoryArtifactContext(ctx, source, 64<<20)
	if err != nil {
		return ImportedArtifact{}, err
	}
	for _, overlay := range config.ContextSources {
		if contextBytes, err = OverlayArtifactContext(ctx, overlay.Source, overlay.Into, contextBytes, 64<<20); err != nil {
			return ImportedArtifact{}, err
		}
	}
	if len(config.Execution.Egress) == 0 {
		config.Execution.Egress = discoverOpenAPIEgress(contextBytes)
	}
	if len(config.Execution.Egress) == 0 {
		config.Execution.Egress = []string{"127.0.0.1"}
	}
	config.ProxyEnvironment = mergeProxyEnvironment(config.ProxyEnvironment, discoverProxyEnvironment(contextBytes), claimedEnvironmentNames(config))
	var pythonExtras []string
	if entry, ok, lookupErr := preparedForSource(source); lookupErr != nil {
		return ImportedArtifact{}, lookupErr
	} else if ok {
		pythonExtras = entry.PythonExtras
	}
	recipe, generated, err := GenerateArtifactRecipe(contextBytes, config.Language, config.BaseImage, config.Entrypoint, pythonExtras...)
	if err != nil {
		return ImportedArtifact{}, err
	}
	artifact, err := BuildRestrictedOCI(ctx, recipe, generated, build)
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
	reviewDigest, err := reviewDigest(source, recipe, artifact, config)
	if err != nil {
		return ImportedArtifact{}, err
	}
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: config.DefinitionID, Version: config.Version,
		Transport: ContainerMCP,
		Source: DefinitionSource{
			Image: config.Image, Digest: artifact.Evidence.ImageManifestDigest,
			Command: firstArg(recipe.Entrypoint), Args: remainingArgs(recipe.Entrypoint),
			Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA,
			ArchiveDigest: artifact.ArchiveDigest, ProvenanceDigest: artifact.Evidence.ProvenanceDigest,
			SBOMDigest: artifact.Evidence.SBOMDigest, RecipeDigest: recipeDigest, ReviewDigest: reviewDigest,
			ContextSources: append([]ArtifactOverlay(nil), config.ContextSources...),
		},
		Tools: append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...), CredentialGroups: cloneStringGroups(config.CredentialGroups), CredentialContractID: config.CredentialContractID, CredentialContractRevision: config.CredentialContractRevision, CredentialContractEnv: config.CredentialContractEnv, Environment: append([]string(nil), config.Environment...),
		RuntimeEnvironment: config.RuntimeEnvironment, ProxyEnvironment: append([]string(nil), config.ProxyEnvironment...), Workload: config.Workload, Execution: config.Execution, Health: config.Health,
	}
	if err := definition.Validate(); err != nil {
		return ImportedArtifact{}, err
	}
	return ImportedArtifact{Definition: definition, Recipe: recipe, Artifact: artifact}, nil
}

// ImportPublishedArtifact turns a catalog-proven immutable OCI image into the
// same review packet used by the local build path. ArchiveDigest stays empty:
// the image digest, source labels and OCI attestations are the immutable
// evidence for this path.
func ImportPublishedArtifact(source ArtifactSource, config ArtifactImportConfig, resolution RecipeResolution, egress []string) (ImportedArtifact, error) {
	if resolution.State != "ready" || resolution.Launch.Transport != ContainerMCP || resolution.Source != source {
		return ImportedArtifact{}, fmt.Errorf("%w: published recipe is not ready", ErrInvalid)
	}
	if resolution.Launch.Artifact == "" || !digestPattern.MatchString(resolution.Launch.Digest) || len(resolution.Launch.Entrypoint) == 0 {
		return ImportedArtifact{}, fmt.Errorf("%w: published OCI image evidence is incomplete", ErrInvalid)
	}
	provenance, sbom := recipeAttestationDigests(resolution.Evidence)
	if provenance == "" || sbom == "" {
		return ImportedArtifact{}, fmt.Errorf("%w: published OCI attestations are incomplete", ErrUnauthorized)
	}
	config = normalizeArtifactImportConfig(config)
	config.Image = resolution.Launch.Artifact
	config.Entrypoint = append([]string(nil), resolution.Launch.Entrypoint...)
	config.Health = resolution.Launch.Health
	if config.Health.Value == "" {
		config.Health = HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5}
	}
	if len(egress) == 0 {
		egress = append([]string(nil), resolution.Launch.Network...)
	}
	if len(egress) == 0 {
		egress = []string{"127.0.0.1"}
	}
	config.Execution.Egress = append([]string(nil), egress...)
	config.Credentials = resolution.Connection.CredentialInputs()
	recipe := ArtifactRecipe{Format: "published-oci-v1", Entrypoint: append([]string(nil), resolution.Launch.Entrypoint...)}
	recipeDigest, err := publishedRecipeDigest(source, resolution)
	if err != nil {
		return ImportedArtifact{}, err
	}
	artifact := StoredOCIArtifact{Evidence: OCIArtifactEvidence{ImageManifestDigest: resolution.Launch.Digest, ProvenanceDigest: provenance, SBOMDigest: sbom}}
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: config.DefinitionID, Version: config.Version, Transport: ContainerMCP,
		Source: DefinitionSource{Image: config.Image, Digest: resolution.Launch.Digest, Command: firstArg(config.Entrypoint), Args: remainingArgs(config.Entrypoint), Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA, ProvenanceDigest: provenance, SBOMDigest: sbom, RecipeDigest: recipeDigest},
		Tools:  append([]ToolSpec(nil), config.Tools...), Credentials: append([]CredentialInput(nil), config.Credentials...), CredentialGroups: cloneStringGroups(config.CredentialGroups), CredentialContractID: config.CredentialContractID, CredentialContractRevision: config.CredentialContractRevision, CredentialContractEnv: config.CredentialContractEnv, Environment: append([]string(nil), config.Environment...), Workload: config.Workload, Execution: config.Execution, Health: config.Health,
	}
	imported := ImportedArtifact{Definition: definition, Recipe: recipe, Artifact: artifact}
	imported.Definition.RuntimeEnvironment = config.RuntimeEnvironment
	imported.Definition.ProxyEnvironment = append([]string(nil), config.ProxyEnvironment...)
	review, err := reviewDigestForImported(imported)
	if err != nil {
		return ImportedArtifact{}, err
	}
	imported.Definition.Source.ReviewDigest = review
	if err := imported.Definition.Validate(); err != nil {
		return ImportedArtifact{}, err
	}
	return imported, nil
}

func recipeAttestationDigests(evidence []RecipeEvidence) (string, string) {
	var provenance, sbom string
	for _, item := range evidence {
		if item.Source != "oci" || !digestPattern.MatchString(item.Digest) {
			continue
		}
		detail := strings.ToLower(item.Detail)
		if provenance == "" && strings.Contains(detail, "provenance") {
			provenance = item.Digest
		}
		if sbom == "" && strings.Contains(detail, "sbom") {
			sbom = item.Digest
		}
	}
	return provenance, sbom
}

func publishedRecipeDigest(source ArtifactSource, resolution RecipeResolution) (string, error) {
	encoded, err := json.Marshal(struct {
		Source     ArtifactSource
		Launch     LaunchRecipe
		Connection ConnectionRecipe
		Evidence   []RecipeEvidence
	}{source, resolution.Launch, resolution.Connection, resolution.Evidence})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func discoverOpenAPIEgress(contextBytes []byte) []string {
	reader := tar.NewReader(bytes.NewReader(contextBytes))
	hosts := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		name := strings.ToLower(path.Base(header.Name))
		if err != nil || header.Typeflag != tar.TypeReg || header.Size > 8<<20 {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, 8<<20))
		if readErr != nil {
			continue
		}
		if !strings.Contains(name, "openapi") || !strings.HasSuffix(name, ".json") {
			if egressDiscoveryFixturePath(header.Name) {
				continue
			}
			switch path.Ext(name) {
			case ".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".rs":
				for _, literal := range repositoryHTTPSURL.FindAllString(string(body), -1) {
					if parsed, parseErr := url.Parse(literal); parseErr == nil {
						if host, ok := egressDiscoveryHost(parsed.Hostname()); ok {
							hosts[host] = true
						}
					}
				}
			}
			continue
		}
		var document struct {
			Servers []struct {
				URL string `json:"url"`
			} `json:"servers"`
		}
		if json.Unmarshal(body, &document) != nil {
			continue
		}
		for _, server := range document.Servers {
			parsed, err := url.Parse(server.URL)
			if err == nil && parsed.Scheme == "https" && parsed.User == nil && parsed.Host == parsed.Hostname() {
				if host, ok := egressDiscoveryHost(parsed.Hostname()); ok {
					hosts[host] = true
				}
			}
		}
	}
	result := make([]string, 0, len(hosts))
	for host := range hosts {
		result = append(result, host)
	}
	slices.Sort(result)
	if len(result) > 32 {
		return nil
	}
	return result
}

// Proxy-env discovery patterns: the only accepted names end in _PROXY, so a
// discovered candidate can only ever receive the controller-owned egress URL.
// Worst case for a false positive is an unused env var or a visible startup
// failure, never an unreviewed network path.
var proxyEnvQuotedLiteral = regexp.MustCompile(`["'` + "`" + `]([A-Z][A-Z0-9_]{2,62}_PROXY)["'` + "`" + `]`)
var proxyEnvDotAccess = regexp.MustCompile(`\b(?:process|Bun)\.env\.([A-Z][A-Z0-9_]{2,62}_PROXY)\b`)
var proxyEnvAssignment = regexp.MustCompile(`(?m)^\s*(?:export\s+|ENV\s+)?([A-Z][A-Z0-9_]{2,62}_PROXY)\s*=`)

// proxyEnvironmentStandard are already injected by the workload runtime and
// proxyEnvironmentPlaceholder are literal noise, not real variable names.
var proxyEnvironmentStandard = map[string]bool{
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
	"FTP_PROXY": true, "RSYNC_PROXY": true, "GRPC_PROXY": true,
	"NO_PROXY": true, "no_proxy": true,
}

// discoverProxyEnvironment scans the repository source for custom proxy env
// vars a server reads at runtime — os.Getenv("X_PROXY") in Go,
// process.env.X_PROXY in Node, os.environ/os.getenv("X_PROXY") in Python and
// env::var("X_PROXY") in Rust — plus .env/Dockerfile-style assignments. The
// result feeds proxy_environment, so review sees the same names the binary
// would otherwise hide behind a bare --help.
func discoverProxyEnvironment(contextBytes []byte) []string {
	reader := tar.NewReader(bytes.NewReader(contextBytes))
	names := map[string]bool{}
	record := func(name string) {
		if proxyEnvironmentStandard[name] || strings.Contains(name, "NO_PROXY") {
			return
		}
		for _, filler := range []string{"EXAMPLE", "PLACEHOLDER", "DUMMY", "MOCK", "SAMPLE", "TEST", "FAKE"} {
			if strings.Contains(name, filler) {
				return
			}
		}
		names[name] = true
	}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size > 4<<20 {
			continue
		}
		if !proxyDiscoveryFilePath(header.Name) {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, 4<<20))
		if readErr != nil {
			continue
		}
		text := string(body)
		for _, match := range proxyEnvQuotedLiteral.FindAllStringSubmatch(text, 16) {
			record(match[1])
		}
		for _, match := range proxyEnvDotAccess.FindAllStringSubmatch(text, 16) {
			record(match[1])
		}
		for _, match := range proxyEnvAssignment.FindAllStringSubmatch(text, 16) {
			record(match[1])
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// proxyDiscoveryFilePath bounds the scan to plausible declaration sites:
// source files in the runtime languages, Dockerfiles and dotenv examples.
// Dependency trees and test fixtures are skipped for the same reason as in
// egress discovery — their names describe vendored code, not this server.
func proxyDiscoveryFilePath(name string) bool {
	lower := strings.ToLower(name)
	if egressDiscoveryFixturePath(name) {
		return false
	}
	for _, marker := range []string{"vendor/", "node_modules/", "third_party/", "/deps/", ".venv/", "site-packages/"} {
		if strings.HasPrefix(lower, marker) || strings.Contains(lower, "/"+marker) {
			return false
		}
	}
	base := path.Base(lower)
	switch path.Ext(base) {
	case ".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".rs", ".rb":
		return true
	}
	return base == "dockerfile" || strings.HasSuffix(base, ".dockerfile") ||
		base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env")
}

// mergeProxyEnvironment unions reviewed names with discovered ones.
// Names already claimed as user-supplied environment, credential or runtime
// inputs stay inputs: a *_PROXY name the server expects the deployer to set
// (like an OAuth callback proxy URL) is not a controller-injected egress var,
// and double-claiming it would trip the disjoint-namespace definition check.
func mergeProxyEnvironment(reviewed, discovered []string, claimed map[string]bool) []string {
	if len(discovered) == 0 {
		return reviewed
	}
	seen := map[string]bool{}
	for _, name := range reviewed {
		seen[name] = true
	}
	merged := append([]string(nil), reviewed...)
	for _, name := range discovered {
		if !seen[name] && !claimed[name] {
			seen[name] = true
			merged = append(merged, name)
		}
	}
	return merged
}

// claimedEnvironmentNames collects every name the import config already
// reserves for user-supplied inputs so proxy discovery cannot take them.
func claimedEnvironmentNames(config ArtifactImportConfig) map[string]bool {
	claimed := map[string]bool{}
	for _, name := range config.Environment {
		claimed[name] = true
	}
	for _, input := range config.Credentials {
		claimed[input.Name] = true
	}
	for name := range config.RuntimeEnvironment {
		claimed[name] = true
	}
	return claimed
}

// egressDiscoveryFixturePath skips files whose URLs are test fixtures rather
// than runtime destinations: unit tests, testdata trees and snapshot stores
// routinely contain attacker-controlled and reserved hostnames.
func egressDiscoveryFixturePath(name string) bool {
	lower := strings.ToLower(name)
	base := path.Base(lower)
	if strings.HasPrefix(lower, "testdata/") || strings.Contains(lower, "/testdata/") || strings.Contains(lower, "__tests__/") || strings.Contains(lower, "__fixtures__/") || strings.Contains(lower, "__snapshots__/") || strings.Contains(lower, "__toolsnaps__/") {
		return true
	}
	for _, marker := range []string{"_test.", ".test.", ".spec.", ".tests."} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return false
}

// egressDiscoveryHost normalizes a discovered literal and rejects hosts that
// can never be real egress destinations: reserved fixture TLDs (RFC 2606/6761),
// placeholder suffixes, names without a dot and non-global IP literals.
func egressDiscoveryHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || !strings.Contains(host, ".") || !hostPattern.MatchString(host) {
		return "", false
	}
	switch host[strings.LastIndex(host, ".")+1:] {
	case "example", "test", "invalid", "localhost", "local", "internal", "host", "hostname", "corp", "lan", "home":
		return "", false
	}
	for _, fixture := range []string{"example.com", "example.net", "example.org", "localhost"} {
		if host == fixture || strings.HasSuffix(host, "."+fixture) {
			return "", false
		}
	}
	// Discovered egress is DNS-only; a bare IP literal in source is a fixture
	// or an odd endpoint the reviewer can add explicitly.
	if _, err := netip.ParseAddr(host); err == nil {
		return "", false
	}
	return host, true
}

// RegisterTrustedArtifact is the only publication step. The review digest is
// recomputed from all immutable inputs, so changing source/artifact/contract
// cannot silently reuse a previous approval.
func (s *Store) RegisterTrustedArtifact(imported ImportedArtifact, approvedReviewDigest string) error {
	if s == nil || imported.Definition.Transport != ContainerMCP || approvedReviewDigest == "" || imported.Definition.Source.ReviewDigest != approvedReviewDigest {
		return fmt.Errorf("%w: artifact review approval required", ErrUnauthorized)
	}
	if expected, err := reviewDigestForImported(imported); err != nil || expected != approvedReviewDigest {
		return fmt.Errorf("%w: artifact review packet drift", ErrStale)
	}
	if err := ValidateTrustedArtifactDefinition(imported.Definition); err != nil {
		return err
	}
	if imported.Definition.Source.Digest != imported.Artifact.Evidence.ImageManifestDigest || imported.Definition.Source.ArchiveDigest != imported.Artifact.ArchiveDigest || imported.Definition.Source.ProvenanceDigest != imported.Artifact.Evidence.ProvenanceDigest || imported.Definition.Source.SBOMDigest != imported.Artifact.Evidence.SBOMDigest {
		return fmt.Errorf("%w: artifact evidence does not match definition", ErrStale)
	}
	entrypoint := append([]string{imported.Definition.Source.Command}, imported.Definition.Source.Args...)
	if len(imported.Definition.Source.Command) == 0 {
		entrypoint = nil
	}
	if len(entrypoint) != len(imported.Recipe.Entrypoint) {
		return fmt.Errorf("%w: artifact entrypoint drift", ErrStale)
	}
	for i := range entrypoint {
		if entrypoint[i] != imported.Recipe.Entrypoint[i] {
			return fmt.Errorf("%w: artifact entrypoint drift", ErrStale)
		}
	}
	return s.RegisterDefinition(imported.Definition)
}

func ValidateTrustedArtifactDefinition(definition ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if definition.Transport != ContainerMCP || definition.Source.Repository == "" || definition.Source.CommitSHA == "" || definition.Source.ProvenanceDigest == "" || definition.Source.SBOMDigest == "" || definition.Source.RecipeDigest == "" || definition.Source.ReviewDigest == "" {
		return fmt.Errorf("%w: immutable build evidence required", ErrUnauthorized)
	}
	if definition.Source.ArchiveDigest != "" && !digestPattern.MatchString(definition.Source.ArchiveDigest) {
		return fmt.Errorf("%w: invalid local artifact evidence", ErrInvalid)
	}
	return verifyDefinitionToolContract(definition)
}

func normalizeArtifactImportConfig(config ArtifactImportConfig) ArtifactImportConfig {
	if config.Image == "" && identity.ValidID(config.DefinitionID) {
		config.Image = "hermes-artifact/" + config.DefinitionID
	}
	if config.Workload.Class == "" {
		config.Workload.Class = PerUser
	}
	if config.Workload.ToolHiveVersion == "" {
		config.Workload.ToolHiveVersion = "v0.48.0"
	}
	if len(config.Workload.SidecarImages) == 0 {
		config.Workload.SidecarImages = []string{restrictedBuildProxyImage}
	}
	return config
}

func recipeDigest(recipe ArtifactRecipe) (string, error) {
	encoded, err := json.Marshal(recipe)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func reviewDigest(source ArtifactSource, recipe ArtifactRecipe, artifact StoredOCIArtifact, config ArtifactImportConfig) (string, error) {
	// Canonical JSON of immutable metadata is the review handle. It contains no
	// context bytes, credentials or secret values.
	value := struct {
		Source   ArtifactSource
		Recipe   ArtifactRecipe
		Artifact StoredOCIArtifact
		Contract artifactReviewContract
	}{Source: source, Recipe: recipe, Artifact: artifact, Contract: artifactReviewContract{config.DefinitionID, config.Version, config.Image, config.Tools, config.Credentials, config.CredentialGroups, config.CredentialContractID, config.CredentialContractRevision, config.CredentialContractEnv, config.Environment, config.RuntimeEnvironment, config.ProxyEnvironment, config.Workload, config.Execution, config.Health, config.ContextSources}}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func reviewDigestForImported(imported ImportedArtifact) (string, error) {
	source := ArtifactSource{Repository: imported.Definition.Source.Repository, Subfolder: imported.Definition.Source.Subfolder, CommitSHA: imported.Definition.Source.CommitSHA,
		Tag: imported.Definition.Source.ReleaseTag, Asset: imported.Definition.Source.ReleaseAsset, AssetDigest: imported.Definition.Source.AssetDigest,
		PackageRegistry: imported.Definition.Source.PackageRegistry, PackageName: imported.Definition.Source.PackageName, PackageVersion: imported.Definition.Source.PackageVersion}
	config := ArtifactImportConfig{
		DefinitionID: imported.Definition.DefinitionID, Version: imported.Definition.Version,
		Image: imported.Definition.Source.Image, Tools: imported.Definition.Tools,
		Entrypoint:  append([]string{imported.Definition.Source.Command}, imported.Definition.Source.Args...),
		Credentials: imported.Definition.Credentials, CredentialGroups: imported.Definition.CredentialGroups, CredentialContractID: imported.Definition.CredentialContractID, CredentialContractRevision: imported.Definition.CredentialContractRevision, CredentialContractEnv: imported.Definition.CredentialContractEnv, Environment: imported.Definition.Environment,
		RuntimeEnvironment: imported.Definition.RuntimeEnvironment, ProxyEnvironment: imported.Definition.ProxyEnvironment, Workload: imported.Definition.Workload, Execution: imported.Definition.Execution, Health: imported.Definition.Health,
		ContextSources: append([]ArtifactOverlay(nil), imported.Definition.Source.ContextSources...),
	}
	return reviewDigest(source, imported.Recipe, imported.Artifact, config)
}

func alignImportedEntrypoint(imported *ImportedArtifact) {
	if imported == nil || len(imported.Recipe.Entrypoint) == 0 {
		return
	}
	imported.Definition.Source.Command = firstArg(imported.Recipe.Entrypoint)
	imported.Definition.Source.Args = remainingArgs(imported.Recipe.Entrypoint)
}

func firstArg(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

func remainingArgs(argv []string) []string {
	if len(argv) < 2 {
		return nil
	}
	return append([]string(nil), argv[1:]...)
}

// ImportedReviewDigest is the immutable review handle operators pass to
// hubctl artifact approve --review-digest. It is recomputed from source,
// recipe, OCI evidence and the review contract; claimed tool lists alone
// cannot produce a trusted registration.
func ImportedReviewDigest(imported ImportedArtifact) (string, error) {
	return reviewDigestForImported(imported)
}

// ValidateImportedReview is useful to callers that persist the review packet
// outside the ToolHub store. It does not execute code or contact a registry.
func ValidateImportedReview(imported ImportedArtifact) error {
	if imported.Definition.Source.ReviewDigest == "" {
		return fmt.Errorf("%w: missing artifact review digest", ErrInvalid)
	}
	if imported.Artifact.ArchiveDigest == "" || imported.Artifact.Evidence.ImageManifestDigest == "" || imported.Artifact.Evidence.ProvenanceDigest == "" || imported.Artifact.Evidence.SBOMDigest == "" {
		return fmt.Errorf("%w: incomplete artifact evidence", ErrInvalid)
	}
	return imported.Definition.Validate()
}
