package api

import (
	"encoding/json"
	"slices"
	"strings"
)

func ConnectionFromMap(document map[string]any, keys ...string) ConnectionRecipe {
	var recipe ConnectionRecipe
	for _, key := range keys {
		if values, ok := document[key].(map[string]any); ok {
			for name, raw := range values {
				if EnvKey(name) != "" {
					field := ConnectionField{Name: name, Type: ConnectionType(name), Required: true, Secret: SecretName(name), Delivery: "env", Target: name}
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

func ConnectionAlternatives(document map[string]any) ConnectionRecipe {
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
					if EnvKey(fieldName) != "" {
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
		if !CredentialPattern.MatchString(field.Name) || seen[field.Name] {
			continue
		}
		if field.Type == "" {
			field.Type = ConnectionType(field.Name)
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

func MergeLaunch(left, right LaunchRecipe, warnings *[]string) LaunchRecipe {
	if !HasLaunch(left) {
		return right
	}
	if !HasLaunch(right) {
		return left
	}
	if SameLaunch(left, right) {
		return left
	}
	if warnings != nil {
		*warnings = append(*warnings, "repository declarations disagree about the launch recipe")
	}
	return left
}

func HasLaunch(launch LaunchRecipe) bool {
	return launch.Artifact != "" || launch.Endpoint != "" || len(launch.Entrypoint) > 0
}

func LaunchReady(launch LaunchRecipe) bool {
	if launch.Endpoint != "" {
		return launch.Transport == RemoteMCP && HostOfURL(launch.Endpoint) != ""
	}
	return launch.Artifact != "" && launch.Digest != "" && len(launch.Entrypoint) > 0 && launch.Transport == ContainerMCP
}

func SameLaunch(left, right LaunchRecipe) bool {
	return left.Artifact == right.Artifact && left.Digest == right.Digest && left.Endpoint == right.Endpoint && slices.Equal(left.Entrypoint, right.Entrypoint) && slices.Equal(left.Args, right.Args)
}

func MergeConnectionForDefinition(recipe ConnectionRecipe, definition *ToolDefinition) {
	if definition == nil {
		return
	}
	definition.Credentials = nil
	definition.Credentials = append(definition.Credentials, recipe.CredentialInputs()...)
}

func SplitImmutableImage(value string) (string, string, bool) {
	name, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
	return name, digest, ok && name != "" && DigestPattern.MatchString(digest) && !strings.ContainsAny(name, " \t\r\n")
}

func StringArray(value any) []string {
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

func EnvKey(value string) string {
	value = strings.TrimSpace(strings.SplitN(value, "=", 2)[0])
	if !CredentialPattern.MatchString(value) {
		return ""
	}
	return value
}

// testFixturePath reports recipe files that live under upstream test or
// fixture directories. Their env vars configure test infrastructure (e.g. a
// throwaway Atlassian container's DB password), not the MCP server itself, so
// they must not become connection inputs.
func testFixturePath(name string) bool {
	parts := strings.Split(strings.ToLower(name), "/")
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "test", "tests", "testdata", "e2e", "fixture", "fixtures", "__tests__":
			return true
		}
	}
	return false
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

func ConnectionType(name string) string {
	name = strings.ToUpper(name)
	if strings.Contains(name, "OAUTH") || strings.Contains(name, "CLIENT_ID") || strings.Contains(name, "CLIENT_SECRET") {
		return "oauth"
	}
	if SecretName(name) {
		return "secret"
	}
	return "string"
}

func SecretName(name string) bool {
	name = strings.ToUpper(name)
	return strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "CREDENTIAL") || strings.HasSuffix(name, "_KEY") || strings.HasSuffix(name, "KEY") || strings.Contains(name, "PRIVATE")
}

func schemaFieldName(value string) string {
	value = strings.TrimSpace(value)
	if name := EnvKey(value); name != "" {
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
	return EnvKey(builder.String())
}
