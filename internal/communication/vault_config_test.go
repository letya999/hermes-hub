package communication

import "testing"

// SPEC-0085 R1.6: the gateway learns the mounted vault from HUB_VAULT and the
// run runner carries it — without the env, no CORE is injected.
func TestConfigFromEnvVaultDir(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_RUNTIME_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	vaultDir := t.TempDir()
	t.Setenv("HUB_VAULT", vaultDir)
	t.Setenv("HUB_COMMUNICATION_SPOOL", t.TempDir())
	t.Setenv("HUB_STATE", t.TempDir())
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.VaultDir != vaultDir {
		t.Fatalf("HUB_VAULT not mapped: %q", config.VaultDir)
	}
	g, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if runner, ok := g.runner.(HTTPRunner); !ok || runner.VaultDir != vaultDir {
		t.Fatalf("runner vault not wired: %#v", g.runner)
	}
}
