package devcheck

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// restrictedRoles get the forbidden-artifact list enforced (SPEC-0082 R0.2).
var restrictedRoles = map[string]bool{"core": true, "control": true}

// imageRoleBudgets are SPEC-0082 uncompressed size budgets in bytes; docker
// reports decimal GB so the budgets are decimal too — the tighter reading.
var imageRoleBudgets = map[string]int64{
	"core":    250_000_000,
	"control": 350_000_000,
	"runtime": 1_300_000_000,
	"browser": 1_000_000_000,
}

// ImageBudget is the SPEC-0082 R0 gate: image size against the role budget
// plus the in-image forbidden-artifact/setuid probe.
func ImageBudget(ctx context.Context, image, role string) error {
	return imageBudget(ctx, image, role, cliDocker)
}

// imageBudget inspects the image size; a missing or uninspectable image fails
// closed (SPEC-0082 R0.3).
func imageBudget(ctx context.Context, image, role string, run dockerRunner) error {
	out, err := run(ctx, "image", "inspect", "-f", "{{.Size}}", image)
	if err != nil {
		return fmt.Errorf("image-budget %s: image %s not found or uninspectable", image, image)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return fmt.Errorf("image-budget %s: image %s reported invalid size %q", image, image, strings.TrimSpace(string(out)))
	}
	report, err := run(ctx, "run", "--rm", "--entrypoint", "/bin/sh", image, "-c", budgetProbe)
	if err != nil {
		return fmt.Errorf("image-budget %s: probe failed: %w", image, err)
	}
	return imageBudgetVerdict(role, size, string(report))
}

// budgetProbe runs inside the candidate image and prints "kind path" records
// for suspicious entries; the host side turns them into violations. The
// resource trees cannot be renamed away even when the binary is.
const budgetProbe = `
for d in /usr/lib/chromium /opt/hermes/.venv /usr/lib/python3 /usr/local/lib/node_modules; do
  [ -e "$d" ] && echo "tree $d"
done
for d in /usr/bin /usr/local/bin /bin /sbin /usr/sbin /opt; do
  [ -d "$d" ] && find "$d" \( -type f -o -type l \) 2>/dev/null
done | while IFS= read -r f; do
  base="${f##*/}"
  case "$base" in
    chromium|chromium-*|node|nodejs|python|python3|python3.*|uv|uvx|glab) echo "binary $f";;
  esac
done
find / -xdev -type f -perm -4000 2>/dev/null | while IFS= read -r f; do echo "setuid $f"; done
`

// imageBudgetVerdict evaluates a probe report ("kind path" lines, emitted by
// the in-image probe script) against the role's SPEC-0082 contract.
func imageBudgetVerdict(role string, sizeBytes int64, report string) error {
	budget, ok := imageRoleBudgets[role]
	if !ok {
		return fmt.Errorf("image-budget: unknown role %q (want core, control, runtime or browser)", role)
	}
	var problems []string
	if sizeBytes > budget {
		problems = append(problems, fmt.Sprintf("size %d exceeds %s budget %d", sizeBytes, role, budget))
	}
	for _, line := range strings.Split(report, "\n") {
		kind, path, _ := strings.Cut(line, " ")
		if path == "" {
			continue
		}
		if kind == "setuid" {
			problems = append(problems, "setuid binary "+path)
		}
		if (kind == "binary" || kind == "tree") && restrictedRoles[role] {
			problems = append(problems, "forbidden "+kind+" "+path)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("image-budget %s violations: %s", role, strings.Join(problems, "; "))
	}
	return nil
}
