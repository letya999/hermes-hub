package communication

import "testing"

// The gateway learns the mounted vault from HUB_VAULT for the nightly memory
// worker — the only remaining communication-side vault consumer pending its
// own ToolHub migration (M32 leaves the run path entirely vault-free).
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
}
