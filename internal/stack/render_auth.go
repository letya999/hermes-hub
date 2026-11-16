package stack

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func ensureRuntimeAuth(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- path under the stack render dir
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runtime auth must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	content := []byte("HUB_RUNTIME_AUTH=" + hex.EncodeToString(value) + "\n")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G703 -- path under the stack render dir
	if err != nil {
		if os.IsExist(err) {
			return ensureRuntimeAuth(path)
		}
		return err
	}
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path) // #nosec G703 -- path under the stack render dir
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path) // #nosec G703 -- path under the stack render dir
		return err
	}
	return f.Close()
}

// ensureMediaAuth provisions the shared bearer token between communication-hub
// and the media sidecars. One file, three mounts: gateway reads it for
// HUB_STT_AUTH/HUB_TTS_AUTH, sidecars read HUB_MEDIA_AUTH from the same file.
func ensureMediaAuth(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- path under the stack render dir
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("media auth must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	token := hex.EncodeToString(value)
	content := []byte("HUB_MEDIA_AUTH=" + token + "\nHUB_STT_AUTH=" + token + "\nHUB_TTS_AUTH=" + token + "\n")
	return os.WriteFile(path, content, 0600) // #nosec G703 -- path under the stack render dir
}

// ensureModelRelayAuth provisions the model relay caller and admin bearers.
func ensureModelRelayAuth(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- path under the stack render dir
	values := map[string]string{}
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("model-relay auth must be a regular file")
		}
		values, err = ReadSecrets(path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	changed := false
	for _, name := range []string{"HUB_MODEL_RELAY_ADMIN", "HUB_MODEL_RELAY_AUTH"} {
		if values[name] != "" {
			continue
		}
		value := make([]byte, 32)
		if _, err = rand.Read(value); err != nil {
			return err
		}
		values[name] = hex.EncodeToString(value)
		changed = true
	}
	if !changed {
		return nil
	}
	return writeEnvFile(path, values)
}

// ensureIAMAuth provisions the bundled identity-hub's secrets: one postgres
// password the api and the database share, the 32-char masterkey encrypting
// secrets at rest, and the login-session cookie secret. Values persist
// across renders — the masterkey must never rotate once the store exists.
func ensureIAMAuth(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- path under the stack render dir
	values := map[string]string{}
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("identity-hub auth must be a regular file")
		}
		values, err = ReadSecrets(path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	changed := false
	generate := func(length int) string {
		value := make([]byte, length)
		if _, err = rand.Read(value); err != nil {
			return ""
		}
		return hex.EncodeToString(value)
	}
	if values["POSTGRES_PASSWORD"] == "" {
		values["POSTGRES_PASSWORD"] = generate(32)
		changed = true
	}
	// ZITADEL connects as the zitadel user for reads/writes and needs the
	// same role for schema admin on first init — the shared postgres
	// currently serves only this consumer, so one credential pair does.
	for _, key := range []string{"ZITADEL_DATABASE_POSTGRES_USER_PASSWORD", "ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD"} {
		if values[key] == "" {
			values[key] = values["POSTGRES_PASSWORD"]
			changed = true
		}
	}
	// Exactly 32 chars: 16 random bytes hex-encode to 32.
	if values["ZITADEL_MASTERKEY"] == "" {
		values["ZITADEL_MASTERKEY"] = generate(16)
		changed = true
	}
	if values["ZITADEL_SESSION_COOKIE_SECRET"] == "" {
		values["ZITADEL_SESSION_COOKIE_SECRET"] = generate(16)
		changed = true
	}
	if !changed {
		return nil
	}
	return writeEnvFile(path, values)
}

// The shared ToolHub gets only the caller bearer, never relay admin authority.
func writeModelRelayCallerAuth(dir string) error {
	values, err := ReadSecrets(filepath.Join(dir, "model-relay.auth"))
	if err != nil || values["HUB_MODEL_RELAY_AUTH"] == "" {
		return fmt.Errorf("model-relay caller auth unavailable")
	}
	path := filepath.Join(dir, "model-relay-caller.auth")
	if err := writeEnvFile(path, map[string]string{"HUB_MODEL_RELAY_AUTH": values["HUB_MODEL_RELAY_AUTH"]}); err != nil {
		return err
	}
	return allowServiceGroupRead(path)
}

// Secondary spaces use the shared sidecar's bearer, not an unused local token.
func runtimeMediaAuth(dir string, s Settings) (map[string]string, error) {
	source := filepath.Join(dir, "media.auth")
	parent := filepath.Dir(filepath.Clean(dir))
	if !s.RendersInfra() && s.PassportServices["hub-media"] != "per-space" && filepath.Base(parent) == "spaces" {
		entries, err := os.ReadDir(parent)
		if err != nil {
			return nil, err
		}
		owner := ""
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			candidate := filepath.Join(parent, entry.Name())
			settings, err := ReadEffective(candidate, s.Environment)
			if err != nil || !settings.RendersInfra() || !settings.Has("image_gen") {
				continue
			}
			if owner != "" {
				return nil, fmt.Errorf("shared media infrastructure is ambiguous")
			}
			owner = candidate
		}
		if owner == "" {
			return nil, fmt.Errorf("shared media infrastructure is unavailable")
		}
		source = filepath.Join(owner, "media.auth")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("media auth must be a regular file")
	}
	auth, err := ReadSecrets(source)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(auth["HUB_MEDIA_AUTH"]) == "" {
		return nil, fmt.Errorf("shared media auth is unavailable")
	}
	return auth, nil
}

// ensureTokenFile creates a random 256-bit hex token file unless it exists.
func ensureTokenFile(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- path under the stack render dir
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("token file must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G703 -- path under the stack render dir
	if err != nil {
		if os.IsExist(err) {
			return ensureTokenFile(path)
		}
		return err
	}
	if _, err = f.Write([]byte(hex.EncodeToString(value) + "\n")); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func ensureContainerTokenFile(path string) error {
	if err := ensureTokenFile(path); err != nil {
		return err
	}
	return allowServiceGroupRead(path)
}

func allowServiceGroupRead(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chown(path, -1, os.Getgid()); err != nil {
		return err
	}
	return os.Chmod(path, 0640)
}
