package locale

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Prefs is the per-user persisted preference record. It lives at the root of
// the user's workspace directory so both the gateway and the runtime (which
// mounts the same workspace) can read it.
type Prefs struct {
	Lang Lang `json:"lang"`
	// Source records how Lang was established: "chosen" (explicit /language or
	// picker), "detected" (channel-provided locale) or "default" (fallthrough).
	// Only "chosen" is terminal — a later detection may upgrade the rest.
	Source string `json:"source"`
}

const (
	SourceChosen   = "chosen"
	SourceDetected = "detected"
	SourceDefault  = "default"
)

// PrefsFile is the preferences file name inside the user's workspace.
const PrefsFile = "preferences.json"

func prefsPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, PrefsFile)
}

// LoadPrefs reads the workspace preferences; a missing file yields the zero
// value so callers can ladder on.
func LoadPrefs(workspaceDir string) Prefs {
	var p Prefs
	data, err := os.ReadFile(prefsPath(workspaceDir))
	if err != nil {
		return p
	}
	_ = json.Unmarshal(data, &p)
	return p
}

// StorePrefs writes preferences inside the workspace, creating the directory
// for users whose workspace has not been materialized by a run yet.
func StorePrefs(workspaceDir string, p Prefs) error {
	if strings.TrimSpace(workspaceDir) == "" {
		return errors.New("no workspace dir")
	}
	if err := os.MkdirAll(workspaceDir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(prefsPath(workspaceDir), data, 0600)
}

// Operator resolves the local console language for tooling that has no user
// workspace: an explicit HUB_LANG wins, then the standard LC_ALL/LANG ladder,
// then the hub fallback. Unknown values fall through to the next rung.
func Operator() Lang {
	for _, key := range []string{"HUB_LANG", "LC_ALL", "LANG"} {
		if l, ok := Parse(os.Getenv(key)); ok {
			return l
		}
	}
	return Fallback()
}

// LangOf returns the persisted language, or Default when nothing supported is
// stored — corrupt or unknown values degrade to the default, never panic.
func LangOf(workspaceDir string) Lang {
	if strings.TrimSpace(workspaceDir) == "" {
		return Fallback()
	}
	p := LoadPrefs(workspaceDir)
	if supported[p.Lang] {
		return p.Lang
	}
	return Fallback()
}
func Resolve(workspaceDir, hint string) (Lang, string) {
	if strings.TrimSpace(workspaceDir) == "" {
		if l, ok := Parse(hint); ok {
			return l, "detected"
		}
		return Fallback(), "picker"
	}
	p := LoadPrefs(workspaceDir)
	if supported[p.Lang] && (p.Source == SourceChosen || p.Source == SourceDetected) {
		return p.Lang, ""
	}
	if l, ok := Parse(hint); ok {
		return l, "detected"
	}
	if supported[p.Lang] {
		return p.Lang, ""
	}
	return Fallback(), "picker"
}

// StoreResult persists what Resolve decided: the action becomes the prefs
// source so later resolves know whether the language may still be upgraded.
func StoreResult(workspaceDir string, lang Lang, action string) bool {
	var source string
	switch action {
	case "detected":
		source = SourceDetected
	case "chosen":
		source = SourceChosen
	case "picker":
		source = SourceDefault
	default:
		return false
	}
	return StorePrefs(workspaceDir, Prefs{Lang: lang, Source: source}) == nil
}
