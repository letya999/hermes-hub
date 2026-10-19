package media

import (
	"bytes"
	"errors"
	"os"
	"strings"
)

func knownSecrets() []string {
	var out []string
	for _, key := range []string{"OPENAI_API_KEY", "FAL_KEY"} {
		value := os.Getenv(key)
		if len(value) >= 8 {
			out = append(out, value)
		}
	}
	return out
}

func containsSecret(s string) bool {
	for _, secret := range knownSecrets() {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

func containsSecretBytes(b []byte) bool {
	for _, secret := range knownSecrets() {
		if bytes.Contains(b, []byte(secret)) {
			return true
		}
	}
	return false
}

func redact(s string) string {
	for _, secret := range knownSecrets() {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

func redactErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redact(err.Error()))
}

func redactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	for key, value := range m {
		if text, ok := value.(string); ok {
			m[key] = redact(text)
		}
	}
	return m
}
