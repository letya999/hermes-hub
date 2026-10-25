package devcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateRoot builds a minimal project tree satisfying every docs gate rule.
func gateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"AGENTS.md", "README.md", "SETUP.md", "CONTRIBUTING.md", "SECURITY.md", "LICENSE"} {
		write(name, "ok\n")
	}
	doc := "---\ndescription: i\nlast_verified: 9999-01-01\n---\n"
	write("docs/index.md", doc)
	write("docs/adr/index.md", doc+"ADR-0001-x.md\n")
	write("docs/adr/ADR-0001-x.md", doc+"# ADR-0001\n")
	write("specs/index.md", "SPEC-0001-x.md\n")
	write("specs/active/SPEC-0001-x.md", "# SPEC-0001\n")
	write(".work/index.md", "CHG-0001-x\n")
	write(".work/done/CHG-0001-x/state.yaml", "status: done\n")
	return root
}

func gateWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectGateNumberingAndIndex(t *testing.T) {
	t.Run("clean tree passes", func(t *testing.T) {
		if err := Project(gateRoot(t)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("duplicate ADR number rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "docs/adr/ADR-0001-y.md",
			"---\ndescription: x\nlast_verified: 9999-01-01\n---\n# ADR-0001\n")
		gateWrite(t, root, "docs/adr/index.md",
			"---\ndescription: i\nlast_verified: 9999-01-01\n---\nADR-0001-x.md ADR-0001-y.md\n")
		if err := Project(root); err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("duplicate ADR number accepted: %v", err)
		}
	})

	t.Run("unindexed spec rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "specs/active/SPEC-0002-y.md", "# SPEC-0002\n")
		if err := Project(root); err == nil || !strings.Contains(err.Error(), "not in index") {
			t.Fatalf("unindexed spec accepted: %v", err)
		}
	})

	t.Run("unindexed CHG dir rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, ".work/done/CHG-0002-x/state.yaml", "status: done\n")
		if err := Project(root); err == nil || !strings.Contains(err.Error(), "not in index") {
			t.Fatalf("unindexed CHG accepted: %v", err)
		}
	})

	t.Run("missing last_verified rejected", func(t *testing.T) {
		root := gateRoot(t)
		gateWrite(t, root, "docs/plain.md", "---\ndescription: x\n---\nbody\n")
		if err := Project(root); err == nil || !strings.Contains(err.Error(), "missing last_verified") {
			t.Fatalf("doc without last_verified accepted: %v", err)
		}
	})

	t.Run("over 500 lines rejected", func(t *testing.T) {
		root := gateRoot(t)
		body := "---\ndescription: x\nlast_verified: 9999-01-01\n---\n" + strings.Repeat("line\n", 600)
		gateWrite(t, root, "docs/huge.md", body)
		if err := Project(root); err == nil || !strings.Contains(err.Error(), "over 500") {
			t.Fatalf("oversize doc accepted: %v", err)
		}
	})

	t.Run("allowlisted historical collision passes", func(t *testing.T) {
		root := gateRoot(t)
		for _, d := range []string{"CHG-0015-migration-map", "CHG-0015-durable-job-runtime-mappings"} {
			gateWrite(t, root, ".work/done/"+d+"/state.yaml", "status: done\n")
		}
		gateWrite(t, root, ".work/index.md",
			"CHG-0001-x CHG-0015-migration-map CHG-0015-durable-job-runtime-mappings\n")
		if err := Project(root); err != nil {
			t.Fatalf("historical collision rejected: %v", err)
		}
	})
}

func TestProjectGateFreshness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := gateRoot(t)
	gateWrite(t, root, "docs/stale.md",
		"---\ndescription: x\nlast_verified: 2000-01-01\n---\nbody\n")
	gateWrite(t, root, "docs/fresh.md",
		"---\ndescription: x\nlast_verified: 9999-01-01\n---\nbody\n")
	gateWrite(t, root, "docs/adr/ADR-0099-old.md",
		"---\ndescription: x\nlast_verified: 2000-01-01\n---\n# ADR-0099\n")
	gateWrite(t, root, "docs/adr/index.md",
		"---\ndescription: i\nlast_verified: 9999-01-01\n---\nADR-0001-x.md ADR-0099-old.md\n")
	// docs/operations.md is allowlisted legacy drift until CHG-0136 reviews it.
	gateWrite(t, root, "docs/operations.md",
		"---\ndescription: x\nlast_verified: 2000-01-01\n---\nbody\n")
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", root}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")

	err := Project(root)
	if err == nil || !strings.Contains(err.Error(), "stale.md") {
		t.Fatalf("stale living doc accepted: %v", err)
	}
	for _, exempt := range []string{"ADR-0099", "operations.md", "fresh.md"} {
		if strings.Contains(err.Error(), exempt) {
			t.Fatalf("%s required freshness bump: %v", exempt, err)
		}
	}
}
