package vault

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVaultEnsureIdempotent(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if err := Ensure(root); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	for _, want := range []string{
		"memory/daily", "memory/cards/contacts", "memory/cards/projects",
		"memory/cards/decisions", "memory/cards/ideas", "memory/cards/notes",
		"memory/summaries/daily", "memory/summaries/weekly",
		"memory/summaries/monthly", "memory/summaries/yearly",
		"memory/.graph", "memory/attachments",
	} {
		info, err := os.Stat(filepath.Join(root, want))
		if err != nil || !info.IsDir() {
			t.Fatalf("scaffold dir %q missing: %v", want, err)
		}
	}
	for _, want := range []string{"memory/CORE.md", "memory/CORE.history.md", "memory/schema.json", ".gitignore", ".okignore"} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Fatalf("scaffold file %q missing: %v", want, err)
		}
	}
	// Ensure never overwrites existing content and re-runs cleanly.
	core := filepath.Join(root, "memory", "CORE.md")
	body := []byte("# CORE\n\n## Directives\n- mine\n")
	if err := os.WriteFile(core, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(root); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if got, _ := os.ReadFile(core); string(got) != string(body) {
		t.Fatal("Ensure overwrote an existing file")
	}
}

// Ensure must not steamroll foreign content: a plain file sitting where a
// scaffold directory belongs is a hard error, not a silent replacement.
func TestVaultEnsureDirBlockedByFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory"), []byte("squatter"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(root); err == nil {
		t.Fatal("Ensure must fail when a file blocks a scaffold dir")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "memory")); string(got) != "squatter" {
		t.Fatal("Ensure destroyed the blocking file")
	}
}

func TestVaultEnsureGitRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("Ensure must git-init the vault: %v", err)
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Run(); err != nil {
		t.Fatalf("vault has no initial commit: %v", err)
	}
	// The scaffold itself is committed — git status is clean after Ensure.
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("uncommitted scaffold entries: %s", out)
	}
}

func TestVaultSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.MkdirAll(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := Ensure(filepath.Join(link, "vault")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked vault path must be rejected, got %v", err)
	}
	if err := Ensure(dir + string(filepath.Separator) + ".." + string(filepath.Separator) + "vault"); err == nil {
		t.Fatal("traversal path must be rejected")
	}
}

// Red-team: the scaffold is a whitelist — no file it creates may look like
// secret material, and .okignore must keep repo internals and runtime
// sidecars out of any external index.
func TestVaultScaffoldNeverSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		for _, suffix := range []string{".env", ".auth", ".pem", ".key", ".p12", ".crt"} {
			if strings.HasSuffix(info.Name(), suffix) {
				t.Fatalf("scaffold created secret-named file %q", info.Name())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	okignore, err := os.ReadFile(filepath.Join(root, ".okignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".git/", ".graph/", ".ok/", "runtime/"} {
		if !strings.Contains(string(okignore), want) {
			t.Fatalf(".okignore missing %q", want)
		}
	}
}

// R11.4: replay mints bank ids from hub-held material the space never sees —
// runtime/memory/scope.key is generated once, stable across opens, and
// lives in the hub-owned sidecar zone (never a governed memory path).
func TestScopeMaterialStable(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	m1, err := OpenScopeMaterial(root)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if len(m1) != 32 {
		t.Fatalf("material is %d bytes, want 32", len(m1))
	}
	info, err := os.Stat(filepath.Join(root, "runtime", "memory", "scope.key"))
	if err != nil {
		t.Fatalf("scope.key missing: %v", err)
	}
	// POSIX-only: Windows reports no permission bits, but deployment hosts
	// are POSIX — the mint material must never be world-readable there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("scope.key mode %v, want 0600", info.Mode().Perm())
	}
	m2, err := OpenScopeMaterial(root)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if string(m1) != string(m2) {
		t.Fatal("scope material must be stable across opens — a mint must reproduce")
	}
}
