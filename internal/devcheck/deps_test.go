package devcheck

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestContractDependenciesCannotBeSkipped(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/hub\n\ngo 1.27.4\n\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ./dependency\n")
	write("dependency/go.mod", "module example.test/dependency\n\ngo 1.27.4\n")
	write("dependency/dep.go", "package dependency\n")
	write("cmd/runtime/main.go", "package main\nfunc main() {}\n")
	write("contracts/task/v1/task.go", "package task\nimport _ \"example.test/dependency\"\n")
	graph, err := depGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	if problems := strings.Join(checkDeps(graph, nil), "\n"); !strings.Contains(problems, "forbidden: contracts/task/v1 -> example.test/dependency") {
		t.Errorf("third-party contract import escaped the gate: %s", problems)
	}
	write("contracts/task/v1/task.go", "package task\nimport _ \"example.test/hub/missing\"\n")
	if _, err := depGraph(root); err == nil {
		t.Error("contract graph failure was hidden by a successful command graph")
	}
}

func TestTransitiveForbiddenEdgeReported(t *testing.T) {
	graph := map[string][]string{
		"cmd/runtime":                 {"internal/runtime"},
		"internal/runtime":            {"internal/helper"},
		"internal/helper":             {"internal/toolhub"},
		"internal/toolhub":            nil,
		"contracts/task/v1":           {"internal/identity", "internal/memory"},
		"internal/identity":           nil,
		"internal/memory":             nil,
		"cmd/hubctl":                  {"internal/toolhub"},
		"cmd/communication":           {"internal/toolhub/automation"},
		"internal/toolhub/automation": nil,
	}
	baseline := map[string]bool{
		"cmd/runtime internal/runtime": true, "cmd/runtime internal/helper": true, "cmd/runtime internal/toolhub": true,
		"cmd/hubctl internal/toolhub": true, "cmd/communication internal/toolhub/automation": true,
		"cmd/hubctl internal/gone": true,
	}
	problems := checkDeps(graph, baseline)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{
		"forbidden: cmd/runtime -> internal/toolhub via cmd/runtime > internal/runtime > internal/helper > internal/toolhub",
		"forbidden: cmd/communication -> internal/toolhub/automation",
		"forbidden: contracts/task/v1 -> internal/memory",
		"stale baseline entry: cmd/hubctl internal/gone",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "cmd/hubctl -> internal/toolhub") || strings.Contains(joined, "internal/identity") {
		t.Errorf("allowed edge reported:\n%s", joined)
	}
}

func TestNewEdgeOutsideBaselineRejected(t *testing.T) {
	graph := map[string][]string{"cmd/hubctl": {"internal/stack"}, "internal/stack": nil}
	problems := checkDeps(graph, map[string]bool{})
	if !slices.Contains(problems, "new dependency: cmd/hubctl internal/stack (shrink-only baseline internal/devcheck/deps-baseline.txt)") {
		t.Fatalf("got %v", problems)
	}
}

// TestModuleImportAllowlist runs the real gate on this repository.
func TestModuleImportAllowlist(t *testing.T) {
	if err := Deps("../.."); err != nil {
		t.Fatal(err)
	}
}

// TestRuntimeDoesNotLinkToolHub pins SPEC-0100 R1.3 on the real closure.
func TestRuntimeDoesNotLinkToolHub(t *testing.T) {
	graph, err := depGraph("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"cmd/runtime", "cmd/supervisor", "cmd/communication"} {
		for pkg := range closure(graph, bin) {
			if pkg == "internal/toolhub" || pkg == "internal/toolhub/automation" {
				t.Errorf("%s links %s", bin, pkg)
			}
		}
	}
}
