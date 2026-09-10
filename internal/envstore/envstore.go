// Package envstore persists the user-owned environment overlay for a runtime.
package envstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	FileName  = "self-env.json"
	maxBytes  = 2 * 1024 * 1024
	maxValues = 128
)

var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

var blocked = map[string]bool{
	"BASH_ENV": true, "COMSPEC": true, "DISPLAY": true, "DOCKER_HOST": true,
	"ENV": true, "GODEBUG": true, "GOMODCACHE": true, "GOCACHE": true,
	"HOME": true, "LD_LIBRARY_PATH": true, "LD_PRELOAD": true, "LOGNAME": true,
	"OLDPWD": true, "PATH": true, "PATHEXT": true, "PWD": true, "PYTHONHOME": true,
	"PYTHONPATH": true, "SHELL": true, "SYSTEMROOT": true, "USER": true, "WINDIR": true,
}

func Parse(text string) (map[string]string, error) {
	entries := map[string]string{}
	lines := strings.Split(text, "\n")
	for lineNumber := 0; lineNumber < len(lines); lineNumber++ {
		line := lines[lineNumber]
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if ok {
			if !keyPattern.MatchString(key) {
				return nil, fmt.Errorf("invalid env entry at line %d", lineNumber+1)
			}
		} else if key, value, ok = strings.Cut(line, ":"); ok && keyPattern.MatchString(strings.TrimSpace(key)) {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if value == "" {
				value, lineNumber, ok = followingValue(lines, lineNumber)
				if !ok {
					return nil, fmt.Errorf("missing value for %s at line %d", key, lineNumber+1)
				}
			}
		} else if isBareEnvKey(line) {
			value, lineNumber, ok = followingValue(lines, lineNumber)
			if !ok {
				return nil, fmt.Errorf("missing value for %s at line %d", line, lineNumber+1)
			}
			key = line
		} else {
			// Allow a short human preamble such as "Для Atlassian" around
			// explicit connector entries, but reject malformed assignments.
			if strings.Contains(line, "=") {
				return nil, fmt.Errorf("invalid env entry at line %d", lineNumber+1)
			}
			continue
		}
		if _, exists := entries[key]; exists {
			return nil, fmt.Errorf("duplicate env key %s", key)
		}
		entries[key] = strings.TrimSpace(value)
	}
	return entries, nil
}

// LooksLikeEnv marks messages containing connector-style entries as sensitive
// so the Telegram gateway can delete them after processing.
func LooksLikeEnv(text string) bool {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, ok := strings.Cut(line, "="); ok && keyPattern.MatchString(strings.TrimSpace(key)) {
			return true
		}
		if key, _, ok := strings.Cut(line, ":"); ok && keyPattern.MatchString(strings.TrimSpace(key)) {
			return true
		}
		if isBareEnvKey(line) {
			return true
		}
	}
	return false
}

func isBareEnvKey(line string) bool {
	return strings.Contains(line, "_") && keyPattern.MatchString(line)
}

func followingValue(lines []string, current int) (string, int, bool) {
	for next := current + 1; next < len(lines); next++ {
		value := strings.TrimSpace(lines[next])
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if LooksLikeEnv(value) {
			return "", current, false
		}
		return value, next, true
	}
	return "", current, false
}

func Update(path, text, allowedRaw, protectedRaw string) ([]string, error) {
	allowed, protected, err := policy(allowedRaw, protectedRaw)
	if err != nil {
		return nil, err
	}
	updates, err := Parse(text)
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, errors.New("at least one env entry required")
	}
	current, err := Load(path, allowedRaw, protectedRaw)
	if err != nil {
		return nil, err
	}
	if current == nil {
		current = map[string]string{}
	}
	for key, value := range updates {
		if err := checkKey(key, allowed, protected); err != nil {
			return nil, err
		}
		if len(value) > maxBytes {
			return nil, fmt.Errorf("value for %s is too large", key)
		}
		current[key] = value
	}
	if len(current) > maxValues {
		return nil, errors.New("too many self-env keys")
	}
	if err := writeAtomic(path, current); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

func Load(path, allowedRaw, protectedRaw string) (map[string]string, error) {
	allowed, protected, err := policy(allowedRaw, protectedRaw)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("self-env must be a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > maxBytes {
		return nil, errors.New("self-env file is too large")
	}
	values := map[string]string{}
	if err := json.Unmarshal(b, &values); err != nil {
		return nil, fmt.Errorf("invalid self-env file: %w", err)
	}
	if len(values) > maxValues {
		return nil, errors.New("too many self-env keys")
	}
	for key, value := range values {
		if err := checkKey(key, allowed, protected); err != nil {
			return nil, err
		}
		if len(value) > maxBytes {
			return nil, fmt.Errorf("value for %s is too large", key)
		}
	}
	return values, nil
}

// Remove deletes explicitly named legacy values after validating the complete
// file. It is used for one-time migrations so obsolete credentials are not
// carried into a new connector configuration.
func Remove(path, allowedRaw, protectedRaw string, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	legacyAllowed := allowedRaw
	if legacyAllowed != "" {
		legacyAllowed += ","
	}
	legacyAllowed += strings.Join(keys, ",")
	values, err := Load(path, legacyAllowed, protectedRaw)
	if err != nil || values == nil {
		return nil, err
	}
	removed := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := values[key]; ok {
			delete(values, key)
			removed = append(removed, key)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	slices.Sort(removed)
	return removed, writeAtomic(path, values)
}

func policy(allowedRaw, protectedRaw string) (map[string]bool, map[string]bool, error) {
	allowed, err := keySet(allowedRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("self-env allowlist: %w", err)
	}
	protected, err := keySet(protectedRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("protected env keys: %w", err)
	}
	return allowed, protected, nil
}

func keySet(raw string) (map[string]bool, error) {
	set := map[string]bool{}
	for _, key := range strings.Split(raw, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid key %s", key)
		}
		set[key] = true
	}
	return set, nil
}

func checkKey(key string, allowed, protected map[string]bool) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("invalid env key %s", key)
	}
	if protected[key] {
		return fmt.Errorf("organization-owned env key %s cannot be changed", key)
	}
	if blocked[key] || strings.HasPrefix(key, "HUB_") || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "PYTHON") {
		return fmt.Errorf("runtime env key %s cannot be changed", key)
	}
	if len(allowed) > 0 && !allowed[key] {
		return fmt.Errorf("env key %s is not used by this runtime", key)
	}
	return nil
}

func writeAtomic(path string, values map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".self-env-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
