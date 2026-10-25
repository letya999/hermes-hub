package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func cloneStringGroups(groups [][]string) [][]string {
	if groups == nil {
		return nil
	}
	out := make([][]string, len(groups))
	for i, group := range groups {
		out[i] = append([]string(nil), group...)
	}
	return out
}

func CloneMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func ParseGitHubSource(raw string) (ArtifactSource, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "https://gitlab.com/") {
		return parseGitLabSource(raw, true)
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ArtifactSource{}, fmt.Errorf("%w: public GitHub URL and exact commit SHA required", ErrInvalid)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && (parts[2] == "commit" || parts[2] == "tree") {
		source := ArtifactSource{Repository: "https://github.com/" + parts[0] + "/" + parts[1], CommitSHA: strings.ToLower(parts[3])}
		if parts[2] == "tree" {
			source.Subfolder = strings.Join(parts[4:], "/")
		} else if len(parts) != 4 {
			return ArtifactSource{}, fmt.Errorf("%w: commit URL cannot include a subfolder", ErrInvalid)
		}
		if _, err := source.ArchiveURL(); err != nil {
			return ArtifactSource{}, err
		}
		return source, nil
	}
	return ArtifactSource{}, fmt.Errorf("%w: public GitHub URL and exact commit SHA required", ErrInvalid)
}

func RejectEscalation(definition ToolDefinition, args map[string]any) error {
	allowedEffects := map[string]bool{}
	allowedTools := map[string]bool{}
	for _, tool := range definition.Tools {
		allowedEffects[string(tool.Effect)] = true
		allowedTools[tool.Name] = true
	}
	for _, effect := range ArgStrings(args, "effects") {
		if !allowedEffects[effect] {
			return fmt.Errorf("%w: effect escalation", ErrUnauthorized)
		}
	}
	for _, name := range ArgStrings(args, "tools") {
		if !allowedTools[name] {
			return fmt.Errorf("%w: effect escalation", ErrUnauthorized)
		}
	}
	if n := ArgInt(args, "cpu_millis"); n > 0 && n > definition.Execution.CPUMillis {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := ArgInt(args, "memory_mib"); n > 0 && n > definition.Execution.MemoryMiB {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := ArgInt(args, "timeout_seconds"); n > 0 && n > definition.Execution.TimeoutSeconds {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := ArgInt(args, "max_pids"); n > 0 && n > definition.Execution.MaxPIDs {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := ArgInt(args, "output_bytes"); n > 0 && n > definition.Execution.OutputBytes {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	return nil
}

func ToolNames(definition ToolDefinition) []string {
	names := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func EffectNames(definition ToolDefinition) []string {
	seen := map[string]bool{}
	effects := make([]string, 0)
	for _, tool := range definition.Tools {
		name := string(tool.Effect)
		if !seen[name] {
			seen[name] = true
			effects = append(effects, name)
		}
	}
	return effects
}

func ArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func ArgStrings(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func ArgInt(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	value, ok := args[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func RandomNonce() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(raw)
}
