package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreflightCredentialMountRejectsUnsafeSources(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "client.json")
	if err := os.WriteFile(source, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	target := "/run/mcp-secrets/client.json"
	for _, tc := range []struct {
		name  string
		mount Mount
	}{
		{"undeclared", Mount{Source: source, Target: "/etc/passwd", ReadOnly: true}},
		{"writable", Mount{Source: source, Target: target}},
		{"directory", Mount{Source: root, Target: target, ReadOnly: true}},
		{"missing", Mount{Source: filepath.Join(root, "missing"), Target: target, ReadOnly: true}},
		{"relative-target", Mount{Source: source, Target: "relative", ReadOnly: true}},
		{"comma-target", Mount{Source: source, Target: "/run/client,source=/etc/passwd", ReadOnly: true}},
		{"comma-source", Mount{Source: filepath.Join(root, "a,b"), Target: target, ReadOnly: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "comma-source" {
				if err := os.WriteFile(tc.mount.Source, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			definition := ToolDefinition{Credentials: []CredentialInput{{Name: "CLIENT_FILE", Delivery: "file", Target: target}}}
			if tc.name == "relative-target" || tc.name == "comma-target" {
				definition.Credentials[0].Target = tc.mount.Target
			}
			ctx := WithProbeOptions(t.Context(), ProbeOptions{Values: map[string]string{"CLIENT_FILE": target}, Mounts: []Mount{tc.mount}})
			_, cleanup, err := PreflightCredentialArgs(ctx, ImportedArtifact{Definition: definition})
			defer cleanup()
			if err == nil {
				t.Error("unsafe credential mount accepted")
			}
		})
	}
}

func TestPreflightCredentialMountRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	realDir, linkDir := filepath.Join(root, "real"), filepath.Join(root, "link")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "client.json"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	target := "/run/mcp-secrets/client.json"
	ctx := WithProbeOptions(t.Context(), ProbeOptions{Values: map[string]string{"CLIENT_FILE": target}, Mounts: []Mount{{Source: filepath.Join(linkDir, "client.json"), Target: target, ReadOnly: true}}})
	_, cleanup, err := PreflightCredentialArgs(ctx, ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "CLIENT_FILE", Delivery: "file", Target: target}}}})
	defer cleanup()
	if err == nil {
		t.Fatal("symlink parent accepted")
	}
}
