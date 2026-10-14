package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
)

// LaunchRecipe is the secret-free description of one MCP workload. It is a
// plan only: Resolver never executes its command, image, compose file or
// healthcheck.
type LaunchRecipe struct {
	Artifact   string           `json:"artifact,omitempty"`
	Version    string           `json:"version,omitempty"`
	Digest     string           `json:"digest,omitempty"`
	Transport  Transport        `json:"transport"`
	Endpoint   string           `json:"endpoint,omitempty"`
	Entrypoint []string         `json:"entrypoint,omitempty"`
	Args       []string         `json:"args,omitempty"`
	Network    []string         `json:"network,omitempty"`
	Mounts     []Mount          `json:"mounts,omitempty"`
	State      string           `json:"state,omitempty"`
	Health     HealthProbe      `json:"health,omitempty"`
	Sidecars   []string         `json:"sidecars,omitempty"`
	Evidence   []RecipeEvidence `json:"evidence,omitempty"`
}

type ConnectionField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Delivery    string `json:"delivery"`
	Target      string `json:"target,omitempty"`
	Alternative string `json:"alternative,omitempty"`
}

type ConnectionAlternative struct {
	Name   string            `json:"name"`
	Fields []ConnectionField `json:"fields,omitempty"`
	URL    string            `json:"url,omitempty"`
}

type ConnectionRecipe struct {
	Fields       []ConnectionField       `json:"fields,omitempty"`
	Alternatives []ConnectionAlternative `json:"alternatives,omitempty"`
}

func (r ConnectionRecipe) Validate() error {
	seen := map[string]bool{}
	for _, field := range r.Fields {
		if !credentialPattern.MatchString(field.Name) || seen[field.Name] {
			return fmt.Errorf("%w: invalid connection field", ErrInvalid)
		}
		if field.Delivery != "env" && field.Delivery != "file" && field.Delivery != "json" && field.Delivery != "http_header" && field.Delivery != "oauth" {
			return fmt.Errorf("%w: unsupported connection delivery", ErrInvalid)
		}
		if field.Target != "" && strings.ContainsAny(field.Target, "\x00\r\n") {
			return fmt.Errorf("%w: invalid connection target", ErrInvalid)
		}
		seen[field.Name] = true
	}
	for _, alternative := range r.Alternatives {
		if alternative.Name == "" || len(alternative.Name) > 128 || strings.ContainsAny(alternative.Name, "\x00\r\n") || (alternative.URL != "" && (!strings.HasPrefix(strings.ToLower(alternative.URL), "https://") || hostOfURL(alternative.URL) == "")) {
			return fmt.Errorf("%w: invalid connection alternative", ErrInvalid)
		}
		if err := (ConnectionRecipe{Fields: alternative.Fields}).Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r ConnectionRecipe) CredentialInputs() []CredentialInput {
	normalized := normalizeConnection(r)
	inputs := make([]CredentialInput, 0, len(normalized.Fields))
	for _, field := range normalized.Fields {
		inputs = append(inputs, CredentialInput{Name: field.Name, Required: field.Required})
	}
	return inputs
}

type RecipeEvidence struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest,omitempty"`
	Detail string `json:"detail"`
}

type RecipeLookup struct {
	Repository   string
	Subfolder    string
	CommitSHA    string
	Owner        string
	Name         string
	ImageHints   []string
	VersionHints []string
}

// RecipeCandidate is the bounded result returned by one external catalog.
// Repository and commit are mandatory for admission; a matching name alone is
// deliberately not enough.
type RecipeCandidate struct {
	Repository string           `json:"repository"`
	Subfolder  string           `json:"subfolder,omitempty"`
	CommitSHA  string           `json:"commit_sha,omitempty"`
	Launch     LaunchRecipe     `json:"launch"`
	Connection ConnectionRecipe `json:"connection,omitempty"`
	Evidence   []RecipeEvidence `json:"evidence,omitempty"`
}

type RecipeCatalog interface {
	Lookup(context.Context, RecipeLookup) ([]RecipeCandidate, error)
}

type RecipeResolution struct {
	Source     ArtifactSource   `json:"source"`
	Launch     LaunchRecipe     `json:"launch"`
	Connection ConnectionRecipe `json:"connection"`
	Evidence   []RecipeEvidence `json:"evidence,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	State      string           `json:"state"`
}

type RecipeResolver struct {
	Catalogs        []RecipeCatalog
	MaxContextBytes int64
}

func (r RecipeResolver) Resolve(ctx context.Context, source ArtifactSource, contextBytes []byte) (RecipeResolution, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return RecipeResolution{}, err
	}
	max := r.MaxContextBytes
	if max == 0 {
		max = 64 << 20
	}
	files, err := readRecipeContext(contextBytes, max)
	if err != nil {
		return RecipeResolution{}, err
	}
	scopedFiles, err := scopeRecipeContext(files, source.Subfolder)
	if err != nil {
		return RecipeResolution{}, err
	}
	lookup, err := recipeLookup(source)
	if err != nil {
		return RecipeResolution{}, err
	}
	resolution := RecipeResolution{Source: source, State: "draft"}
	resolution.Evidence = contextEvidence(scopedFiles)

	localLaunch, localConnection, warnings, err := resolveLocalRecipe(scopedFiles, source)
	if err != nil {
		return RecipeResolution{}, err
	}
	resolution.Launch = localLaunch
	resolution.Connection = localConnection
	resolution.Warnings = append(resolution.Warnings, warnings...)
	lookup.ImageHints = recipeImageHints(localLaunch, scopedFiles)
	if localLaunch.Version != "" {
		lookup.VersionHints = []string{localLaunch.Version}
	}

	candidates := lookupCatalogs(ctx, r.Catalogs, lookup)
	for _, candidate := range candidates {
		if !candidateMatches(candidate, source) {
			resolution.Warnings = append(resolution.Warnings, "catalog candidate ignored: repository or commit did not match the pinned source")
			continue
		}
		resolution.Evidence = append(resolution.Evidence, candidate.Evidence...)
		resolution.Evidence = append(resolution.Evidence, candidate.Launch.Evidence...)
		if candidate.Launch.Artifact != "" || candidate.Launch.Endpoint != "" || len(candidate.Launch.Entrypoint) > 0 {
			if !sameLaunch(resolution.Launch, candidate.Launch) && hasLaunch(resolution.Launch) {
				resolution.State = "review"
				resolution.Warnings = append(resolution.Warnings, "matching catalogs returned conflicting launch recipes")
			} else if !hasLaunch(resolution.Launch) || resolution.State != "review" {
				resolution.Launch = candidate.Launch
			}
		}
		resolution.Connection = mergeConnection(resolution.Connection, candidate.Connection)
	}

	resolution.Connection = normalizeConnection(resolution.Connection)
	if err := resolution.Connection.Validate(); err != nil {
		return RecipeResolution{}, err
	}
	if !hasLaunch(resolution.Launch) {
		resolution.Warnings = append(resolution.Warnings, "no complete launch recipe was proven; existing language fallback remains required")
	}
	for _, warning := range resolution.Warnings {
		if strings.Contains(warning, "conflict") || strings.Contains(warning, "disagree") || strings.Contains(warning, "unsafe") {
			resolution.State = "review"
		}
	}
	if resolution.State != "review" && launchReady(resolution.Launch) {
		resolution.State = "ready"
	}
	slices.Sort(resolution.Warnings)
	return resolution, nil
}

func scopeRecipeContext(files map[string][]byte, subfolder string) (map[string][]byte, error) {
	if subfolder == "" {
		return files, nil
	}
	if !validGitHubSubfolder(subfolder) {
		return nil, fmt.Errorf("%w: invalid source subfolder", ErrInvalid)
	}
	prefix := strings.TrimSuffix(subfolder, "/") + "/"
	scoped := map[string][]byte{}
	for name, content := range files {
		if relative, ok := strings.CutPrefix(name, prefix); ok && relative != "" && recipeContextPath(relative) {
			scoped[relative] = content
		}
	}
	if len(scoped) == 0 {
		return nil, fmt.Errorf("%w: source subfolder has no safe files", ErrInvalid)
	}
	return scoped, nil
}

func recipeContextPath(name string) bool {
	if artifactContextPath(name) {
		return true
	}
	if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00\r\n") {
		return false
	}
	base := strings.ToLower(path.Base(name))
	return base == ".mcp.json" || base == ".env.example"
}

func recipeLookup(source ArtifactSource) (RecipeLookup, error) {
	u := strings.TrimPrefix(source.Repository, "https://github.com/")
	parts := strings.Split(u, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return RecipeLookup{}, fmt.Errorf("%w: source repository lookup", ErrInvalid)
	}
	return RecipeLookup{Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA, Owner: parts[0], Name: parts[1]}, nil
}

func lookupCatalogs(ctx context.Context, catalogs []RecipeCatalog, lookup RecipeLookup) []RecipeCandidate {
	if len(catalogs) == 0 {
		return nil
	}
	results := make([][]RecipeCandidate, len(catalogs))
	var pending sync.WaitGroup
	for i, catalog := range catalogs {
		if catalog == nil {
			continue
		}
		pending.Add(1)
		go func(i int, catalog RecipeCatalog) {
			defer pending.Done()
			candidates, err := catalog.Lookup(ctx, lookup)
			if err == nil && len(candidates) <= 32 {
				results[i] = candidates
			}
		}(i, catalog)
	}
	pending.Wait()
	var flat []RecipeCandidate
	for _, candidates := range results {
		flat = append(flat, candidates...)
	}
	return flat
}

func candidateMatches(candidate RecipeCandidate, source ArtifactSource) bool {
	if !sameRepository(candidate.Repository, source.Repository) || candidate.Subfolder != source.Subfolder {
		return false
	}
	return candidate.CommitSHA != "" && strings.EqualFold(candidate.CommitSHA, source.CommitSHA)
}

func sameRepository(left, right string) bool {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(left)), "/") == strings.TrimRight(strings.ToLower(strings.TrimSpace(right)), "/")
}

func readRecipeContext(data []byte, maxBytes int64) (map[string][]byte, error) {
	if maxBytes < 1 || maxBytes > 64<<20 || int64(len(data)) > maxBytes+1<<20 {
		return nil, fmt.Errorf("%w: resolver context is oversized", ErrInvalid)
	}
	reader := tar.NewReader(bytes.NewReader(data))
	files := map[string][]byte{}
	seen := map[string]bool{}
	var total int64
	for len(files) < 4096 {
		header, err := reader.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxBytes-total || !recipeContextPath(header.Name) || path.Clean(header.Name) != header.Name || seen[header.Name] {
			return nil, fmt.Errorf("%w: unsafe resolver context", ErrInvalid)
		}
		content, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(content)) != header.Size {
			return nil, fmt.Errorf("%w: invalid resolver context file", ErrInvalid)
		}
		files[header.Name] = content
		seen[header.Name] = true
		total += header.Size
	}
	return nil, fmt.Errorf("%w: resolver context has too many files", ErrInvalid)
}

func contextEvidence(files map[string][]byte) []RecipeEvidence {
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	evidence := make([]RecipeEvidence, 0, len(paths))
	for _, name := range paths {
		hash := sha256.Sum256(files[name])
		evidence = append(evidence, RecipeEvidence{Source: "github", Path: name, Digest: "sha256:" + hex.EncodeToString(hash[:]), Detail: "pinned repository file"})
	}
	return evidence
}

func resolveLocalRecipe(files map[string][]byte, source ArtifactSource) (LaunchRecipe, ConnectionRecipe, []string, error) {
	var launch LaunchRecipe
	var connection ConnectionRecipe
	var warnings []string
	if data, name := findFile(files, "server.json"); data != nil {
		candidate, fields, ok, err := parseServerManifest(data, name, source)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if ok {
			launch, connection = candidate, mergeConnection(connection, fields)
		}
	}
	if data, name := findFile(files, "manifest.json"); data != nil {
		candidate, fields, err := parseMCPBManifest(data, name)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if hasLaunch(candidate) {
			launch = mergeLaunch(launch, candidate, &warnings)
		}
		connection = mergeConnection(connection, fields)
	}
	for name, data := range files {
		if testFixturePath(name) {
			continue
		}
		base := strings.ToLower(path.Base(name))
		switch {
		case strings.HasPrefix(base, "dockerfile"):
			candidate, fields, fileWarnings, err := parseDockerfile(data, name)
			if err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			launch = mergeLaunch(launch, candidate, &warnings)
			connection = mergeConnection(connection, fields)
			warnings = append(warnings, fileWarnings...)
		case base == "compose.yaml" || base == "compose.yml" || strings.HasPrefix(base, "docker-compose."):
			candidate, fields, fileWarnings, err := parseCompose(data, name)
			if err != nil {
				warnings = append(warnings, "Compose recipe rejected: "+err.Error())
				continue
			}
			launch = mergeLaunch(launch, candidate, &warnings)
			connection = mergeConnection(connection, fields)
			warnings = append(warnings, fileWarnings...)
		case strings.HasPrefix(base, "readme"):
			connection = mergeConnection(connection, parseDocumentationCredentials(data, name))
		case base == ".mcp.json":
			candidate, fields, err := parseMCPJSON(data, name)
			if err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			launch = mergeLaunch(launch, candidate, &warnings)
			connection = mergeConnection(connection, fields)
		case base == ".env.example":
			connection = mergeConnection(connection, parseExampleEnv(data, name))
		}
	}
	if source.Subfolder != "" {
		warnings = append(warnings, "subfolder is pinned; generated build fallback must preserve that working directory")
	}
	return launch, normalizeConnection(connection), warnings, nil
}
