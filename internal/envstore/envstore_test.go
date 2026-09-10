package envstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateLoadsOnlyAllowedUserKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	keys, err := Update(path, "GITHUB_TOKEN=secret=with-equals\n# comment\n", "GITHUB_TOKEN,SLACK_MCP_XOXP_TOKEN", "ORG_TOKEN")
	if err != nil || len(keys) != 1 || keys[0] != "GITHUB_TOKEN" {
		t.Fatal(keys, err)
	}
	values, err := Load(path, "GITHUB_TOKEN,SLACK_MCP_XOXP_TOKEN", "ORG_TOKEN")
	if err != nil || values["GITHUB_TOKEN"] != "secret=with-equals" {
		t.Fatal(values, err)
	}
	if strings.Contains(string(mustRead(t, path)), "SLACK") {
		t.Fatal("unexpected key persisted")
	}
	if _, err := Update(path, "SLACK_MCP_XOXP_TOKEN=second", "GITHUB_TOKEN,SLACK_MCP_XOXP_TOKEN", "ORG_TOKEN"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRejectsProtectedAndRuntimeKeysWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if _, err := Update(path, "GITHUB_TOKEN=keep", "GITHUB_TOKEN", "ORG_TOKEN"); err != nil {
		t.Fatal(err)
	}
	before := string(mustRead(t, path))
	for _, text := range []string{"ORG_TOKEN=x", "HUB_ORG_ACTIONS=x", "PATH=x", "UNKNOWN=x"} {
		if _, err := Update(path, text, "GITHUB_TOKEN,OTHER", "ORG_TOKEN"); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if got := string(mustRead(t, path)); got != before {
		t.Fatal("failed update changed overlay")
	}
}

func TestLoadRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(`{"TOKEN":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, FileName)
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	if _, err := Load(path, "TOKEN", ""); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestEnvstoreRejectsMalformedPoliciesAndFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	for _, raw := range []string{"BAD KEY=x", "TOKEN=x\nTOKEN=y", ""} {
		if _, err := Parse(raw); raw != "" && err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := Update(path, "TOKEN=x", "BAD KEY", ""); err == nil {
		t.Fatal("invalid allowlist accepted")
	}
	if _, err := Update(path, "TOKEN=x", "TOKEN", "BAD KEY"); err == nil {
		t.Fatal("invalid protected list accepted")
	}
	if got, err := Load(filepath.Join(t.TempDir(), FileName), "TOKEN", ""); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "TOKEN", ""); err == nil {
		t.Fatal("malformed overlay accepted")
	}
	if err := os.WriteFile(path, []byte(`{"UNKNOWN":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "TOKEN", ""); err == nil {
		t.Fatal("disallowed overlay accepted")
	}
}

func TestEnvstoreWriteFailure(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "file")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(filepath.Join(parent, FileName), "TOKEN=x", "TOKEN", ""); err == nil {
		t.Fatal("write through file accepted")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
