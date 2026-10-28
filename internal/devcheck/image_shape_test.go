package devcheck

import (
	"os"
	"strings"
	"testing"
)

// dockerfileStage returns the text of one FROM...AS stage, header included.
func dockerfileStage(t *testing.T, stage string) string {
	t.Helper()
	data, err := os.ReadFile("../../docker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	marker := "AS " + stage + "\n"
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("stage %q missing from docker/Dockerfile", stage)
	}
	start = strings.LastIndex(source[:start], "FROM ")
	chunk := source[start:]
	if end := strings.Index(chunk, "\nFROM "); end >= 0 {
		chunk = chunk[:end]
	}
	return chunk
}

func TestCoreImageContents(t *testing.T) {
	core := dockerfileStage(t, "core")
	// SPEC-0082 D1: core is the lean shared base — Go daemons plus TLS roots,
	// nothing an agent shell could interpret.
	for _, bin := range []string{"hubctl", "hub-runtime", "toolhub", "communication-hub", "credential-broker", "hub-media", "cellinit", "cli-proxy-api"} {
		if !strings.Contains(core, bin) {
			t.Errorf("core image missing %s", bin)
		}
	}
	if !strings.Contains(core, "ca-certificates") {
		t.Error("core image missing CA roots — outbound TLS would fail")
	}
	for _, banned := range []string{"python", "node_modules", "/usr/local/bin/node", "chromium", "/opt/hermes/.venv", "uv"} {
		if strings.Contains(core, banned) {
			t.Errorf("core image carries forbidden artifact %q", banned)
		}
	}
	if !strings.Contains(core, "debian:") {
		t.Error("core must derive from the Debian slim base, not the Python base")
	}
	// Compose builds cliproxy against <env>-core and CI publishes the same
	// targets, so they must resolve just like prod/dev and prod/dev-control.
	source, err := os.ReadFile("../../docker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"prod-core", "dev-core"} {
		if !strings.Contains(string(source), "AS "+stage) {
			t.Errorf("stage %q missing — compose/CI build target would not resolve", stage)
		}
	}
}

func TestHermesRevFileVerified(t *testing.T) {
	build := dockerfileStage(t, "python_build")
	// R1.3: the pinned checkout records its revision before .git is pruned, and
	// the runtime image carries the file the contract check reads back.
	if !strings.Contains(build, ".hermes-rev") || !strings.Contains(build, "rm -rf .git") {
		t.Fatal("python_build must write /opt/hermes/.hermes-rev and remove .git")
	}
	contract, err := os.ReadFile("hermes_contract_integration.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contract), ".hermes-rev") {
		t.Fatal("contract check still reads the pruned .git — switch to .hermes-rev")
	}
	if strings.Contains(string(contract), "rev-parse") {
		t.Fatal("contract check still shells out to git rev-parse inside /opt/hermes")
	}
}

func TestImageHasBytecode(t *testing.T) {
	build := dockerfileStage(t, "python_build")
	if !strings.Contains(build, "UV_COMPILE_BYTECODE=1") {
		t.Fatal("python_build must pre-compile the venv (UV_COMPILE_BYTECODE=1)")
	}
	if !strings.Contains(build, "uv sync") {
		t.Fatal("python_build must own the uv sync step")
	}
	runtime := dockerfileStage(t, "runtime")
	// The runtime image consumes the finished venv; a second sync would pay
	// the download cost again and could write bytecode on the RO rootfs.
	if strings.Contains(runtime, "uv sync") || strings.Contains(runtime, "uv pip install") {
		t.Fatal("runtime must copy the built venv, not re-run uv")
	}
	if !strings.Contains(runtime, "from=python_build") {
		t.Fatal("runtime must COPY the python_build checkout")
	}
}

func TestEditableImportsIntact(t *testing.T) {
	build := dockerfileStage(t, "python_build")
	// The upstream venv installs hermes-agent editable: its .pth maps modules
	// (agent/, hermes_* and friends) straight into /opt/hermes. Pruning may
	// only delete leaf content upstream never imports — removing a mapped
	// package silently breaks every hermes op in the shipped image.
	requiredRemovals := []string{
		".git", "tests", "apps", "website", "ui-tui", "web", "evals", "nix",
		"docs", ".github", "tests-js", "mcp-research-data",
		"datagen-config-examples", "docker", "package-lock.json",
	}
	for _, entry := range requiredRemovals {
		if !strings.Contains(build, " "+entry) {
			t.Errorf("prune does not remove %s — dead upstream weight stays", entry)
		}
	}
	for _, keep := range []string{"agent", "optional-skills", "plugins", "skills", "compat_manifest.json", "pyproject.toml", "uv.lock", "run_agent.py"} {
		if strings.Contains(build, " "+keep) {
			t.Errorf("prune deletes %s — the editable mapping needs it", keep)
		}
	}
}

func TestNoLibnodeInImage(t *testing.T) {
	runtime := dockerfileStage(t, "runtime")
	// Debian `novnc` hard-depends nodejs+libnode115 for a launcher the hub never
	// calls; noVNC arrives as pinned static files in the browser sidecar.
	for _, banned := range []string{" novnc", "nodejs", "libnode", "espeak"} {
		if strings.Contains(runtime, banned) {
			t.Errorf("runtime image still installs %q", banned)
		}
	}
}

func TestE2EBinaryOnlyInDevImage(t *testing.T) {
	// The e2e harness is a developer tool; shipping it in prod hands every
	// agent container an extra exec surface for nothing.
	for _, stage := range []string{"runtime", "core", "prod", "prod-control"} {
		if strings.Contains(dockerfileStage(t, stage), "/e2e") {
			t.Errorf("%s image carries the e2e harness", stage)
		}
	}
	if !strings.Contains(dockerfileStage(t, "dev"), "/e2e") {
		t.Error("dev image lost the e2e harness")
	}
}

func TestSttRequirementsHashed(t *testing.T) {
	// SPEC-0082 R2.3: Python installs in the voice images are hash-pinned, same
	// supply-chain posture as the runtime aiohttp layer.
	for _, pair := range [][2]string{
		{"docker/Dockerfile.stt", "stt-requirements.txt"},
		{"docker/Dockerfile.tts", "tts-requirements.txt"},
	} {
		data, err := os.ReadFile("../../" + pair[0])
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if !strings.Contains(body, "--require-hashes") || !strings.Contains(body, pair[1]) {
			t.Fatalf("%s must pip install --require-hashes -r %s", pair[0], pair[1])
		}
		reqs, err := os.ReadFile("../../docker/" + pair[1])
		if err != nil {
			t.Fatalf("%s missing: %v", pair[1], err)
		}
		if strings.Count(string(reqs), "--hash=sha256:") < 2 {
			t.Fatalf("%s is not a hashed lockfile", pair[1])
		}
	}
}
