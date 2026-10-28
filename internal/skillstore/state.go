package skillstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// .state is store-private live state (credentials, settings, usage). Dot
// entries are invisible to loadTree/Build, so values kept here can never
// leak into the read-only effective view (R8.2/R8.4).
const (
	credFileMax   = 64 * 1024
	credValueMax  = 16 * 1024
	credNamesMax  = 16
	settingMaxLen = 4096
)

var credNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ValidCredentialName reports whether name is a well-formed env-style
// credential identifier (R8.1): the only shape a lease may carry.
func ValidCredentialName(name string) bool {
	return credNamePattern.MatchString(name)
}

func credentialsFile(storeDir, skill string) (string, error) {
	if !ValidName(skill) {
		return "", fmt.Errorf("%w: skill name %q", ErrInvalid, skill)
	}
	return filepath.Join(storeDir, ".state", "credentials", skill+".json"), nil
}

// ReadCredentials returns the stored name→value map for one skill; absent
// files and corrupt entries degrade to an empty map (a cleared file simply
// means "no secrets leased").
func ReadCredentials(storeDir, skill string) (map[string]string, error) {
	path, err := credentialsFile(storeDir, skill)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path) // #nosec G304 -- path built from a ValidName-checked skill inside the owned store
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(body) > credFileMax {
		return nil, fmt.Errorf("%w: credentials file too large", ErrInvalid)
	}
	var values map[string]string
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, fmt.Errorf("%w: credentials file corrupt", ErrInvalid)
	}
	return values, nil
}

// WriteCredentials replaces one skill's stored secrets. Only env-shaped,
// non-reserved names are admitted; the file is 0600 inside a 0700 dir.
func WriteCredentials(storeDir, skill string, values map[string]string) error {
	path, err := credentialsFile(storeDir, skill)
	if err != nil {
		return err
	}
	if len(values) > credNamesMax {
		return fmt.Errorf("%w: too many credential names", ErrInvalid)
	}
	for name, value := range values {
		if !credNamePattern.MatchString(name) || ReservedSecretName(name) ||
			len(value) == 0 || len(value) > credValueMax || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: credential %q shape", ErrInvalid, name)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return writePrivateFile(path, body)
}

func settingsFile(storeDir, skill string) (string, error) {
	if !ValidName(skill) {
		return "", fmt.Errorf("%w: skill name %q", ErrInvalid, skill)
	}
	return filepath.Join(storeDir, ".state", "settings", skill+".json"), nil
}

// ReadSettings returns the per-user non-secret settings map for a skill.
func ReadSettings(storeDir, skill string) (map[string]string, error) {
	path, err := settingsFile(storeDir, skill)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path) // #nosec G304 -- ValidName-checked path inside the owned store
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, fmt.Errorf("%w: settings file corrupt", ErrInvalid)
	}
	return values, nil
}

// WriteSettings persists per-user non-secret skill settings (R8.4).
func WriteSettings(storeDir, skill string, values map[string]string) error {
	path, err := settingsFile(storeDir, skill)
	if err != nil {
		return err
	}
	for key, value := range values {
		if !credNamePattern.MatchString(key) || len(value) > settingMaxLen || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: setting %q shape", ErrInvalid, key)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return writePrivateFile(path, body)
}

// writePrivateFile writes atomically with 0600 perms (tmp+rename so a torn
// write never leaves a half-readable secrets file).
func writePrivateFile(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
