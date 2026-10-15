package api

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

type RecipeResolver struct {
	Catalogs        []RecipeCatalog
	MaxContextBytes int64
}

func (r RecipeResolver) Resolve(ctx context.Context, source ArtifactSource, contextBytes []byte) (RecipeResolution, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return RecipeResolution{}, err
	}
	Max := r.MaxContextBytes
	if Max == 0 {
		Max = 64 << 20
	}
	files, err := ReadRecipeContext(contextBytes, Max)
	if err != nil {
		return RecipeResolution{}, err
	}
	scopedFiles, err := ScopeRecipeContext(files, source.Subfolder)
	if err != nil {
		return RecipeResolution{}, err
	}
	lookup, err := LookupRecipe(source)
	if err != nil {
		return RecipeResolution{}, err
	}
	resolution := RecipeResolution{Source: source, State: "draft"}
	resolution.Evidence = contextEvidence(scopedFiles)

	localLaunch, localConnection, warnings, err := ResolveLocalRecipe(scopedFiles, source)
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

	candidates := LookupCatalogs(ctx, r.Catalogs, lookup)
	for _, candidate := range candidates {
		if !CandidateMatches(candidate, source) {
			resolution.Warnings = append(resolution.Warnings, "catalog candidate ignored: repository or commit did not match the pinned source")
			continue
		}
		resolution.Evidence = append(resolution.Evidence, candidate.Evidence...)
		resolution.Evidence = append(resolution.Evidence, candidate.Launch.Evidence...)
		if candidate.Launch.Artifact != "" || candidate.Launch.Endpoint != "" || len(candidate.Launch.Entrypoint) > 0 {
			if !SameLaunch(resolution.Launch, candidate.Launch) && HasLaunch(resolution.Launch) {
				resolution.State = "review"
				resolution.Warnings = append(resolution.Warnings, "matching catalogs returned conflicting launch recipes")
			} else if !HasLaunch(resolution.Launch) || resolution.State != "review" {
				resolution.Launch = candidate.Launch
			}
		}
		resolution.Connection = mergeConnection(resolution.Connection, candidate.Connection)
	}

	resolution.Connection = normalizeConnection(resolution.Connection)
	if err := resolution.Connection.Validate(); err != nil {
		return RecipeResolution{}, err
	}
	if !HasLaunch(resolution.Launch) {
		resolution.Warnings = append(resolution.Warnings, "no complete launch recipe was proven; existing language fallback remains required")
	}
	for _, warning := range resolution.Warnings {
		if strings.Contains(warning, "conflict") || strings.Contains(warning, "disagree") || strings.Contains(warning, "unsafe") {
			resolution.State = "review"
		}
	}
	if resolution.State != "review" && LaunchReady(resolution.Launch) {
		resolution.State = "ready"
	}
	slices.Sort(resolution.Warnings)
	return resolution, nil
}

func LookupCatalogs(ctx context.Context, catalogs []RecipeCatalog, lookup RecipeLookup) []RecipeCandidate {
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

func ReadRecipeContext(data []byte, maxBytes int64) (map[string][]byte, error) {
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

func ResolveLocalRecipe(files map[string][]byte, source ArtifactSource) (LaunchRecipe, ConnectionRecipe, []string, error) {
	var launch LaunchRecipe
	var connection ConnectionRecipe
	var warnings []string
	if data, name := findFile(files, "server.json"); data != nil {
		candidate, fields, ok, err := ParseServerManifest(data, name, source)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if ok {
			launch, connection = candidate, mergeConnection(connection, fields)
		}
	}
	if data, name := findFile(files, "manifest.json"); data != nil {
		candidate, fields, err := ParseMCPBManifest(data, name)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if HasLaunch(candidate) {
			launch = MergeLaunch(launch, candidate, &warnings)
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
			launch = MergeLaunch(launch, candidate, &warnings)
			connection = mergeConnection(connection, fields)
			warnings = append(warnings, fileWarnings...)
		case base == "compose.yaml" || base == "compose.yml" || strings.HasPrefix(base, "docker-compose."):
			candidate, fields, fileWarnings, err := parseCompose(data, name)
			if err != nil {
				warnings = append(warnings, "Compose recipe rejected: "+err.Error())
				continue
			}
			launch = MergeLaunch(launch, candidate, &warnings)
			connection = mergeConnection(connection, fields)
			warnings = append(warnings, fileWarnings...)
		case strings.HasPrefix(base, "readme"):
			connection = mergeConnection(connection, ParseDocumentationCredentials(data, name))
		case base == ".mcp.json":
			candidate, fields, err := ParseMCPJSON(data, name)
			if err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			launch = MergeLaunch(launch, candidate, &warnings)
			connection = mergeConnection(connection, fields)
		case base == ".env.example":
			connection = mergeConnection(connection, ParseExampleEnv(data, name))
		}
	}
	if source.Subfolder != "" {
		warnings = append(warnings, "subfolder is pinned; generated build fallback must preserve that working directory")
	}
	return launch, normalizeConnection(connection), warnings, nil
}
