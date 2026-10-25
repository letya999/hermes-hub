package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapDiffRejectsUnboundedWorkspace(t *testing.T) {
	old := workspace
	t.Cleanup(func() { workspace = old })
	for _, mode := range []string{"entries", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			workspace = t.TempDir()
			if mode == "entries" {
				for i := 0; i < 4097; i++ {
					if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("file-%05d", i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				file, err := os.Create(filepath.Join(workspace, "huge"))
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate((64 << 20) + 1); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := workspaceSnapshot(); err == nil {
				t.Fatalf("bootstrap diff accepted unbounded %s", mode)
			}
		})
	}
}
