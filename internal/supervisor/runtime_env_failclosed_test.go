package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unreadable, corrupt or missing runtime env must refuse materialization
// instead of rendering a config from an empty secret map; the error never
// echoes a key or value from the file.
func TestSupervisorFailsClosedOnUnreadableRuntimeEnv(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	if _, err := m.materializeHermesConfig(context.Background(), binding(root), "prod"); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(root, "managed-runtime.prod.env")
	for name, corrupt := range map[string]func() error{
		"directory": func() error { return os.Mkdir(envPath, 0700) },
		"duplicate key": func() error {
			return os.WriteFile(envPath, []byte("SENTINEL_SECRET_KEY=first-value\nSENTINEL_SECRET_KEY=second-value\n"), 0600)
		},
		"missing": func() error { return nil },
	} {
		if err := os.RemoveAll(envPath); err != nil {
			t.Fatal(err)
		}
		if err := corrupt(); err != nil {
			t.Fatal(err)
		}
		_, err := m.materializeHermesConfig(context.Background(), binding(root), "prod")
		if err == nil {
			t.Fatalf("%s runtime env materialized a config", name)
		}
		for _, leaked := range []string{"SENTINEL_SECRET_KEY", "first-value", "second-value"} {
			if strings.Contains(err.Error(), leaked) {
				t.Fatalf("%s runtime env error leaked %q: %v", name, leaked, err)
			}
		}
	}
}
