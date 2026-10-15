package stack

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProfilesOverlayDefaultGroupOwner(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	for _, user := range []string{"alice", "bob", "carol"} {
		if err := InitEnvironment(filepath.Join(spaces, user), user, "prod"); err != nil {
			t.Fatal(err)
		}
	}
	// carol keeps her own choice: owner fields override every profile layer.
	carol, err := ReadEnvironment(filepath.Join(spaces, "carol"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	carol.Model = "own-model"
	if err := WriteSpace(filepath.Join(spaces, "carol"), carol); err != nil {
		t.Fatal(err)
	}
	// alice and bob carry no model of their own; profiles supply it. alice is
	// the secondary space: dedicated cliproxy is only renderable off-infra.
	noInfra := false
	for _, user := range []string{"alice", "bob"} {
		s, err := ReadEnvironment(filepath.Join(spaces, user), "prod")
		if err != nil {
			t.Fatal(err)
		}
		s.Model, s.ModelURL = "", ""
		if user == "alice" {
			s.Infra = &noInfra
		}
		if err := WriteSpace(filepath.Join(spaces, user), s); err != nil {
			t.Fatal(err)
		}
	}
	saveYAML(t, filepath.Join(spaces, "profiles.yaml"), map[string]any{
		"profiles": map[string]any{
			"default": map[string]any{"model": "shared-model", "model_url": "http://shared-upstream/v1"},
			"power":   map[string]any{"owners": []string{"alice"}, "model": "power-model", "cliproxy": "dedicated"},
		},
	})

	alice, err := ReadEnvironment(filepath.Join(spaces, "alice"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if alice.Model != "power-model" || alice.Cliproxy != "dedicated" {
		t.Fatalf("named profile did not apply: %+v", alice)
	}
	bob, err := ReadEnvironment(filepath.Join(spaces, "bob"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if bob.Model != "shared-model" || bob.Cliproxy != "" || bob.ModelURL != "http://shared-upstream/v1" {
		t.Fatalf("default profile did not apply: %+v", bob)
	}
	carol, err = ReadEnvironment(filepath.Join(spaces, "carol"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if carol.Model != "own-model" {
		t.Fatalf("owner override lost to profile: %+v", carol)
	}
}

func TestProfilesOverlayModelFallbacks(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	if err := InitEnvironment(filepath.Join(spaces, "alice"), "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironment(filepath.Join(spaces, "bob"), "bob", "prod"); err != nil {
		t.Fatal(err)
	}
	// model_fallbacks render through the managed relay only.
	for _, user := range []string{"alice", "bob"} {
		s, err := ReadEnvironment(filepath.Join(spaces, user), "prod")
		if err != nil {
			t.Fatal(err)
		}
		s.CapabilityMode, s.CapabilityProfileID, s.CapabilityGeneration = "managed", "p", 1
		s.Model, s.ModelURL = "m", "http://model-relay:8318/v1"
		if err := WriteSpace(filepath.Join(spaces, user), s); err != nil {
			t.Fatal(err)
		}
	}
	saveYAML(t, filepath.Join(spaces, "profiles.yaml"), map[string]any{
		"profiles": map[string]any{
			"default": map[string]any{"model_fallbacks": []any{
				map[string]any{"name": "backup", "url": "https://backup.invalid/api", "key_env": "BACKUP_KEY"},
			}},
		},
	})
	alice, err := ReadEnvironment(filepath.Join(spaces, "alice"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(alice.ModelFallbacks) != 1 || alice.ModelFallbacks[0].Name != "backup" || alice.ModelFallbacks[0].KeyEnv != "BACKUP_KEY" {
		t.Fatalf("fallback ladder not inherited: %+v", alice.ModelFallbacks)
	}
	// bob keeps his own ladder over the shared one.
	bob, err := ReadEnvironment(filepath.Join(spaces, "bob"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	bob.ModelFallbacks = []ModelFallback{{Name: "own", URL: "https://own.invalid"}}
	if err := WriteSpace(filepath.Join(spaces, "bob"), bob); err != nil {
		t.Fatal(err)
	}
	bob, err = ReadEnvironment(filepath.Join(spaces, "bob"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(bob.ModelFallbacks) != 1 || bob.ModelFallbacks[0].Name != "own" {
		t.Fatalf("owner ladder lost to profile: %+v", bob.ModelFallbacks)
	}
}

func TestValidateCliproxyAndFallbacks(t *testing.T) {
	base := func() Settings {
		return Settings{Schema: 3, User: "alice", Timezone: "UTC", Model: "m", ModelURL: "http://model-relay:8318/v1",
			CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
			Environment: "prod", BrowserPort: 6080, OAuthPort: 8000}
	}
	bad := []func(*Settings){
		func(s *Settings) { s.Cliproxy = "bogus" },
		func(s *Settings) {
			infra := true
			s.Infra = &infra
			s.Cliproxy = "dedicated"
		},
		func(s *Settings) {
			s.CapabilityMode, s.CapabilityProfileID, s.CapabilityGeneration = "", "", 0
			s.ModelURL = "http://cliproxy:8317/v1"
			s.ModelFallbacks = []ModelFallback{{Name: "x", URL: "https://x.invalid"}}
		},
		func(s *Settings) {
			s.ModelFallbacks = []ModelFallback{{Name: "x", URL: "ftp://x"}}
		},
		func(s *Settings) {
			s.ModelFallbacks = []ModelFallback{{Name: "x", URL: "http://u:p@x.invalid"}}
		},
		func(s *Settings) {
			s.ModelFallbacks = []ModelFallback{{Name: "Bad Name", URL: "https://x.invalid"}}
		},
		func(s *Settings) {
			s.ModelFallbacks = []ModelFallback{{Name: "cliproxy", URL: "https://x.invalid"}}
		},
		func(s *Settings) {
			s.ModelFallbacks = []ModelFallback{{Name: "x", URL: "https://x.invalid"}, {Name: "x", URL: "https://y.invalid"}}
		},
		func(s *Settings) {
			s.ModelFallbacks = []ModelFallback{{Name: "x", URL: "https://x.invalid", KeyEnv: "lower_env"}}
		},
	}
	for i, mutate := range bad {
		s := base()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	good := base()
	noInfra := false
	good.Infra = &noInfra
	good.Cliproxy = "dedicated"
	good.ModelFallbacks = []ModelFallback{{Name: "or", URL: "https://openrouter.ai/api", KeyEnv: "OPENROUTER_KEY"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid topology rejected: %v", err)
	}
}

func TestModelRelayRendersFallbackLadder(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
		Model: "m", ModelURL: "http://model-relay:8318/v1",
		ModelFallbacks: []ModelFallback{{Name: "or", URL: "https://openrouter.ai/api", KeyEnv: "OPENROUTER_KEY"}}}
	relay := Compose(s, "/source", "/space")["services"].(M)["model-relay"].(M)
	env := relay["environment"].(M)
	if env["HUB_MODEL_RELAY_UPSTREAMS"] != "cliproxy=http://cliproxy:8317,or=https://openrouter.ai/api|OPENROUTER_KEY" {
		t.Fatalf("fallback ladder not rendered: %v", env["HUB_MODEL_RELAY_UPSTREAMS"])
	}
	// Hop key_env names resolve from the space's own secrets env file.
	files, _ := relay["env_file"].([]any)
	found := false
	for _, f := range files {
		if m, ok := f.(M); ok && strings.HasSuffix(fmt.Sprint(m["path"]), "runtime.prod.env") {
			found = true
		}
	}
	if !found {
		t.Fatal("model-relay lacks the secrets env file for hop credentials")
	}
}

func TestDedicatedCliproxyRenderedForSecondarySpace(t *testing.T) {
	noInfra := false
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Infra: &noInfra,
		CapabilityMode: "managed", CapabilityProfileID: "p", CapabilityGeneration: 1,
		Model: "m", ModelURL: "http://model-relay:8318/v1", Cliproxy: "dedicated"}
	services := Compose(s, "/source", "/space")["services"].(M)
	cliproxy, ok := services["cliproxy"].(M)
	if !ok {
		t.Fatal("dedicated cliproxy was not rendered")
	}
	// Its alias carries the owner id on both networks so the space-local
	// "cliproxy" name can never shadow the shared infra instance.
	for _, net := range []string{"default", sharedNetworkName} {
		entry, ok := cliproxy["networks"].(M)[net].(M)
		if !ok || !slices.Contains(entry["aliases"].([]string), "cliproxy-alice") {
			t.Fatalf("dedicated cliproxy misses alias on %s: %v", net, cliproxy["networks"])
		}
	}
	env := services["model-relay"].(M)["environment"].(M)
	if env["HUB_MODEL_RELAY_UPSTREAMS"] != "cliproxy=http://cliproxy-alice:8317" {
		t.Fatalf("relay does not point at the dedicated proxy: %v", env["HUB_MODEL_RELAY_UPSTREAMS"])
	}
}
