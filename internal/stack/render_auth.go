package stack

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ensureRuntimeAuth(path string) error {
	info, err := os.Lstat(path)
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return ensureRuntimeAuth(path)
		}
		return err
	}
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// ensureMediaAuth provisions the shared bearer token between communication-hub
// and the media sidecars. One file, three mounts: gateway reads it for
// HUB_STT_AUTH/HUB_TTS_AUTH, sidecars read HUB_MEDIA_AUTH from the same file.
func ensureMediaAuth(path string) error {
	info, err := os.Lstat(path)
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
	return os.WriteFile(path, content, 0600)
}

// Secondary spaces use the shared sidecar's bearer, not an unused local token.
func runtimeMediaAuth(dir string, s Settings) (map[string]string, error) {
	source := filepath.Join(dir, "media.auth")
	parent := filepath.Dir(filepath.Clean(dir))
	if !s.RendersInfra() && filepath.Base(parent) == "spaces" {
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
			settings, err := ReadEnvironment(candidate, s.Environment)
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
	info, err := os.Lstat(path)
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
