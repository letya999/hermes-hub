package devcheck

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var cyrillicRunes = regexp.MustCompile(`[\p{Cyrillic}]`)

// Local scripts print through a per-language table; a literal Cyrillic call
// site would silently pin Russian for every operator.
func TestOperatorScriptsKeepCyrillicInTables(t *testing.T) {
	psSites := regexp.MustCompile(`(Write-Host|throw |Read-Host|\.Title\s*=)\s*['"]`)
	pySites := regexp.MustCompile(`(getpass\.getpass|print|raise ValueError)\(\s*["']`)
	for _, tc := range []struct {
		path string
		call *regexp.Regexp
	}{
		{"../../scripts/local-accounts.ps1", psSites},
		{"../../scripts/start-local-accounts.ps1", psSites},
		{"../../docker/telegram-account-login.py", pySites},
	} {
		data, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if tc.call.MatchString(line) && cyrillicRunes.MatchString(line) {
				t.Errorf("%s:%d user-facing literal not translated: %s", tc.path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Operator scripts honor the operator's language: HUB_LANG is the explicit
// channel (the ps1 wrapper forwards it into the login container), and English
// stays the guaranteed fallback inside every translation table.
func TestOperatorScriptsResolveLangEnv(t *testing.T) {
	for _, path := range []string{
		"../../scripts/local-accounts.ps1",
		"../../scripts/start-local-accounts.ps1",
		"../../docker/telegram-account-login.py",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body := string(data)
		if !strings.Contains(body, "HUB_LANG") {
			t.Errorf("%s never reads HUB_LANG", path)
		}
		for _, lang := range []string{"en", "ru", "kk", "es"} {
			if !strings.Contains(body, lang+" = ") && !strings.Contains(body, "\""+lang+"\":") {
				t.Errorf("%s misses %s translations", path, lang)
			}
		}
	}
}
