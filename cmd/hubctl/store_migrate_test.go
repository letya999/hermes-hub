package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The store-migrate command turns an existing store.json into store.db via
// the verified substrate migration; it refuses without --apply and never
// writes to the source.
func TestStoreMigrateCLI(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "state", "store.json")

	seed := toolhub.NewStore()
	definition := api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: "def", Version: "1.0.0", Transport: api.RemoteMCP, Source: api.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"}, Workload: api.WorkloadPolicy{Class: api.PerUser, Rationale: "per-user remote tool"}, Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}}, Health: api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}, Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}}
	if err := seed.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(from); err != nil {
		t.Fatal(err)
	}

	if err := run(t.Context(), []string{"store-migrate", "--toolhub-store", from}); err == nil {
		t.Fatal("store-migrate without --apply must refuse")
	}
	to := filepath.Join(dir, "state", "store.db")
	if _, err := os.Lstat(to); !os.IsNotExist(err) {
		t.Fatal("dry-run created the destination")
	}
	if err := run(t.Context(), []string{"store-migrate", "--toolhub-store", from, "--apply"}); err != nil {
		t.Fatalf("apply via run(): %v", err)
	}
	migrated, err := toolhub.Load(to)
	if err != nil {
		t.Fatal(err)
	}
	migrated.CloseDB()
	if _, err := os.Stat(to + ".manifest.json"); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(from + ".substrate-backup"); err != nil {
		t.Fatalf("rollback copy missing: %v", err)
	}
	if err := run(t.Context(), []string{"store-activate", "--toolhub-store", from, "--to", to}); err == nil {
		t.Fatal("store-activate without --apply must refuse")
	}
	if err := run(t.Context(), []string{"store-activate", "--toolhub-store", from, "--to", to, "--apply"}); err != nil {
		t.Fatalf("activate via run(): %v", err)
	}
	if _, err := os.Lstat(from); !os.IsNotExist(err) {
		t.Fatal("legacy store still live after activation")
	}
	if _, err := os.Stat(from + ".legacy"); err != nil {
		t.Fatalf("retired copy missing: %v", err)
	}
}

// The same commands take --spool for the communication spool: import lands
// in <spool>/spool.db, activation flips records.activated and retires files.
func TestStoreMigrateCLIForSpool(t *testing.T) {
	spool := t.TempDir()
	pending := filepath.Join(spool, "pending")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "job-c-1.json"), []byte(`{"id":"job-c-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"store-migrate", "--spool", spool}); err == nil {
		t.Fatal("store-migrate without --apply must refuse")
	}
	if _, err := os.Lstat(filepath.Join(spool, "spool.db")); !os.IsNotExist(err) {
		t.Fatal("dry-run created spool.db")
	}
	if err := run(t.Context(), []string{"store-migrate", "--spool", spool, "--apply"}); err != nil {
		t.Fatalf("spool import: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(spool, "spool.db.manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if err := run(t.Context(), []string{"store-activate", "--spool", spool}); err == nil {
		t.Fatal("store-activate without --apply must refuse")
	}
	if err := run(t.Context(), []string{"store-activate", "--spool", spool, "--apply"}); err != nil {
		t.Fatalf("spool activation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(spool, "records.activated")); err != nil {
		t.Fatal("activation marker missing")
	}
	if _, err := os.Lstat(filepath.Join(spool, "substrate-backup", "pending", "job-c-1.json")); err != nil {
		t.Fatal("retired record missing")
	}
}

// The broker store lives on Docker volumes, so --broker orchestrates the
// whole stop -> migrate -> start cycle through the service image itself:
// the broker binary's own store-migrate/store-activate does the work.
func TestStoreMigrateCLIForBroker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space")
	// Infra owner: the credential-broker is a declared shared service whose
	// Docker volumes the broker store migration orchestrates.
	writeManagedSpaceDocs(t, dir, "owner", managedInfraDocs("owner", false))
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	dockerName := "docker"
	dockerScript := "#!/bin/sh\nprintf 'docker %s\n' \"$*\" >> \"$HUB_TEST_LOG\"\ncase \"$*\" in *ps*credential-broker*-a*format*json*) printf '{\"Service\":\"credential-broker\",\"Image\":\"broker-live:local\"}';; *config*format*json*) printf '{\"services\":{\"credential-broker\":{\"image\":\"broker-img:test\"}}}';; esac\n"
	if runtime.GOOS == "windows" {
		dockerName = "docker.cmd"
		dockerScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo docker %*\r\necho %* | findstr /C:\"ps credential-broker -a\" | findstr /C:\"format json\" >nul && echo {\"Service\":\"credential-broker\",\"Image\":\"broker-live:local\"}\r\necho %* | findstr /C:\"ps credential-broker -a\" | findstr /C:\"format json\" >nul && exit /b 0\r\necho %* | findstr /C:\"format json\" >nul && echo {\"services\":{\"credential-broker\":{\"image\":\"broker-img:test\"}}}\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, dockerName), []byte(dockerScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(t.Context(), []string{"store-migrate", "--dir", dir, "--root", "../..", "--env", "dev", "--broker"}); err == nil {
		t.Fatal("store-migrate without --apply must refuse")
	}
	if err := run(t.Context(), []string{"store-migrate", "--dir", dir, "--root", "../..", "--env", "dev", "--broker", "--apply"}); err != nil {
		t.Fatalf("broker migrate: %v", err)
	}
	if err := run(t.Context(), []string{"store-activate", "--dir", dir, "--root", "../..", "--env", "dev", "--broker", "--apply"}); err != nil {
		t.Fatalf("broker activate: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{
		"stop credential-broker",
		"hermes-credential-broker-dev:/var/lib/credential-broker",
		"hermes-credential-broker-recovery-dev:/var/lib/credential-broker-recovery",
		"hermes-credential-broker-recovery-key-dev:/var/lib/credential-broker-recovery-key",
		"--entrypoint credential-broker",
		"broker-live:local",
		"store-migrate --config /var/lib/credential-broker/config.json",
		"store-activate --config /var/lib/credential-broker/config.json",
		"start credential-broker",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %s in %s", want, calls)
		}
	}
}
