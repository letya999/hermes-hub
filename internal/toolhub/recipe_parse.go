package toolhub

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
)

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
	placeholder := placeholderValue(value)
	if value != "" && !secret && !placeholder {
		return ConnectionField{}, false
	}
	// Heuristic sources cannot prove a non-secret knob is mandatory: an
	// uncommented NAME= in .env.example is conventionally optional. Only
	// secret-class names, explicit manifest isRequired flags, and placeholder
	// values are required — "JIRA_URL=https://your-company.example" is the
	// author's replace-me marker, not a usable default (mcp-atlassian lists
	// zero tools until it is filled).
	field := ConnectionField{Name: key, Type: connectionType(key), Required: secret || placeholder, Secret: secret, Delivery: "env", Target: key}
	if strings.Contains(strings.ToUpper(key), "CREDENTIALS") && strings.HasSuffix(strings.ToLower(value), ".json") {
		field.Type, field.Delivery = "json", "json"
	}
	return field, true
}

// placeholderValue reports whether an .env.example value is a replace-me
// marker rather than a usable default. "https://gitlab.com" ships working
// configuration; "https://your-company.atlassian.net" does not — the field
// must be collected from the owner like a credential.
func placeholderValue(value string) bool {
	value = strings.ToLower(strings.Trim(value, `"'`))
	for _, marker := range []string{"your-", "your_", "your.", "example.", "changeme", "change-me", "change_me", "placeholder", "xxxx", "xxx", "todo", "replace", "_here", "-here", "dummy", "insert-", "insert_", "<", ">"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
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
