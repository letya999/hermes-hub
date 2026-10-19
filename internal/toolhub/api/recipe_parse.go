package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func ParseMCPJSON(data []byte, name string) (LaunchRecipe, ConnectionRecipe, error) {
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
			if !ValidCommand(command) {
				return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: unsafe .mcp.json command", ErrInvalid)
			}
			candidate.Transport, candidate.Entrypoint = BoundedCLI, []string{command}
			candidate.Args = StringArray(server["args"])
		} else {
			continue
		}
		if len(document.Servers) == 1 {
			launch = candidate
		} else {
			launch = LaunchRecipe{}
		}
		connection = mergeConnection(connection, ConnectionFromMap(server, "env", "environment"))
	}
	return launch, connection, nil
}

func ParseExampleEnv(data []byte, name string) ConnectionRecipe {
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
	key := EnvKey(line)
	if key == "" || nonCredentialEnvName(key) {
		return ConnectionField{}, false
	}
	value := ""
	if _, rawValue, ok := strings.Cut(line, "="); ok {
		value = strings.TrimSpace(strings.SplitN(rawValue, "#", 2)[0])
	}
	secret := SecretName(key)
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
	field := ConnectionField{Name: key, Type: ConnectionType(key), Required: (secret || placeholder) && !deploySecretEnvName(key), Secret: secret, Delivery: "env", Target: key}
	if strings.Contains(strings.ToUpper(key), "CREDENTIALS") && strings.HasSuffix(strings.ToLower(value), ".json") {
		field.Type, field.Delivery = "json", "json"
	}
	return field, true
}

// deploySecretEnvName marks env names that guard the MCP server's own
// listener — HTTP bearer or session material. They are deployment secrets
// the operator mints at run time, not upstream provider credentials, so
// heuristic env-line sources must not force them into the required set the
// owner is asked to supply.
func deploySecretEnvName(name string) bool {
	if strings.EqualFold(name, "SESSION_SECRET") {
		return true
	}
	return strings.HasSuffix(name, "_AUTH_TOKEN") || strings.HasSuffix(name, "_AUTH_SECRET")
}

// DeploySecretRequired reports whether the definition still demands a
// deploy-secret-named credential. Stored definitions carrying one predate the
// demotion heuristic and must not be reused: the owner cannot satisfy them —
// a demoted form field materializes no env, so admission fails closed on
// every retry while a fresh review would re-parse with the demotion applied.
func DeploySecretRequired(definition ToolDefinition) bool {
	for _, input := range definition.Credentials {
		if input.Required && !input.PerRequest && deploySecretEnvName(input.Name) {
			return true
		}
	}
	return false
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

func ParseServerManifest(data []byte, name string, source ArtifactSource) (LaunchRecipe, ConnectionRecipe, bool, error) {
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
			candidate := ManifestPackageLaunch(pkg)
			if HasLaunch(candidate) {
				if HasLaunch(launch) && !SameLaunch(launch, candidate) {
					return LaunchRecipe{}, ConnectionRecipe{}, false, fmt.Errorf("%w: conflicting server manifest launch recipes", ErrInvalid)
				}
				launch = MergeLaunch(launch, candidate, nil)
			}
			connection = mergeConnection(connection, ConnectionFromMap(pkg, "environment", "env"))
			connection = mergeConnection(connection, ConnectionFromOfficialPackage(pkg))
			connection = mergeConnection(connection, ConnectionAlternatives(pkg))
		}
	}
	if remotes, ok := document["remotes"].([]any); ok && len(remotes) > 1 {
		// Two remote endpoints is a choice the manifest never made; picking
		// one silently would be guessing a different server than reviewed.
		return LaunchRecipe{}, ConnectionRecipe{}, false, fmt.Errorf("%w: server manifest advertises %d remotes", ErrAmbiguousEntrypoint, len(remotes))
	}
	if remotes, ok := document["remotes"].([]any); ok && len(remotes) == 1 {
		if remote, ok := remotes[0].(map[string]any); ok {
			if endpoint := firstString(remote, "url", "endpoint"); endpoint != "" {
				launch = MergeLaunch(launch, LaunchRecipe{Transport: RemoteMCP, Endpoint: endpoint, Network: []string{HostOfURL(endpoint)}, Evidence: []RecipeEvidence{{Source: "server.json", Path: name, Detail: "repository-matched remote"}}}, nil)
				connection = mergeConnection(connection, ConnectionFromMap(remote, "environment", "env"))
			}
		}
	}
	connection = mergeConnection(connection, ConnectionFromMap(document, "environment", "env"))
	connection = mergeConnection(connection, ConnectionAlternatives(document))
	return launch, connection, HasLaunch(launch), nil
}

func ManifestPackageLaunch(pkg map[string]any) LaunchRecipe {
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
		if runtime := firstString(pkg, "runtimeHint", "runtime"); runtime != "" && ValidCommand(runtime) {
			launch.Entrypoint = []string{runtime}
		}
		if launch.Entrypoint == nil {
			launch.Entrypoint = []string{firstString(pkg, "command", "entrypoint")}
		}
	}
	if launch.Artifact != "" {
		if imageName, imageDigest, ok := SplitImmutableImage(launch.Artifact); ok {
			launch.Artifact, launch.Digest = imageName, imageDigest
		}
	}
	if args := StringArray(pkg["args"]); len(args) > 0 {
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
	if command := firstString(pkg, "command", "entrypoint"); command != "" && ValidCommand(command) {
		launch.Entrypoint = []string{command}
	}
	if len(launch.Entrypoint) == 1 && launch.Entrypoint[0] == "" {
		launch.Entrypoint = nil
	}
	return launch
}

func ConnectionFromOfficialPackage(pkg map[string]any) ConnectionRecipe {
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
		if EnvKey(name) == "" {
			continue
		}
		field := ConnectionField{Name: name, Type: ConnectionType(name), Required: value["isRequired"] == true, Secret: value["isSecret"] == true || SecretName(name), Delivery: "env", Target: name}
		recipe.Fields = append(recipe.Fields, field)
	}
	return recipe
}

func ParseMCPBManifest(data []byte, name string) (LaunchRecipe, ConnectionRecipe, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: invalid MCPB manifest", ErrInvalid)
	}
	launch := LaunchRecipe{Transport: BoundedCLI, Version: firstString(document, "version", "manifest_version")}
	entrypoint := firstString(document, "entry_point", "entrypoint", "command")
	if entrypoint != "" {
		if !ValidCommand(entrypoint) || strings.ContainsAny(entrypoint, ";&|`$()") {
			return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: unsafe MCPB entrypoint", ErrInvalid)
		}
		launch.Entrypoint = []string{entrypoint}
	}
	launch.Args = StringArray(document["args"])
	launch.Evidence = []RecipeEvidence{{Source: "mcpb", Path: name, Detail: "MCPB manifest metadata"}}
	connection := mergeConnection(ConnectionFromMap(document, "environment", "env"), ConnectionFromMap(document, "config", "configSchema"))
	connection = mergeConnection(connection, ConnectionAlternatives(document))
	if server, ok := document["server"].(map[string]any); ok {
		var err error
		launch, connection, err = mcpbServerContract(document, server, launch, connection)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, err
		}
	}
	return launch, connection, nil
}

// mcpbServerContract reads the official MCPB server block: mcp_config argv
// becomes the entrypoint with ${__dirname} resolved to the container install
// dir, literal env lands on LaunchRecipe.Environment, and ${user_config.*}
// references in env project the user_config contract onto connection fields.
// A user_config reference inside args is refused — secrets never ride argv.
func mcpbServerContract(document, server map[string]any, launch LaunchRecipe, connection ConnectionRecipe) (LaunchRecipe, ConnectionRecipe, error) {
	userConfig, _ := document["user_config"].(map[string]any)
	config, _ := server["mcp_config"].(map[string]any)
	command := firstString(config, "command")
	if command == "" {
		command = firstString(server, "command")
	}
	args := StringArray(config["args"])
	if len(args) == 0 && len(config) == 0 {
		if ep := firstString(server, "entry_point", "entrypoint"); ep != "" {
			args = []string{ep}
		}
	}
	if command != "" {
		if !ValidCommand(command) || strings.ContainsAny(command, ";&|`$()") {
			return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: unsafe MCPB mcp_config command", ErrInvalid)
		}
		launch.Entrypoint = []string{command}
		launch.Args = nil
	}
	for i, arg := range args {
		if strings.Contains(arg, "${user_config.") {
			return LaunchRecipe{}, ConnectionRecipe{}, fmt.Errorf("%w: MCPB user_config references in args would put secrets on argv", ErrUnsupportedCredentialContract)
		}
		args[i] = strings.ReplaceAll(arg, "${__dirname}", "/app")
	}
	launch.Args = append(launch.Args, args...)
	if len(launch.Entrypoint) > 0 {
		launch.Transport = ContainerMCP
	}
	for envName, rawValue := range configMap(config["env"]) {
		value, _ := rawValue.(string)
		name := EnvKey(envName)
		if name == "" {
			continue
		}
		if key := mcpbUserConfigKey(value); key != "" {
			entry, _ := userConfig[key].(map[string]any)
			connection.Fields = append(connection.Fields, ConnectionField{
				Name: name, Type: ConnectionType(name), Delivery: "env", Target: name,
				Secret:   entry["sensitive"] == true || SecretName(name),
				Required: entry["required"] == true,
			})
			continue
		}
		if launch.Environment == nil {
			launch.Environment = map[string]string{}
		}
		launch.Environment[name] = strings.ReplaceAll(value, "${__dirname}", "/app")
	}
	return launch, connection, nil
}

func configMap(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	return m
}

func mcpbUserConfigKey(value string) string {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(value, "}"), "${user_config.")
	if !ok || !strings.HasSuffix(value, "}") {
		return ""
	}
	return rest
}

func ParseDocumentationCredentials(data []byte, name string) ConnectionRecipe {
	var recipe ConnectionRecipe
	for _, line := range strings.Split(string(data), "\n") {
		if index := strings.IndexByte(line, '='); index >= 0 {
			line = line[:index]
		}
		for _, word := range strings.FieldsFunc(line, func(r rune) bool {
			return !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
		}) {
			if CredentialPattern.MatchString(word) && (strings.Contains(word, "TOKEN") || strings.Contains(word, "KEY") || strings.Contains(word, "SECRET") || strings.Contains(word, "OAUTH") || strings.Contains(word, "CLIENT_ID")) {
				recipe.Fields = append(recipe.Fields, ConnectionField{Name: word, Type: ConnectionType(word), Secret: SecretName(word), Delivery: "env", Target: word, Alternative: "documentation"})
			}
		}
	}
	return recipe
}
