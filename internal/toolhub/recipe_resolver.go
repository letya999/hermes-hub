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
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
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

func parseMCPJSON(data []byte, name string) (LaunchRecipe, ConnectionRecipe, error) {
	var document struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: invalid .mcp.json: %v", ErrInvalid, err)
	}
	var launch LaunchRecipe
	var connection ConnectionRecipe
	for _, raw := range document.Servers {
		var server map[string]any
		if json.Unmarshal(raw, &server) != nil {
			continue
		}
		candidate := LaunchRecipe{Evidence: []RecipeEvidence{{Source: "mcp.json", Path: name, Detail: "metadata-only MCP server declaration"}}}
		if endpoint := firstString(server, "url", "endpoint"); endpoint != "" {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: unsafe .mcp.json endpoint", ErrInvalid)
			}
			candidate.Transport, candidate.Endpoint, candidate.Network = RemoteMCP, endpoint, []string{strings.ToLower(parsed.Hostname())}
		} else if command := firstString(server, "command", "runtime"); command != "" {
			if !validCommand(command) {
				return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: unsafe .mcp.json command", ErrInvalid)
			}
			candidate.Transport, candidate.Entrypoint = BoundedCLI, []string{command}
			candidate.Args = stringArray(server["args"])
		} else {
			continue
		}
		if len(document.Servers) == 1 {
			launch = candidate
		} else {
			launch = LaunchRecipe{}
		}
		connection = mergeConnection(connection, connectionFromMap(server, "env", "environment"))
	}
	return launch, connection, nil
}

func parseExampleEnv(data []byte, name string) ConnectionRecipe {
	var recipe ConnectionRecipe
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if field, ok := connectionFieldFromEnv(line); ok {
			field.Alternative = "env.example"
			recipe.Fields = append(recipe.Fields, field)
		}
	}
	if len(recipe.Fields) > 0 {
		recipe.Fields[0].Alternative = "env.example"
	}
	return normalizeConnection(recipe)
}

func connectionFieldFromEnv(line string) (ConnectionField, bool) {
	key := envKey(line)
	if key == "" || nonCredentialEnvName(key) {
		return ConnectionField{}, false
	}
	value := ""
	if _, rawValue, ok := strings.Cut(line, "="); ok {
		value = strings.TrimSpace(strings.SplitN(rawValue, "#", 2)[0])
	}
	secret := secretName(key)
	if value != "" && !secret {
		return ConnectionField{}, false
	}
	// Heuristic sources cannot prove a non-secret knob is mandatory: an
	// uncommented NAME= in .env.example is conventionally optional. Only
	// secret-class names and explicit manifest isRequired flags are required.
	field := ConnectionField{Name: key, Type: connectionType(key), Required: secret, Secret: secret, Delivery: "env", Target: key}
	if strings.Contains(strings.ToUpper(key), "CREDENTIALS") && strings.HasSuffix(strings.ToLower(value), ".json") {
		field.Type, field.Delivery = "json", "json"
	}
	return field, true
}

func findFile(files map[string][]byte, base string) ([]byte, string) {
	var names []string
	for name := range files {
		if strings.EqualFold(path.Base(name), base) {
			names = append(names, name)
		}
	}
	if len(names) != 1 {
		return nil, ""
	}
	return files[names[0]], names[0]
}

func parseServerManifest(data []byte, name string, source ArtifactSource) (LaunchRecipe, ConnectionRecipe, bool, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return LaunchRecipe{}, ConnectionRecipe{}, false, fmt.Errorf("%w: invalid MCP server manifest", ErrInvalid)
	}
	repository := firstString(document, "repository", "repositoryUrl", "repository_url")
	if repository == "" {
		if value, ok := document["repository"].(map[string]any); ok {
			repository = firstString(value, "url", "uri")
		}
	}
	if repository != "" && !sameRepository(repository, source.Repository) {
		return LaunchRecipe{}, ConnectionRecipe{}, false, nil
	}
	if value, ok := document["repository"].(map[string]any); ok {
		if subfolder := firstString(value, "subfolder", "path"); subfolder != "" && subfolder != source.Subfolder {
			return LaunchRecipe{}, ConnectionRecipe{}, false, nil
		}
	}
	var launch LaunchRecipe
	var connection ConnectionRecipe
	if packages, ok := document["packages"].([]any); ok {
		for _, raw := range packages {
			pkg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			candidate := manifestPackageLaunch(pkg)
			if hasLaunch(candidate) {
				if hasLaunch(launch) && !sameLaunch(launch, candidate) {
					return LaunchRecipe{}, ConnectionRecipe{}, false, fmt.Errorf("%w: conflicting server manifest launch recipes", ErrInvalid)
				}
				launch = mergeLaunch(launch, candidate, nil)
			}
			connection = mergeConnection(connection, connectionFromMap(pkg, "environment", "env"))
			connection = mergeConnection(connection, connectionFromOfficialPackage(pkg))
			connection = mergeConnection(connection, connectionAlternatives(pkg))
		}
	}
	if remotes, ok := document["remotes"].([]any); ok && len(remotes) == 1 {
		if remote, ok := remotes[0].(map[string]any); ok {
			if endpoint := firstString(remote, "url", "endpoint"); endpoint != "" {
				launch = mergeLaunch(launch, LaunchRecipe{Transport: RemoteMCP, Endpoint: endpoint, Network: []string{hostOfURL(endpoint)}, Evidence: []RecipeEvidence{{Source: "server.json", Path: name, Detail: "repository-matched remote"}}}, nil)
				connection = mergeConnection(connection, connectionFromMap(remote, "environment", "env"))
			}
		}
	}
	connection = mergeConnection(connection, connectionFromMap(document, "environment", "env"))
	connection = mergeConnection(connection, connectionAlternatives(document))
	return launch, connection, hasLaunch(launch), nil
}

func manifestPackageLaunch(pkg map[string]any) LaunchRecipe {
	image := firstString(pkg, "identifier", "image", "imageUri", "image_uri")
	digest := firstString(pkg, "digest", "imageDigest", "image_digest")
	if !strings.Contains(image, "@") && digest != "" {
		image += "@" + digest
	}
	launch := LaunchRecipe{Artifact: image, Version: firstString(pkg, "version"), Transport: ContainerMCP}
	transport := strings.ToLower(firstString(pkg, "transport", "transportType"))
	if transport == "" {
		if value, ok := pkg["transport"].(map[string]any); ok {
			transport = strings.ToLower(firstString(value, "type", "transportType"))
		}
	}
	if transport == "streamable-http" || transport == "sse" || transport == "http" {
		launch.Transport = RemoteMCP
		launch.Endpoint = firstString(pkg, "url", "endpoint")
		if value, ok := pkg["transport"].(map[string]any); ok && launch.Endpoint == "" {
			launch.Endpoint = firstString(value, "url", "endpoint")
		}
	} else if transport == "stdio" && strings.ToLower(firstString(pkg, "registryType")) != "oci" {
		launch.Transport = BoundedCLI
		if runtime := firstString(pkg, "runtimeHint", "runtime"); runtime != "" && validCommand(runtime) {
			launch.Entrypoint = []string{runtime}
		}
		if launch.Entrypoint == nil {
			launch.Entrypoint = []string{firstString(pkg, "command", "entrypoint")}
		}
	}
	if launch.Artifact != "" {
		if imageName, imageDigest, ok := splitImmutableImage(launch.Artifact); ok {
			launch.Artifact, launch.Digest = imageName, imageDigest
		}
	}
	if args := stringArray(pkg["args"]); len(args) > 0 {
		launch.Args = args
	}
	if runtimeArguments, ok := pkg["runtimeArguments"].([]any); ok {
		for _, raw := range runtimeArguments {
			if value, ok := raw.(map[string]any); ok {
				if arg := firstString(value, "value"); arg != "" && !strings.ContainsAny(arg, "\x00\r\n") {
					launch.Args = append(launch.Args, arg)
				}
			}
		}
	}
	if command := firstString(pkg, "command", "entrypoint"); command != "" && validCommand(command) {
		launch.Entrypoint = []string{command}
	}
	if len(launch.Entrypoint) == 1 && launch.Entrypoint[0] == "" {
		launch.Entrypoint = nil
	}
	return launch
}

func connectionFromOfficialPackage(pkg map[string]any) ConnectionRecipe {
	var recipe ConnectionRecipe
	values, ok := pkg["environmentVariables"].([]any)
	if !ok {
		return recipe
	}
	for _, raw := range values {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := firstString(value, "name")
		if envKey(name) == "" {
			continue
		}
		field := ConnectionField{Name: name, Type: connectionType(name), Required: value["isRequired"] == true, Secret: value["isSecret"] == true || secretName(name), Delivery: "env", Target: name}
		recipe.Fields = append(recipe.Fields, field)
	}
	return recipe
}

func parseMCPBManifest(data []byte, name string) (LaunchRecipe, ConnectionRecipe, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: invalid MCPB manifest", ErrInvalid)
	}
	launch := LaunchRecipe{Transport: BoundedCLI, Version: firstString(document, "version", "manifest_version")}
	entrypoint := firstString(document, "entry_point", "entrypoint", "command")
	if entrypoint != "" {
		if !validCommand(entrypoint) || strings.ContainsAny(entrypoint, ";&|`$()") {
			return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: unsafe MCPB entrypoint", ErrInvalid)
		}
		launch.Entrypoint = []string{entrypoint}
	}
	launch.Args = stringArray(document["args"])
	launch.Evidence = []RecipeEvidence{{Source: "mcpb", Path: name, Detail: "MCPB manifest metadata"}}
	connection := mergeConnection(connectionFromMap(document, "environment", "env"), connectionFromMap(document, "config", "configSchema"))
	return launch, mergeConnection(connection, connectionAlternatives(document)), nil
}

func parseDockerfile(data []byte, name string) (LaunchRecipe, ConnectionRecipe, []string, error) {
	launch := LaunchRecipe{Transport: ContainerMCP}
	var connection ConnectionRecipe
	var warnings []string
	for _, line := range dockerfileInstructions(data) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.EqualFold(fields[0], "RUN") && unsafeRunOptions(fields[1:]) {
			warnings = append(warnings, "unsafe Dockerfile directive in "+name+"; restricted generated build fallback required")
			return LaunchRecipe{}, ConnectionRecipe{}, warnings, nil
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			base := fields[1]
			if !strings.Contains(base, "@sha256:") {
				warnings = append(warnings, "Dockerfile base image is mutable; resolver requires digest resolution before build")
			} else {
				launch.Evidence = append(launch.Evidence, RecipeEvidence{Source: "dockerfile", Path: name, Detail: "digest-pinned base image"})
			}
		case "ENTRYPOINT", "CMD":
			value := strings.TrimSpace(line[len(fields[0]):])
			argv := stringArray(json.RawMessage(value))
			if len(argv) == 0 {
				warnings = append(warnings, "shell-form Dockerfile command was not accepted as an entrypoint")
				continue
			}
			if strings.ToUpper(fields[0]) == "ENTRYPOINT" || len(launch.Entrypoint) == 0 {
				launch.Entrypoint = argv
			}
		case "ENV":
			if name := envKey(fields[1]); name != "" {
				connection = mergeConnection(connection, ConnectionRecipe{Fields: []ConnectionField{{Name: name, Type: "string", Delivery: "env", Target: name}}})
			}
		case "VOLUME":
			targets := stringArray(json.RawMessage(strings.TrimSpace(line[len(fields[0]):])))
			if len(targets) == 0 {
				targets = fields[1:]
			}
			for _, target := range targets {
				if path.Base(target) == "docker.sock" {
					warnings = append(warnings, "unsafe Dockerfile directive in "+name+"; restricted generated build fallback required")
					return LaunchRecipe{}, ConnectionRecipe{}, warnings, nil
				}
				if strings.HasPrefix(target, "/") {
					launch.Mounts = append(launch.Mounts, Mount{Source: "connection-state", Target: target})
				}
			}
		case "HEALTHCHECK":
			if len(fields) >= 3 && strings.EqualFold(fields[1], "CMD") {
				check := stringArray(json.RawMessage(strings.TrimSpace(line[strings.Index(strings.ToUpper(line), "CMD")+3:])))
				if len(check) > 0 {
					launch.Health = HealthProbe{Kind: "exec", Value: strings.Join(check, " "), TimeoutSeconds: 5}
				}
			}
		}
	}
	if len(launch.Entrypoint) > 0 {
		launch.Evidence = append(launch.Evidence, RecipeEvidence{Source: "dockerfile", Path: name, Detail: "literal exec-form entrypoint"})
	}
	return launch, connection, warnings, nil
}

// dockerfileInstructions folds line continuations and drops comments and blank
// lines so RUN options are parsed as instruction tokens, not substrings.
func dockerfileInstructions(data []byte) []string {
	var lines []string
	var current strings.Builder
	flush := func() {
		if line := strings.TrimSpace(current.String()); line != "" {
			lines = append(lines, line)
		}
		current.Reset()
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		continued := strings.HasSuffix(line, "\\")
		if continued {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		}
		if current.Len() > 0 && line != "" {
			current.WriteString(" ")
		}
		current.WriteString(line)
		if !continued {
			flush()
		}
	}
	flush()
	return lines
}

// unsafeRunOptions inspects the leading --option tokens of a RUN instruction.
// Only cache mounts, default/none networks and sandbox security are metadata;
// every other option is unsafe and forces the generated build fallback.
func unsafeRunOptions(tokens []string) bool {
	for _, token := range tokens {
		if !strings.HasPrefix(token, "--") {
			return false
		}
		lower := strings.ToLower(token)
		switch {
		case strings.HasPrefix(lower, "--mount="):
			if !cacheOnlyMount(strings.TrimPrefix(lower, "--mount=")) {
				return true
			}
		case strings.HasPrefix(lower, "--network="):
			if value := strings.TrimPrefix(lower, "--network="); value != "default" && value != "none" {
				return true
			}
		case strings.HasPrefix(lower, "--security="):
			if strings.TrimPrefix(lower, "--security=") != "sandbox" {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func cacheOnlyMount(spec string) bool {
	mountType := ""
	for _, part := range strings.Split(spec, ",") {
		if key, value, ok := strings.Cut(part, "="); ok && key == "type" {
			mountType = value
		}
	}
	return mountType == "cache"
}

type composeService struct {
	name        string
	image       string
	build       bool
	unsafe      bool
	command     []string
	entrypoint  []string
	environment []ConnectionField
	volumes     []string
	secrets     []string
	configs     []string
	health      string
}

func parseCompose(data []byte, name string) (LaunchRecipe, ConnectionRecipe, []string, error) {
	var document map[string]any
	if yaml.Unmarshal(data, &document) != nil {
		return LaunchRecipe{}, ConnectionRecipe{}, nil, fmt.Errorf("%w: invalid Compose YAML", ErrInvalid)
	}
	return parseComposeDocument(document, name)
}

func parseComposeDocument(document map[string]any, name string) (LaunchRecipe, ConnectionRecipe, []string, error) {
	servicesValue, ok := document["services"]
	servicesMap := anyStringMap(servicesValue)
	if !ok || len(servicesMap) == 0 {
		return LaunchRecipe{}, ConnectionRecipe{}, []string{"Compose file has no services declaration"}, nil
	}
	services := map[string]*composeService{}
	for serviceName, raw := range servicesMap {
		value := anyStringMap(raw)
		service := &composeService{name: serviceName}
		service.image = firstString(value, "image")
		if _, exists := value["build"]; exists {
			service.build = true
		}
		service.command = composeArgs(value["command"])
		service.entrypoint = composeArgs(value["entrypoint"])
		service.environment = composeEnvironmentFields(value["environment"])
		service.secrets = composeNames(value["secrets"])
		service.configs = composeNames(value["configs"])
		service.volumes = composeArgs(value["volumes"])
		service.health = composeHealth(value["healthcheck"])
		for _, key := range []string{"privileged", "network_mode", "pid", "ipc"} {
			if flag, ok := value[key]; ok {
				text := strings.ToLower(strings.TrimSpace(fmt.Sprint(flag)))
				if (key == "privileged" && text == "true") || (key != "privileged" && text == "host") {
					service.unsafe = true
				}
			}
		}
		services[serviceName] = service
	}
	return composeLaunch(services, name)
}

func composeLaunch(services map[string]*composeService, name string) (LaunchRecipe, ConnectionRecipe, []string, error) {
	var selected *composeService
	for _, service := range services {
		if strings.Contains(strings.ToLower(service.name+" "+service.image), "mcp") {
			if selected != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, fmt.Errorf("%w: Compose has multiple MCP-looking services", ErrInvalid)
			}
			selected = service
		}
	}
	if selected == nil && len(services) == 1 {
		for _, service := range services {
			selected = service
		}
	}
	if selected == nil {
		return LaunchRecipe{}, ConnectionRecipe{}, []string{"Compose MCP service is ambiguous; sidecars require explicit review"}, nil
	}
	if selected.unsafe {
		return LaunchRecipe{}, ConnectionRecipe{}, nil, fmt.Errorf("%w: Compose privileged/host namespace is not allowed", ErrInvalid)
	}
	warnings := []string{}
	launch := LaunchRecipe{Transport: ContainerMCP, Artifact: selected.image, Entrypoint: selected.entrypoint, Args: selected.command, Evidence: []RecipeEvidence{{Source: "compose", Path: name, Detail: "selected MCP service"}}}
	if image, digest, ok := splitImmutableImage(selected.image); ok {
		launch.Artifact, launch.Digest = image, digest
	} else if selected.image != "" && !selected.build {
		return LaunchRecipe{}, ConnectionRecipe{}, nil, fmt.Errorf("%w: Compose image must be immutable or use reviewed build fallback", ErrInvalid)
	}
	if selected.health != "" {
		if strings.Contains(strings.ToLower(selected.health), "cmd-shell") {
			warnings = append(warnings, "Compose shell healthcheck was ignored; it will not be executed")
		} else {
			parts := strings.Fields(selected.health)
			if len(parts) > 1 && strings.EqualFold(parts[0], "cmd") && validCommand(parts[1]) {
				launch.Health = HealthProbe{Kind: "exec", Value: strings.Join(parts[1:], " "), TimeoutSeconds: 5}
			}
		}
	}
	for _, volume := range selected.volumes {
		parts := strings.Split(volume, ":")
		if len(parts) < 2 || strings.HasPrefix(parts[0], "/") || strings.HasPrefix(parts[0], ".") || strings.Contains(parts[0], "..") || !strings.HasPrefix(parts[1], "/") {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, fmt.Errorf("%w: Compose host mount is not allowed", ErrInvalid)
		}
		launch.Mounts = append(launch.Mounts, Mount{Source: parts[0], Target: parts[1], ReadOnly: len(parts) > 2 && strings.Contains(parts[2], "ro")})
	}
	connection := ConnectionRecipe{}
	connection.Fields = append(connection.Fields, selected.environment...)
	for _, secret := range append(selected.secrets, selected.configs...) {
		if key := envKey(secret); key != "" {
			connection.Fields = append(connection.Fields, ConnectionField{Name: key, Type: "secret", Secret: true, Delivery: "file", Target: key})
		}
	}
	if selected.build && selected.image != "" && launch.Digest == "" {
		warnings = append(warnings, "Compose build requires restricted BuildKit and digest resolution before launch")
	}
	if len(services) > 1 {
		for serviceName := range services {
			if serviceName != selected.name {
				launch.Sidecars = append(launch.Sidecars, serviceName)
			}
		}
		slices.Sort(launch.Sidecars)
		warnings = append(warnings, "Compose sidecars were recorded but will not be started automatically")
	}
	return launch, connection, warnings, nil
}

func anyStringMap(value any) map[string]any {
	result := map[string]any{}
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		for key, value := range typed {
			if text, ok := key.(string); ok {
				result[text] = value
			}
		}
	}
	return result
}

func composeArgs(value any) []string {
	if _, ok := value.(string); ok {
		return nil
	}
	return stringArray(value)
}

func composeNames(value any) []string {
	if values := composeArgs(value); len(values) > 0 {
		return values
	}
	for key := range anyStringMap(value) {
		if envKey(key) != "" {
			return append([]string(nil), key)
		}
	}
	return nil
}

func composeEnvironmentFields(value any) []ConnectionField {
	var fields []ConnectionField
	if values := composeArgs(value); len(values) > 0 {
		for _, item := range values {
			if field, ok := connectionFieldFromEnv(item); ok {
				fields = append(fields, field)
			}
		}
		return fields
	}
	for key, value := range anyStringMap(value) {
		line := key
		if value != nil {
			line += "=" + fmt.Sprint(value)
		}
		if field, ok := connectionFieldFromEnv(line); ok {
			fields = append(fields, field)
		}
	}
	return fields
}

func composeHealth(value any) string {
	values := composeArgs(anyStringMap(value)["test"])
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, " ")
}

func parseDocumentationCredentials(data []byte, name string) ConnectionRecipe {
	var recipe ConnectionRecipe
	for _, line := range strings.Split(string(data), "\n") {
		if index := strings.IndexByte(line, '='); index >= 0 {
			line = line[:index]
		}
		for _, word := range strings.FieldsFunc(line, func(r rune) bool {
			return !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
		}) {
			if credentialPattern.MatchString(word) && (strings.Contains(word, "TOKEN") || strings.Contains(word, "KEY") || strings.Contains(word, "SECRET") || strings.Contains(word, "OAUTH") || strings.Contains(word, "CLIENT_ID")) {
				recipe.Fields = append(recipe.Fields, ConnectionField{Name: word, Type: connectionType(word), Secret: secretName(word), Delivery: "env", Target: word, Alternative: "documentation"})
			}
		}
	}
	return recipe
}

func connectionFromMap(document map[string]any, keys ...string) ConnectionRecipe {
	var recipe ConnectionRecipe
	for _, key := range keys {
		if values, ok := document[key].(map[string]any); ok {
			for name, raw := range values {
				if envKey(name) != "" {
					field := ConnectionField{Name: name, Type: connectionType(name), Required: true, Secret: secretName(name), Delivery: "env", Target: name}
					if descriptor, ok := raw.(map[string]any); ok {
						if delivery := firstString(descriptor, "delivery", "format"); delivery != "" {
							field.Delivery = delivery
						}
						if target := firstString(descriptor, "target", "header", "path"); target != "" {
							field.Target = target
						}
						if kind := firstString(descriptor, "type"); kind != "" {
							field.Type = kind
						}
						if required, ok := descriptor["required"].(bool); ok {
							field.Required = required
						}
						if secret, ok := descriptor["secret"].(bool); ok {
							field.Secret = secret
						}
					}
					recipe.Fields = append(recipe.Fields, field)
				}
			}
		}
	}
	return recipe
}

func connectionAlternatives(document map[string]any) ConnectionRecipe {
	var recipe ConnectionRecipe
	for _, key := range []string{"auth", "authentication", "oauth"} {
		options, ok := document[key].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range options {
			option, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			alternative := ConnectionAlternative{Name: name, URL: firstString(option, "url", "authorization_url", "authorizationUrl")}
			if env, ok := option["env"].(map[string]any); ok {
				for fieldName := range env {
					if envKey(fieldName) != "" {
						alternative.Fields = append(alternative.Fields, ConnectionField{Name: fieldName, Type: "oauth", Required: true, Secret: true, Delivery: "oauth", Target: fieldName})
					}
				}
			}
			if alternative.Name != "" && (alternative.URL != "" || len(alternative.Fields) > 0) {
				recipe.Alternatives = append(recipe.Alternatives, alternative)
			}
		}
	}
	return recipe
}

func normalizeConnection(recipe ConnectionRecipe) ConnectionRecipe {
	seen := map[string]bool{}
	fields := recipe.Fields[:0]
	for _, field := range recipe.Fields {
		if !credentialPattern.MatchString(field.Name) || seen[field.Name] {
			continue
		}
		if field.Type == "" {
			field.Type = connectionType(field.Name)
		}
		if field.Delivery == "" {
			field.Delivery = "env"
		}
		if field.Target == "" {
			field.Target = field.Name
		}
		seen[field.Name] = true
		fields = append(fields, field)
	}
	recipe.Fields = fields
	slices.SortFunc(recipe.Fields, func(a, b ConnectionField) int { return strings.Compare(a.Name, b.Name) })
	return recipe
}

func mergeConnection(left, right ConnectionRecipe) ConnectionRecipe {
	left.Fields = append(left.Fields, right.Fields...)
	left.Alternatives = append(left.Alternatives, right.Alternatives...)
	return normalizeConnection(left)
}

func mergeLaunch(left, right LaunchRecipe, warnings *[]string) LaunchRecipe {
	if !hasLaunch(left) {
		return right
	}
	if !hasLaunch(right) {
		return left
	}
	if sameLaunch(left, right) {
		return left
	}
	if warnings != nil {
		*warnings = append(*warnings, "repository declarations disagree about the launch recipe")
	}
	return left
}

func hasLaunch(launch LaunchRecipe) bool {
	return launch.Artifact != "" || launch.Endpoint != "" || len(launch.Entrypoint) > 0
}

func launchReady(launch LaunchRecipe) bool {
	if launch.Endpoint != "" {
		return launch.Transport == RemoteMCP && hostOfURL(launch.Endpoint) != ""
	}
	return launch.Artifact != "" && launch.Digest != "" && len(launch.Entrypoint) > 0 && launch.Transport == ContainerMCP
}

func sameLaunch(left, right LaunchRecipe) bool {
	return left.Artifact == right.Artifact && left.Digest == right.Digest && left.Endpoint == right.Endpoint && slices.Equal(left.Entrypoint, right.Entrypoint) && slices.Equal(left.Args, right.Args)
}

func mergeConnectionForDefinition(recipe ConnectionRecipe, definition *ToolDefinition) {
	if definition == nil {
		return
	}
	definition.Credentials = nil
	definition.Credentials = append(definition.Credentials, recipe.CredentialInputs()...)
}

func splitImmutableImage(value string) (string, string, bool) {
	name, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
	return name, digest, ok && name != "" && digestPattern.MatchString(digest) && !strings.ContainsAny(name, " \t\r\n")
}

func stringArray(value any) []string {
	var values []string
	switch typed := value.(type) {
	case json.RawMessage:
		if json.Unmarshal(typed, &values) != nil {
			return nil
		}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	case []string:
		values = append(values, typed...)
	}
	for _, value := range values {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil
		}
	}
	return values
}

func firstString(document map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := document[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func envKey(value string) string {
	value = strings.TrimSpace(strings.SplitN(value, "=", 2)[0])
	if !credentialPattern.MatchString(value) {
		return ""
	}
	return value
}

// nonCredentialEnvName excludes names that can never be user-supplied
// connection credentials: proxy variables are injected by the workload
// runtime itself, and TEST_*/​*_TEST fixtures belong to upstream test suites.
func nonCredentialEnvName(name string) bool {
	switch name {
	case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY":
		return true
	}
	return strings.HasPrefix(name, "TEST_") || strings.HasSuffix(name, "_TEST") || strings.Contains(name, "_TEST_")
}

func connectionType(name string) string {
	name = strings.ToUpper(name)
	if strings.Contains(name, "OAUTH") || strings.Contains(name, "CLIENT_ID") || strings.Contains(name, "CLIENT_SECRET") {
		return "oauth"
	}
	if secretName(name) {
		return "secret"
	}
	return "string"
}

func secretName(name string) bool {
	name = strings.ToUpper(name)
	return strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "CREDENTIAL") || strings.HasSuffix(name, "_KEY") || strings.HasSuffix(name, "KEY") || strings.Contains(name, "PRIVATE")
}

func schemaFieldName(value string) string {
	value = strings.TrimSpace(value)
	if name := envKey(value); name != "" {
		return name
	}
	runes := []rune(value)
	var builder strings.Builder
	for index, current := range runes {
		if index > 0 && current >= 'A' && current <= 'Z' {
			previous := runes[index-1]
			nextLower := index+1 < len(runes) && runes[index+1] >= 'a' && runes[index+1] <= 'z'
			if previous >= 'a' && previous <= 'z' || previous >= '0' && previous <= '9' || previous >= 'A' && previous <= 'Z' && nextLower {
				builder.WriteByte('_')
			}
		}
		if current >= 'a' && current <= 'z' {
			builder.WriteByte(byte(current - 'a' + 'A'))
		} else if current >= 'A' && current <= 'Z' || current >= '0' && current <= '9' {
			builder.WriteRune(current)
		} else {
			builder.WriteByte('_')
		}
	}
	return envKey(builder.String())
}
