package stack

import (
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/systemguide"

	"gopkg.in/yaml.v3"
)

// MaterializeSoulEffective composes the effective SOUL (SPEC-0053 R1.8/D7): the
// owner's persona file verbatim plus the hub-owned channel contract. It runs
// after HealUserSoul so the persona it reads is the healed one. The output is
// what the runtime mounts at /state/hermes/SOUL.md, so contract edits move
// the session revision exactly once per render.
func MaterializeSoulEffective(dir string, s Settings) error {
	persona, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	body := strings.TrimRight(string(persona), "\n") + "\n\n" + ChannelContract
	return atomic(filepath.Join(dir, "generated", "soul-effective."+s.Environment+".md"), []byte(body))
}

// writeModelRegistry materializes the merged spaces/org/user models.yaml into
// generated/ so the managed model-relay mounts it read-only. A space with no
// declared models still gets its cliproxy ladder so managed chat keeps working.
func writeModelRegistry(dir string, s Settings) error {
	reg, err := LoadModelRegistry(dir, s)
	if err != nil {
		return err
	}
	if len(reg.Models) == 0 {
		reg.Models["default"] = ModelEntry{Ladder: []ModelHopRef{{Provider: "cliproxy", Model: s.Model}}}
	}
	body, err := yaml.Marshal(reg)
	if err != nil {
		return err
	}
	return atomic(filepath.Join(dir, "generated", "models."+s.Environment+".yaml"), body)
}

// materializeHermesConfig renders the effective Hermes config host-side so the
// runtime container mounts it read-only over the agent-writable state dir.
// Inputs mirror what the in-container startup used to read from the env file
// and runtime.auth produced by this same render.
func materializeHermesConfig(dir string, s Settings) error {
	source := filepath.Join(dir, "hermes."+s.Environment+".yaml")
	dest := filepath.Join(dir, "generated", "hermes-effective."+s.Environment+".yaml")
	envFile := "runtime." + s.Environment + ".env"
	if s.CapabilityMode == "managed" {
		envFile = "managed-runtime." + s.Environment + ".env"
	}
	runtimeEnv, err := ReadSecrets(filepath.Join(dir, envFile))
	if err != nil {
		return err
	}
	auth, _ := ReadSecrets(filepath.Join(dir, "runtime.auth"))
	tokenEnv := strings.TrimSpace(runtimeEnv["HUB_TOOLHUB_TOKEN_ENV"])
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	selfSettingsPath := ""
	if s.CapabilityMode == "managed" {
		selfSettingsPath = filepath.Join(dir, "managed", s.Environment, "runtime", selfsettings.FileName)
	}
	return MaterializeHermesConfig(source, dest, MaterializeOptions{
		Managed:            s.CapabilityMode == "managed",
		ToolHubEndpoint:    strings.TrimSpace(runtimeEnv["HUB_TOOLHUB_ENDPOINT"]),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: strings.TrimSpace(auth[tokenEnv]) != "" || strings.TrimSpace(runtimeEnv[tokenEnv]) != "",
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(runtimeEnv["HUB_TOOLHUB_RECONNECT"]), "false"),
		SelfServicesPath:   filepath.Join(dir, "runtime", "self-services.json"),
		NativeToolsets:     s.nativeCarveouts(),
		Web:                s.Web,
		ImageGen:           s.managedImageGenGrant(),
		SelfSettingsPath:   selfSettingsPath,
	})
}

// GeneratedSkillsDir is the per-space filtered skills tree materialized by
// MaterializeGlobalSkills and mounted at /opt/hub/skills.
func GeneratedSkillsDir(dir string) string {
	return filepath.Join(dir, "generated", "skills")
}

// GeneratedPluginsDir is the per-space filtered hub-plugin tree materialized
// by MaterializeHubPlugins; each gated plugin mounts read-only at
// /opt/hermes/plugins/<name>, which upstream discovers as a bundled backend.
func GeneratedPluginsDir(dir string) string {
	return filepath.Join(dir, "generated", "plugins")
}

// MaterializeHubPlugins mirrors the repo's config/plugins tree into the
// space's generated/plugins mount, dropping plugins whose gating feature is
// disabled. The runtime then sees exactly the plugins this space may load.
func MaterializeHubPlugins(dir, root string, s Settings) error {
	src := filepath.Join(root, "config", "plugins")
	if _, err := os.Stat(src); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		src = ""
	}
	dest := GeneratedPluginsDir(dir)
	if src != "" && filepath.Clean(src) == filepath.Clean(dest) {
		return nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	// Every enabled gated plugin gets a dir even when the repo copy is absent:
	// the compose/supervisor mount expects the source path to exist; an empty
	// dir holds no plugin.yaml, so nothing loads.
	for name, feature := range hubGatedPlugins {
		if s.Has(feature) {
			if err := os.MkdirAll(filepath.Join(dest, name), 0755); err != nil {
				return err
			}
		}
	}
	if src == "" {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return fs.SkipDir
			}
			// A plugin directory is <name>/plugin.yaml; gated names skip wholesale.
			if rel != "." {
				if feature, gated := hubGatedPlugins[d.Name()]; gated && !s.Has(feature) && rel == d.Name() {
					return fs.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0755)
		}
		if strings.HasSuffix(d.Name(), ".pyc") {
			return nil
		}
		body, err := os.ReadFile(path) // #nosec G304 G122 -- operator-owned plugins dir
		if err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(filepath.Join(dest, rel), body, mode) // #nosec G703 -- dest join of a walked rel path
	})
}

// MaterializeGlobalSkills mirrors the shared GlobalSkillsDir into the space's
// generated/skills mount, dropping bundled skills whose gating feature is
// disabled. The container then sees exactly the skills this space may run;
// skills.disabled in the rendered config blocks the same names as a fallback.
func MaterializeGlobalSkills(dir string, s Settings) error {
	if s.CapabilityMode == "managed" {
		return nil
	}
	src := strings.TrimSpace(s.GlobalSkillsDir)
	dest := GeneratedSkillsDir(dir)
	if filepath.Clean(src) == filepath.Clean(dest) {
		return materializeSystemGuide(dir)
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := materializeSystemGuide(dir); err != nil {
		return err
	}
	if src == "" {
		return nil
	}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A skill directory is <name>/SKILL.md; gated names skip wholesale.
			if rel == "hermes-hub-system-guide" {
				return fs.SkipDir
			}
			if rel != "." {
				if feature, gated := webGatedSkills[d.Name()]; gated && !s.Has(feature) && rel == d.Name() {
					return fs.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0755)
		}
		body, err := os.ReadFile(path) // #nosec G304 G122 -- operator-owned skills dir
		if err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(filepath.Join(dest, rel), body, mode) // #nosec G703 -- dest join of a walked rel path
	})
	if err != nil {
		return err
	}
	return materializeSystemGuide(dir)
}

func materializeSystemGuide(dir string) error {
	dest := filepath.Join(GeneratedSkillsDir(dir), "hermes-hub-system-guide")
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(systemguide.Skill), 0644)
}
