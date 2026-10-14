package toolhub

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"path"
	"slices"
	"strings"
)

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
