package main

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/serviceauth"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDevRebuildRefreshesSiblingControlToken(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	owner, member := filepath.Join(spaces, "owner"), filepath.Join(spaces, "member")
	writeManagedSpaceDocs(t, owner, "owner", managedInfraDocs("owner", false))
	writeManagedSpaceDocs(t, member, "member", nil)
	oldToken, newToken := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, token := range []string{oldToken, newToken} {
		if err := os.WriteFile(filepath.Join(member, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := refreshDevSiblingTokens(member); err != nil {
			t.Fatal(err)
		}
		entries, err := serviceauth.ReadTokenEnvelopes(filepath.Join(owner, "toolhub-tokens.json"))
		if err != nil || entries[token].PrincipalID != "member" {
			t.Fatalf("sibling control token not refreshed: %v", err)
		}
		if token == newToken {
			if _, stale := entries[oldToken]; stale {
				t.Fatal("retired sibling control token survived refresh")
			}
		}
	}
}

func TestDevCatalogStagesOnlyPinnedRegularFiles(t *testing.T) {
	root, build := t.TempDir(), t.TempDir()
	source := filepath.Join(root, "platform", "catalogs", catalog.Release)
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{catalog.ManifestFile, catalog.CapabilitiesFile, catalog.ToolsetsFile, catalog.ServicesFile, catalog.RightsFile} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := stageDevCatalog(root, build); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{catalog.ManifestFile, catalog.CapabilitiesFile, catalog.ToolsetsFile, catalog.ServicesFile, catalog.RightsFile} {
		body, err := os.ReadFile(filepath.Join(build, "catalogs", catalog.Release, name))
		if err != nil || string(body) != name {
			t.Fatalf("staged %s: %q %v", name, body, err)
		}
	}
	if err := os.Remove(filepath.Join(source, catalog.RightsFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(source, catalog.ManifestFile), filepath.Join(source, catalog.RightsFile)); err == nil {
		if err := stageDevCatalog(root, t.TempDir()); err == nil {
			t.Fatal("symlink catalog accepted")
		}
	}
}

func TestDevRebuildServiceBinaryMap(t *testing.T) {
	for service, binary := range map[string]string{
		"communication-hub":   "communication-hub",
		"credential-broker":   "credential-broker",
		"hub-media":           "hub-media",
		"toolhub":             "toolhub",
		"workload-controller": "hubctl",
		"supervisor":          "hubctl",
		"hermes-runtime":      "hub-runtime",
		"model-relay":         "hub-runtime",
		"toolhub-relay":       "hub-runtime",
		"egress-relay":        "hub-runtime",
	} {
		spec, ok := devService(service)
		if !ok || spec.binary != binary {
			t.Fatalf("%s: %+v, %v", service, spec, ok)
		}
	}
}

func TestDevRebuildRejectsProdAndUnsupportedBeforeDocker(t *testing.T) {
	for _, args := range [][]string{
		{"--env", "prod", "communication-hub"},
		{"--env", "dev", "cliproxy"},
		{"--env", "dev", "../communication-hub"},
	} {
		if err := runDevRebuild(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestDevServiceRoleBases(t *testing.T) {
	// SPEC-0082 R3: dev overlays build onto the service's role image —
	// daemons onto -dev-core, Docker consumers onto -dev-control, the agent
	// onto the runtime image.
	for name, want := range map[string]string{
		"communication-hub":   "hermes-hub:0.5.0-dev-core",
		"credential-broker":   "hermes-hub:0.5.0-dev-core",
		"hub-media":           "hermes-hub:0.5.0-dev-core",
		"toolhub":             "hermes-hub:0.5.0-dev-control",
		"workload-controller": "hermes-hub:0.5.0-dev-control",
		"supervisor":          "hermes-hub:0.5.0-dev-control",
		"hermes-runtime":      "hermes-hub:0.5.0-dev",
		"model-relay":         "hermes-hub:0.5.0-dev",
		"toolhub-relay":       "hermes-hub:0.5.0-dev",
		"egress-relay":        "hermes-hub:0.5.0-dev-core",
	} {
		spec, ok := devService(name)
		if !ok {
			t.Fatalf("devService(%s) not mapped", name)
		}
		if spec.base != want {
			t.Fatalf("devService(%s).base = %q, want %q", name, spec.base, want)
		}
	}
}

// ADR-0087: credential-broker links SQLCipher through CGO, so dev-rebuild must
// compile it inside the pinned toolchain image, not on the host with
// CGO_ENABLED=0 — a stub binary would silently serve no encrypted store.
func TestDevRebuildBrokerBuildsInSqlcipherToolchain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space")
	// Infra owner with a telegram channel: the shared gateway renders, which
	// is what the dev-rebuild assertions below inspect.
	writeManagedSpaceDocs(t, dir, "owner", managedInfraDocs("owner", true))
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=fake-for-test\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	dockerName, goName := "docker", "go"
	dockerScript := "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\ncase \"$*\" in *Architecture*) echo amd64;; *.Id*) echo sha256:old;; esac\n"
	goScript := "#!/bin/sh\nprintf 'go %s GOOS=%s GOARCH=%s CGO_ENABLED=%s\\n' \"$*\" \"$GOOS\" \"$GOARCH\" \"$CGO_ENABLED\" >> \"$HUB_TEST_LOG\"\nexit 0\n"
	if runtime.GOOS == "windows" {
		dockerName, goName = "docker.cmd", "go.cmd"
		dockerScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo docker %*\r\necho %* | findstr /C:\"Architecture\" >nul && echo amd64\r\necho %* | findstr /C:\".Id\" >nul && echo sha256:old\r\nexit /b 0\r\n"
		goScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo go %* GOOS=%GOOS% GOARCH=%GOARCH% CGO_ENABLED=%CGO_ENABLED%\r\nexit /b 0\r\n"
	}
	for name, script := range map[string]string{dockerName: dockerScript, goName: goScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "credential-broker"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{"--target build", "deploy" + string(filepath.Separator) + "Dockerfile", "libsqlite3", "/src/services/credential-broker", "./cmd/credential-broker"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %s in %s", want, calls)
		}
	}
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "go build") && strings.Contains(line, "credential-broker") {
			t.Fatalf("broker compiled on host without CGO toolchain: %s", line)
		}
	}
}

func TestDevRebuildCompilesAndStartsOnlySelectedService(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space")
	// Infra owner with telegram: hub-media is never declared, so asking
	// dev-rebuild for it must be refused, not silently skipped.
	ownerDocs := managedInfraDocs("owner", true)
	ownerDocs["stack"] = strings.Replace(ownerDocs["stack"], "  supervisor: {mode: shared}\n", "  supervisor: {mode: shared}\n  workload-controller: {mode: shared}\n", 1)
	writeManagedSpaceDocs(t, dir, "owner", ownerDocs)
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=fake-for-test\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// In-project supervisor: the declared shared supervisor renders only
	// under this exact supervisor-mode selection.
	if err := os.WriteFile(filepath.Join(dir, stack.ExecutionPath("dev")), []byte(`{"schema":1,"user":"owner","environment":"dev","mode":"supervisor","supervisor_url":"http://supervisor:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	dockerName, goName := "docker", "go"
	dockerScript := "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\n[ \"$HUB_FAIL_DOCKER\" = 1 ] && [ \"$1\" = build ] && exit 9\n[ \"$HUB_FAIL_UP\" = 1 ] && [ \"$1\" = compose ] && echo \"$*\" | grep -q ' up ' && exit 9\ncase \"$*\" in *Architecture*) echo amd64;; *.Id*) echo sha256:old;; esac\n"
	goScript := "#!/bin/sh\nprintf 'go %s GOOS=%s GOARCH=%s CGO_ENABLED=%s\\n' \"$*\" \"$GOOS\" \"$GOARCH\" \"$CGO_ENABLED\" >> \"$HUB_TEST_LOG\"\n[ \"$HUB_FAIL_GO\" = 1 ] && exit 9\nexit 0\n"
	if runtime.GOOS == "windows" {
		dockerName, goName = "docker.cmd", "go.cmd"
		dockerScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo docker %*\r\nif \"%HUB_FAIL_DOCKER%\"==\"1\" if \"%~1\"==\"build\" exit /b 9\r\nif \"%HUB_FAIL_UP%\"==\"1\" echo %* | findstr /C:\" up \" >nul && exit /b 9\r\necho %* | findstr /C:\"Architecture\" >nul && echo amd64\r\necho %* | findstr /C:\".Id\" >nul && echo sha256:old\r\nexit /b 0\r\n"
		goScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo go %* GOOS=%GOOS% GOARCH=%GOARCH% CGO_ENABLED=%CGO_ENABLED%\r\nif \"%HUB_FAIL_GO%\"==\"1\" exit /b 9\r\nexit /b 0\r\n"
	}
	for name, script := range map[string]string{dockerName: dockerScript, goName: goScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"dev-rebuild", "--dir", dir, "--root", "../..", "--env", "dev", "communication-hub"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{"config --quiet", "go build", "GOOS=linux GOARCH=amd64 CGO_ENABLED=0", "Dockerfile.dev-overlay", "hermes-hub-dev-owner-communication-hub:local", "--no-build --no-deps communication-hub"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %s in %s", want, calls)
		}
	}
	if strings.Contains(calls, "cli-tools") || strings.Contains(calls, "cell-proxy") || strings.Contains(calls, "image prune") || strings.Contains(calls, "buildx prune") {
		t.Fatalf("unrelated images rebuilt: %s", calls)
	}
	for _, tc := range []struct{ service, source, base string }{
		{"communication-hub", "./cmd/communication", "hermes-hub:0.5.0-dev-core"},
		{"credential-broker", "./cmd/credential-broker", "hermes-hub:0.5.0-dev-core"},
		{"toolhub", "./cmd/toolhub", "hermes-hub:0.5.0-dev-control"},
		{"workload-controller", "./cmd/hubctl", "hermes-hub:0.5.0-dev-control"},
		{"hermes-runtime", "./cmd/runtime", "hermes-hub:0.5.0-dev"},
	} {
		before, _ := os.ReadFile(log)
		if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", tc.service}); err != nil {
			t.Fatal(tc.service, err)
		}
		all, _ := os.ReadFile(log)
		if tc.service == "hermes-runtime" {
			if !strings.Contains(string(all[len(before):]), "./cmd/hubctl") {
				t.Error("runtime overlay must rebuild its tools executor hubctl alongside hub-runtime")
			}
			// Supervised runtimes never compose-up: the rebuilt tag is
			// pinned for the next spawn instead.
			stamped, serr := os.ReadFile(filepath.Join(dir, "generated", "dev-runtime-image"))
			if serr != nil || strings.TrimSpace(string(stamped)) != "hermes-hub-dev-owner-hermes-runtime:local" {
				t.Fatalf("supervised runtime image not pinned: %q %v", stamped, serr)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(dir, "generated", "dev-runtime-image"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0644 {
					t.Fatalf("supervisor cannot read runtime image pin: mode=%v", info.Mode().Perm())
				}
			}
			if strings.Contains(string(all[len(before):]), "--no-deps hermes-runtime") {
				t.Fatalf("supervised runtime must not compose-up: %s", all)
			}
			continue
		}
		for _, want := range []string{tc.source, "BASE=" + tc.base, "--no-build --no-deps " + tc.service} {
			if !strings.Contains(string(all), want) {
				t.Fatalf("%s missing %s in %s", tc.service, want, all)
			}
		}
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "hub-media"}); err == nil {
		t.Fatal("built disabled hub-media service")
	}
	selection, _ := json.Marshal(stack.ExecutionSelection{Schema: 1, User: "owner", Environment: "dev", Mode: "supervisor", SupervisorURL: "http://supervisor:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"})
	if err := os.WriteFile(filepath.Join(dir, stack.ExecutionPath("dev")), selection, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "hermes-runtime"}); err != nil {
		t.Fatal(err)
	}
	selected, err := os.ReadFile(filepath.Join(dir, "generated", "dev-runtime-image"))
	if err != nil || strings.TrimSpace(string(selected)) != "hermes-hub-dev-owner-hermes-runtime:local" {
		t.Fatalf("supervisor runtime image selection: %q %v", selected, err)
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err != nil {
		t.Fatal(err)
	}
	scriptSelected, err := os.ReadFile(filepath.Join(dir, "generated", "dev-script-image"))
	if err != nil || strings.TrimSpace(string(scriptSelected)) != "hermes-hub-dev-owner-communication-hub:local" {
		t.Fatalf("script image selection: %q %v", scriptSelected, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "generated", "dev-script-image"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0644 {
			t.Fatalf("supervisor cannot read script image pin: mode=%v", info.Mode().Perm())
		}
	}
	if err := run(context.Background(), []string{"build", "--dir", dir, "--root", "../..", "--env", "dev", "cliproxy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "dev-runtime-image")); !os.IsNotExist(err) {
		t.Fatal("full core build retained stale supervised dev image selection")
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "dev-script-image")); !os.IsNotExist(err) {
		t.Fatal("full core build retained stale script image selection")
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "dev-runtime-image"), selected, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"build", "--dir", dir, "--root", "../..", "--env", "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "dev-runtime-image")); !os.IsNotExist(err) {
		t.Fatal("unscoped full build retained stale supervised dev image selection")
	}
	before, _ := os.ReadFile(log)
	t.Setenv("HUB_FAIL_GO", "1")
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err == nil {
		t.Fatal("accepted failed Go compile")
	}
	after, _ := os.ReadFile(log)
	if strings.Count(string(after), "docker build ") != strings.Count(string(before), "docker build ") {
		t.Fatal("Docker built after failed compilation")
	}
	t.Setenv("HUB_FAIL_GO", "")
	t.Setenv("HUB_FAIL_DOCKER", "1")
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err == nil {
		t.Fatal("accepted failed Docker build")
	}
	afterDocker, _ := os.ReadFile(log)
	if strings.Contains(string(afterDocker[len(after):]), "prune") {
		t.Fatal("failed scoped Docker build pruned the shared cache")
	}
	t.Setenv("HUB_FAIL_DOCKER", "")
	t.Setenv("HUB_FAIL_UP", "1")
	beforeFailedUp, _ := os.ReadFile(log)
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err == nil {
		t.Fatal("accepted failed container start")
	}
	afterFailedUp, _ := os.ReadFile(log)
	newCalls := string(afterFailedUp[len(beforeFailedUp):])
	if !strings.Contains(newCalls, "docker tag sha256:old hermes-hub-dev-owner-communication-hub:local") || strings.Count(newCalls, "--no-build --no-deps communication-hub") != 2 {
		t.Fatalf("failed start did not restore previous image and retry service: %s", newCalls)
	}
}
