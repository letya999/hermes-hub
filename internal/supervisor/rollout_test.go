package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRolloutPinsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pins := RolloutPins{Envs: map[string]RolloutEnv{
		"prod": {
			Current:  RolloutPin{Image: "registry.example/img:1.2.3", SmokedAt: time.Now().UTC().Truncate(time.Second)},
			Previous: &RolloutPin{Image: "registry.example/img:1.2.2", SmokedAt: time.Now().UTC().Add(-time.Hour).Truncate(time.Second)},
			Canary:   &CanaryPin{Image: "registry.example/img:1.3.0-rc1", ContextID: "ctx1", SetAt: time.Now().UTC().Truncate(time.Second)},
		},
	}}
	if err := WriteRolloutPins(dir, pins); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadRolloutPins(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	e := got.Envs["prod"]
	if e.Current.Image != "registry.example/img:1.2.3" || e.Previous == nil || e.Previous.Image != "registry.example/img:1.2.2" {
		t.Fatalf("pins lost in round trip: %+v", e)
	}
	if e.Canary == nil || e.Canary.ContextID != "ctx1" || !e.Canary.SetAt.Equal(pins.Envs["prod"].Canary.SetAt) {
		t.Fatalf("canary lost in round trip: %+v", e.Canary)
	}
	if _, err := ReadRolloutPins(t.TempDir()); err != nil {
		t.Fatalf("missing file must read as empty: %v", err)
	}
}

func TestRolloutPinsRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := []RolloutPins{
		{Envs: map[string]RolloutEnv{"staging": {Current: RolloutPin{Image: "img:1"}}}},
		{Envs: map[string]RolloutEnv{"prod": {Current: RolloutPin{Image: "img:bad ref"}}}},
		{Envs: map[string]RolloutEnv{"prod": {Canary: &CanaryPin{Image: "img:1", ContextID: "bad ctx!"}}}},
	}
	for i, p := range bad {
		if err := WriteRolloutPins(dir, p); err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rolloutPinsPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRolloutPins(dir); err == nil {
		t.Fatal("malformed pin file must fail")
	}
}

func TestReadRejectsForgedPinImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	forged := `{"envs":{"prod":{"current":{"image":"img --privileged","smoked_at":"2026-01-01T00:00:00Z"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "generated", "image-pins.json"), []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRolloutPins(dir); err == nil {
		t.Fatal("forged pin image must be rejected on read")
	}
	image, source, err := resolveRuntimeImage(dir, "u1", "ctx-1", "prod", "flag:img")
	if err != nil {
		t.Fatal(err)
	}
	if image != "flag:img" || source != "default" {
		t.Fatalf("forged pin steered spawn: %q via %s", image, source)
	}
}

func TestReadRejectsForgedPreviousAndCanary(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		`{"envs":{"prod":{"current":{"image":"img/ok:1"},"previous":{"image":"img --privileged"}}}}`,
		`{"envs":{"prod":{"current":{"image":"img/ok:1"},"canary":{"image":"img -e X=Y","context_id":"ctx-1"}}}}`,
	}
	for i, forged := range cases {
		sub := filepath.Join(dir, fmt.Sprintf("c%d", i))
		if err := os.MkdirAll(filepath.Join(sub, "generated"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "generated", "image-pins.json"), []byte(forged), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadRolloutPins(sub); err == nil {
			t.Fatalf("case %d: forged image must be rejected on read", i)
		}
	}
}

func TestRuntimeImageFlagDefault(t *testing.T) {
	image, source, err := resolveRuntimeImage(t.TempDir(), "me", "ctx1", "prod", "img/flag:1")
	if err != nil || image != "img/flag:1" || source != "default" {
		t.Fatalf("got %q %q %v", image, source, err)
	}
}

func TestRuntimeImagePinBeatsFlag(t *testing.T) {
	dir := t.TempDir()
	pins := RolloutPins{Envs: map[string]RolloutEnv{"prod": {Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()}}}}
	if err := WriteRolloutPins(dir, pins); err != nil {
		t.Fatal(err)
	}
	image, source, err := resolveRuntimeImage(dir, "me", "ctx1", "prod", "img/flag:1")
	if err != nil || image != "img/stable:1" || source != "pin" {
		t.Fatalf("got %q %q %v", image, source, err)
	}
	// a pin for another env must not leak
	image, source, err = resolveRuntimeImage(dir, "me", "ctx1", "dev", "img/flag:1")
	if err != nil || image != "img/flag:1" || source != "default" {
		t.Fatalf("dev must not see prod pin: got %q %q %v", image, source, err)
	}
}

func TestRuntimeImageCanaryScoped(t *testing.T) {
	dir := t.TempDir()
	pins := RolloutPins{Envs: map[string]RolloutEnv{"prod": {
		Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()},
		Canary:  &CanaryPin{Image: "img/next:rc", ContextID: "ctx1", SetAt: time.Now().UTC()},
	}}}
	if err := WriteRolloutPins(dir, pins); err != nil {
		t.Fatal(err)
	}
	image, source, err := resolveRuntimeImage(dir, "me", "ctx1", "prod", "img/flag:1")
	if err != nil || image != "img/next:rc" || source != "canary" {
		t.Fatalf("canary ctx: got %q %q %v", image, source, err)
	}
	image, source, err = resolveRuntimeImage(dir, "me", "ctx2", "prod", "img/flag:1")
	if err != nil || image != "img/stable:1" || source != "pin" {
		t.Fatalf("other ctx must stay on pin: got %q %q %v", image, source, err)
	}
}

func TestRuntimeImageDevPinBeatsRollout(t *testing.T) {
	dir := t.TempDir()
	pins := RolloutPins{Envs: map[string]RolloutEnv{"dev": {Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()}}}}
	if err := WriteRolloutPins(dir, pins); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "dev-runtime-image"), []byte("hermes-hub-dev-me-hermes-runtime:local"), 0o600); err != nil {
		t.Fatal(err)
	}
	image, source, err := resolveRuntimeImage(dir, "me", "ctx1", "dev", "img/flag:1")
	if err != nil || image != "hermes-hub-dev-me-hermes-runtime:local" || source != "dev" {
		t.Fatalf("got %q %q %v", image, source, err)
	}
}

func TestRuntimeImageMalformedPinsFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rolloutPinsPath(dir), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	image, source, err := resolveRuntimeImage(dir, "me", "ctx1", "prod", "img/flag:1")
	if err != nil || image != "img/flag:1" || source != "default" {
		t.Fatalf("malformed pins must fall back to flag: got %q %q %v", image, source, err)
	}
}

func TestSpawnArgsUseRolloutPin(t *testing.T) {
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	m.cfg.Environment = "prod"
	if err := WriteRolloutPins(ctxRoot, RolloutPins{Envs: map[string]RolloutEnv{
		"prod": {Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()}},
	}}); err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(binding(ctxRoot), "fixture", 19000, "gen-1")
	if err != nil {
		t.Fatalf("runArgs: %v", err)
	}
	if args[len(args)-2] != "img/stable:1" {
		t.Fatalf("spawn did not pick rollout pin: %v", args[len(args)-2:])
	}
}

func TestManagedSpawnArgsUseRolloutPin(t *testing.T) {
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	settings := writeManagedFixtures(t, ctxRoot)
	m.cfg.Environment = "prod"
	settings.Environment = "prod"
	if err := WriteRolloutPins(ctxRoot, RolloutPins{Envs: map[string]RolloutEnv{
		"prod": {Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()}},
	}}); err != nil {
		t.Fatal(err)
	}
	args, err := m.managedRunArgs(binding(ctxRoot), settings, "fixture", "gen")
	if err != nil {
		t.Fatalf("managedRunArgs: %v", err)
	}
	if args[len(args)-2] != "img/stable:1" {
		t.Fatalf("managed spawn did not pick rollout pin: %v", args[len(args)-2:])
	}
}

func TestControlRelayUsesRolloutPin(t *testing.T) {
	var run []string
	m, ctxRoot := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			run = append([]string(nil), args...)
		}
		return nil, nil
	}, nil)
	m.cfg.Environment = "prod"
	if err := WriteRolloutPins(ctxRoot, RolloutPins{Envs: map[string]RolloutEnv{
		"prod": {Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.spawnControlRelay(context.Background(), binding(ctxRoot), "runtime", 8091, "generation", "agent-net"); err != nil {
		t.Fatal(err)
	}
	if len(run) < 2 || run[len(run)-2] != "img/stable:1" {
		t.Fatalf("relay image: %v", run)
	}
}

func TestEnsureRecordsImageSource(t *testing.T) {
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil },
		func(context.Context, string, string) error { return nil })
	m.cfg.Environment = "prod"
	if err := WriteRolloutPins(ctxRoot, RolloutPins{Envs: map[string]RolloutEnv{
		"prod": {Current: RolloutPin{Image: "img/stable:1", SmokedAt: time.Now().UTC()}},
	}}); err != nil {
		t.Fatal(err)
	}
	r, err := m.Ensure(context.Background(), binding(ctxRoot))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if r.ImageSource != "pin" {
		t.Fatalf("runtime record lost image source: %q", r.ImageSource)
	}
}
