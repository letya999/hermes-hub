package devcheck

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestImageBudgetRejectsForbiddenBinary(t *testing.T) {
	report := "binary /usr/bin/chromium\ntree /usr/lib/chromium\n"
	if err := imageBudgetVerdict("core", 100_000_000, report); err == nil || !strings.Contains(err.Error(), "chromium") {
		t.Fatalf("core image with chromium must fail: %v", err)
	}
	// A renamed binary is still caught through its resource tree.
	if err := imageBudgetVerdict("control", 100_000_000, "tree /usr/local/lib/node_modules"); err == nil {
		t.Fatal("control image with a node tree must fail")
	}
	if err := imageBudgetVerdict("core", 100_000_000, "binary /usr/bin/python3.13"); err == nil {
		t.Fatal("core image with python must fail")
	}
	// The same files are legal in the runtime role (they are its job).
	if err := imageBudgetVerdict("runtime", 1_000_000_000, report+"binary /usr/bin/node\ntree /opt/hermes/.venv\n"); err != nil {
		t.Fatalf("runtime image may carry its toolchain: %v", err)
	}
}

func TestImageBudgetRejectsOversize(t *testing.T) {
	if err := imageBudgetVerdict("core", 300_000_000, ""); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over-budget core image must fail: %v", err)
	}
	if err := imageBudgetVerdict("runtime", 1_400_000_000, ""); err == nil {
		t.Fatal("over-budget runtime image must fail")
	}
	if err := imageBudgetVerdict("core", 200_000_000, ""); err != nil {
		t.Fatalf("in-budget image must pass: %v", err)
	}
}

func TestImageBudgetRejectsSetuid(t *testing.T) {
	if err := imageBudgetVerdict("browser", 500_000_000, "setuid /usr/bin/su"); err == nil {
		t.Fatal("setuid binary must fail on the browser role")
	}
	if err := imageBudgetVerdict("runtime", 500_000_000, "setuid /bin/mount"); err == nil {
		t.Fatal("setuid binary must fail on the runtime role too")
	}
}

func TestImageBudgetMissingImageFails(t *testing.T) {
	missing := func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, errors.New("No such image")
	}
	if err := imageBudget(context.Background(), "hermes-hub:nope", "core", missing); err == nil {
		t.Fatal("missing image must fail closed")
	}
	badSize := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "image" {
			return []byte("not-a-number"), nil
		}
		return nil, errors.New("unreachable")
	}
	if err := imageBudget(context.Background(), "hermes-hub:test", "core", badSize); err == nil {
		t.Fatal("unparseable size must fail closed")
	}
}

func TestImageBudgetRunsProbeAndAppliesVerdict(t *testing.T) {
	var sawProbe bool
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			sawProbe = true
			return []byte("tree /usr/lib/chromium\n"), nil
		}
		return []byte("100000000"), nil
	}
	if err := imageBudget(context.Background(), "hermes-hub:test", "core", runner); err == nil {
		t.Fatal("probe findings must fail the gate")
	}
	if !sawProbe {
		t.Fatal("gate skipped the in-image probe")
	}
	probeErr := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			return nil, errors.New("container refused")
		}
		return []byte("100000000"), nil
	}
	if err := imageBudget(context.Background(), "hermes-hub:test", "core", probeErr); err == nil {
		t.Fatal("a failed probe must fail closed")
	}
}

func TestImageBudgetProbeCoversForbiddenSurface(t *testing.T) {
	// The probe is the gate's anti-rename defense: it must watch the resource
	// trees that outlive a renamed binary, the binary names themselves, and
	// setuid bits. A probe that only greps for "chromium" is trivially evaded.
	for _, tree := range []string{"/usr/lib/chromium", "/opt/hermes/.venv", "/usr/lib/python3", "/usr/local/lib/node_modules"} {
		if !strings.Contains(budgetProbe, tree) {
			t.Fatalf("probe does not watch %s — a renamed install would pass", tree)
		}
	}
	for _, name := range []string{"chromium", "node", "python3", "uv", "glab"} {
		if !strings.Contains(budgetProbe, name) {
			t.Fatalf("probe does not match binary name %s", name)
		}
	}
	if !strings.Contains(budgetProbe, "-perm -4000") {
		t.Fatal("probe does not scan for setuid binaries")
	}
}

func TestImageBudgetFeedsSizeToVerdict(t *testing.T) {
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			return []byte(""), nil
		}
		return []byte("400000000"), nil // 400 MB: fine for runtime, over for core
	}
	if err := imageBudget(context.Background(), "hermes-hub:test", "core", runner); err == nil {
		t.Fatal("oversize image passed the gate — inspected size is not enforced")
	}
	if err := imageBudget(context.Background(), "hermes-hub:test", "runtime", runner); err != nil {
		t.Fatalf("in-budget runtime image must pass: %v", err)
	}
}

func TestImageBudgetEveryRoleHasBudget(t *testing.T) {
	// SPEC-0082 budgets all four roles; an unbudgeted role must not silently
	// pass, and every budgeted role enforces its own ceiling.
	for role, budget := range map[string]int64{
		"core": 250_000_000, "control": 350_000_000, "runtime": 1_300_000_000, "browser": 1_000_000_000,
	} {
		if err := imageBudgetVerdict(role, budget-1, ""); err != nil {
			t.Fatalf("%s image just under budget must pass: %v", role, err)
		}
		if err := imageBudgetVerdict(role, budget+1, ""); err == nil {
			t.Fatalf("%s image just over budget must fail", role)
		}
	}
	if err := imageBudgetVerdict("sidecar", 1, ""); err == nil {
		t.Fatal("unknown role must fail closed")
	}
}

func TestImageJobRunsBudgetGate(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				If  string `yaml:"if"`
				Run string `yaml:"run"`
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range workflow.Jobs["image"].Steps {
		script += step.Run + "\n"
	}
	// Every published role image must pass its SPEC-0082 budget gate before
	// the publish can be called green; core/control/runtime/browser each get
	// their own budgeted invocation.
	for _, role := range []string{"core", "control", "runtime", "browser"} {
		if !strings.Contains(script, "devcheck image-budget") || !strings.Contains(script, " "+role) {
			t.Errorf("image job does not gate the %s role", role)
		}
	}
}
