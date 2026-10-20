package devcheck

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type coverBlock struct {
	statements int
	covered    bool
	pkg        string
}

func parseCoverage(name string) (map[string]coverBlock, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() || !regexp.MustCompile(`^mode: (set|count|atomic)$`).MatchString(s.Text()) {
		return nil, fmt.Errorf("invalid coverage header")
	}
	blocks := map[string]coverBlock{}
	trailingBlank := false
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == "" {
			trailingBlank = true
			continue
		}
		if trailingBlank {
			return nil, fmt.Errorf("invalid coverage row after blank line")
		}
		fields := strings.Fields(s.Text())
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid coverage row %q", s.Text())
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || statements < 0 || count < 0 {
			return nil, fmt.Errorf("invalid coverage row %q", s.Text())
		}
		merged := blocks[fields[0]]
		if merged.pkg == "" {
			file := fields[0][:strings.IndexByte(fields[0], ':')]
			if i := strings.LastIndexByte(file, '/'); i >= 0 {
				merged.pkg = file[:i]
			} else {
				merged.pkg = file
			}
		}
		if merged.statements == 0 {
			merged.statements = statements
		}
		merged.covered = merged.covered || count > 0
		blocks[fields[0]] = merged
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("empty coverage profile")
	}
	return blocks, nil
}

func Coverage(name string, minimum float64) (float64, error) {
	return CoverageGate(name, minimum, 0)
}

// mainModule returns the module path declared by the nearest go.mod walking
// up from the working directory; empty when none is found.
func mainModule() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return strings.TrimSpace(m)
				}
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// CoverageGate applies the global statement minimum plus an optional
// per-package floor. A merged -coverpkg=./... profile can hide an untested
// package behind coverage from another package's tests; the floor keeps each
// package honest. Only internal/ packages are gated: cmd/ binaries are
// exercised by the container contract gates, not unit tests.
func CoverageGate(name string, minimum, packageMinimum float64) (float64, error) {
	blocks, err := parseCoverage(name)
	if err != nil {
		return 0, err
	}
	var total, covered int
	type pkgStat struct{ total, covered int }
	pkgs := map[string]*pkgStat{}
	for _, b := range blocks {
		total += b.statements
		if b.covered {
			covered += b.statements
		}
		// Only this module's internal packages: replaced local modules
		// (credential-broker) run their own stricter coverage gate.
		if strings.HasPrefix(b.pkg, mainModule()+"/internal/") {
			p := pkgs[b.pkg]
			if p == nil {
				p = &pkgStat{}
				pkgs[b.pkg] = p
			}
			p.total += b.statements
			if b.covered {
				p.covered += b.statements
			}
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("empty coverage profile")
	}
	actual := float64(covered) * 100 / float64(total)
	if actual < minimum {
		return actual, fmt.Errorf("go coverage %.2f%% < %.2f%%", actual, minimum)
	}
	if packageMinimum > 0 {
		var low []string
		for pkg, p := range pkgs {
			if pct := float64(p.covered) * 100 / float64(p.total); pct < packageMinimum {
				low = append(low, fmt.Sprintf("%s %.2f%%", pkg, pct))
			}
		}
		if len(low) > 0 {
			sort.Strings(low)
			return actual, fmt.Errorf("package coverage below %.2f%%: %s", packageMinimum, strings.Join(low, ", "))
		}
	}
	return actual, nil
}

func CoverAll(profile string, minimum, packageMinimum float64, listPackages func() ([]string, error), testPackage func(pkg, out string) error) (float64, error) {
	packages, err := listPackages()
	if err != nil {
		return 0, err
	}
	out, err := os.Create(profile)
	if err != nil {
		return 0, err
	}
	if _, err := out.WriteString("mode: count\n"); err != nil {
		out.Close()
		return 0, err
	}
	tmp := profile + ".tmp"
	for _, pkg := range packages {
		if err := testPackage(pkg, tmp); err != nil {
			out.Close()
			return 0, err
		}
		body, err := os.ReadFile(tmp)
		if err != nil {
			out.Close()
			return 0, err
		}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "mode:") {
				continue
			}
			if _, err := out.WriteString(line + "\n"); err != nil {
				out.Close()
				return 0, err
			}
		}
	}
	_ = os.Remove(tmp)
	if err := out.Close(); err != nil {
		return 0, err
	}
	return CoverageGate(profile, minimum, packageMinimum)
}

func Project(root string) error {
	required := []string{"AGENTS.md", "README.md", "SETUP.md", "CONTRIBUTING.md", "SECURITY.md", "LICENSE", "docs/index.md", "specs/index.md", ".work/index.md"}
	var problems []string
	for _, name := range required {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil || !info.Mode().IsRegular() {
			problems = append(problems, "missing "+name)
		}
	}
	link := regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)
	for _, folder := range []string{"docs", "specs", ".work"} {
		err := filepath.WalkDir(filepath.Join(root, folder), func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(name) != ".md" {
				return nil
			}
			body, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, name)
			if bytes.Contains(body, []byte("{{")) {
				problems = append(problems, "template placeholder: "+rel)
			}
			if folder == "docs" && !bytes.HasPrefix(body, []byte("---\ndescription:")) && !bytes.HasPrefix(body, []byte("---\r\ndescription:")) {
				problems = append(problems, "missing metadata: "+rel)
			}
			for _, match := range link.FindAllSubmatch(body, -1) {
				target := string(match[1])
				if parsed, err := url.Parse(target); err != nil || parsed.IsAbs() || strings.HasPrefix(target, "#") {
					continue
				}
				target = strings.SplitN(target, "#", 2)[0]
				if _, err := os.Stat(filepath.Join(filepath.Dir(name), filepath.FromSlash(target))); err != nil {
					problems = append(problems, "broken link: "+rel+" -> "+target)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// CHG-0136: numbering uniqueness, index sync, freshness and size rules.
	indexBodies := map[string]string{}
	for _, idx := range []string{"docs/adr/index.md", "specs/index.md", ".work/index.md"} {
		if body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(idx))); err == nil {
			indexBodies[idx] = string(body)
		}
	}
	numRe := regexp.MustCompile(`^(ADR|SPEC|CHG)-\d{4}`)
	verifiedRe := regexp.MustCompile(`(?m)^last_verified:\s*"?(\d{4}-\d{2}-\d{2})`)
	seen := map[string]string{}
	shallowOut, _ := exec.Command("git", "-C", root, "rev-parse", "--is-shallow-repository").Output()
	fullHistory := strings.TrimSpace(string(shallowOut)) == "false"
	// Historical CHG number collisions kept for reference stability; new
	// collisions fail the gate.
	allowedCollisions := map[string]bool{
		"done/CHG-0015-durable-job-runtime-mappings": true,
		"done/CHG-0015-migration-map":                true,
		"done/CHG-0026-recipe-resolver":              true,
		"done/CHG-0026-user-binding-journey":         true,
		"done/CHG-0027-m53-e2e":                      true,
		"done/CHG-0027-recipe-catalog-discovery":     true,
		"done/CHG-0031-docker-disk-diagnosis":        true,
		"done/CHG-0031-docker-reclaim":               true,
		"done/CHG-0039-oauth-handoff":                true,
		"done/CHG-0039-parallel-context-workers":     true,
		"done/CHG-0047-mcp-admission-and-wake":       true,
		"done/CHG-0047-ssh-capability":               true,
		"done/CHG-0074-host-hygiene":                 true,
		"done/CHG-0074-telegram-auth-plugin":         true,
		"done/CHG-0105-broker-drift-self-heal":       true,
		"done/CHG-0105-m13-closeout":                 true,
		"done/CHG-0106-live-replay-defects":          true,
		"done/CHG-0106-m13-auth-mcpb":                true,
		"done/CHG-0127-m13-p0-connector-path":        true,
		"done/CHG-0127-watcher-failure-visibility":   true,
	}
	// Living docs whose verified date predates their last edit. Entries leave
	// this list only through a real content review in CHG-0136 phase 3 —
	// never by bumping the date alone.
	verifiedDriftAllowlist := map[string]bool{
		"docs/ai-stp-compatibility.md":      true,
		"docs/architecture.md":              true,
		"docs/capability-boundary-audit.md": true,
		"docs/channels.md":                  true,
		"docs/ci-latency.md":                true,
		"docs/cli-policy.md":                true,
		"docs/index.md":                     true,
		"docs/integrations.md":              true,
		"docs/local-accounts-manager.md":    true,
		"docs/m10-stabilization.md":         true,
		"docs/operations.md":                true,
		"docs/prepared-connectors.md":       true,
		"docs/routines.md":                  true,
		"docs/threat-model.md":              true,
		"docs/toolhive-vmcp-audit.md":       true,
		"docs/validation.md":                true,
	}
	// Legacy oversize entries allowed until CHG-0136 phase 3 splits them.
	oversizeAllowlist := map[string]bool{
		"docs/architecture.md":        true,
		"docs/integrations.md":        true,
		"docs/operations.md":          true,
		"docs/prepared-connectors.md": true,
		"docs/validation.md":          true,
	}
	checkFile := func(rel, idx string) {
		base := filepath.Base(rel)
		num := numRe.FindString(base)
		if num == "" {
			return
		}
		if _, dup := seen[num]; dup && !allowedCollisions[strings.TrimPrefix(rel, ".work/")] {
			problems = append(problems, "number collision: "+num+" in "+rel)
		}
		seen[num] = rel
		if body := indexBodies[idx]; body != "" && !strings.Contains(body, base) {
			problems = append(problems, "not in index: "+rel)
		}
	}
	for _, dir := range []string{"docs/adr", "specs/active", "specs/archive"} {
		entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" || e.Name() == "index.md" {
				continue
			}
			idx := "specs/index.md"
			if strings.HasPrefix(dir, "docs/") {
				idx = "docs/adr/index.md"
			}
			checkFile(dir+"/"+e.Name(), idx)
		}
	}
	for _, dir := range []string{".work/in-progress", ".work/done"} {
		entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			checkFile(dir+"/"+e.Name(), ".work/index.md")
		}
	}
	_ = filepath.WalkDir(filepath.Join(root, "docs"), func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(name) != ".md" {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		rel = filepath.ToSlash(rel)
		body, _ := os.ReadFile(name)
		if lines := bytes.Count(body, []byte("\n")) + 1; lines > 500 && !oversizeAllowlist[rel] {
			problems = append(problems, fmt.Sprintf("over 500 lines: %s (%d)", rel, lines))
		}
		m := verifiedRe.FindSubmatch(body)
		if m == nil {
			problems = append(problems, "missing last_verified: "+rel)
			return nil
		}
		// ADR records freeze at acceptance; their last_verified is the record
		// date, not a review claim. Living docs must re-verify on every edit.
		if !fullHistory || strings.HasPrefix(rel, "docs/adr/") || verifiedDriftAllowlist[rel] {
			return nil
		}
		out, _ := exec.Command("git", "-C", root, "log", "-1", "--format=%cs", "--", rel).Output()
		lastCommit := strings.TrimSpace(string(out))
		if lastCommit != "" && string(m[1]) < lastCommit {
			problems = append(problems, "last_verified "+string(m[1])+" predates last commit "+lastCommit+": "+rel)
		}
		return nil
	})
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

func Format(root string) error {
	var bad []string
	for _, folder := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, folder), func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(name) != ".go" {
				return err
			}
			body, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			// Git checks out text with CRLF on Windows; gofmt's canonical output is LF.
			normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
			formatted, err := format.Source(normalized)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if !bytes.Equal(normalized, formatted) {
				rel, _ := filepath.Rel(root, name)
				bad = append(bad, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("gofmt required: %s", strings.Join(bad, ", "))
	}
	return nil
}

func DockerSmoke(ctx context.Context, image string) error {
	var suffix [5]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("hermes-smoke-%x", suffix)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	defer exec.Command("docker", "rm", "-f", name).Run()
	checks := [][]string{
		{"run", "--rm", "--entrypoint", "hermes", image, "--version"},
		{"run", "--rm", "--entrypoint", "glab", image, "--version"},
		{"run", "--rm", "--entrypoint", "/opt/hermes/.venv/bin/python", image, "-c", "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"},
		{"run", "-d", "--name", name, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--shm-size", "1g", "--tmpfs", "/tmp:mode=1777", "--tmpfs", "/state:uid=10001,gid=10001,mode=0700", "--tmpfs", "/workspace:uid=10001,gid=10001,mode=0700", "-e", "HUB_BROWSER=true", image, "idle"},
	}
	for _, args := range checks {
		if err := run(args...); err != nil {
			return err
		}
	}
	for range 90 {
		if exec.CommandContext(ctx, "docker", "exec", name, "hub-runtime", "health").Run() == nil {
			if err := runtimeContractSmoke(ctx, image, name+"-contract"); err != nil {
				return err
			}
			fmt.Println("Standalone Docker runtime smoke passed; no live provider calls")
			return nil
		}
		time.Sleep(time.Second)
	}
	_ = run("logs", name)
	return fmt.Errorf("agent/browser failed health check")
}

func runtimeContractSmoke(ctx context.Context, image, name string) error {
	defer exec.Command("docker", "rm", "-f", name).Run()
	start := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp:mode=1777", "--tmpfs", "/state:uid=10001,gid=10001,mode=0700", "--tmpfs", "/workspace:uid=10001,gid=10001,mode=0700", "-e", "HUB_BROWSER=false", "-e", "HUB_RUNTIME_AUTH=smoke-auth", "-e", "HUB_USER_ID=smoke", "-e", "HUB_ORGANIZATION_ID=personal", "-e", "HUB_RUNTIME_ID=smoke", "-e", "HUB_POLICY_VERSION=policy-smoke", image, "serve")
	if err := start.Run(); err != nil {
		return err
	}
	for range 30 {
		output, err := exec.CommandContext(ctx, "docker", "exec", name, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-X", "POST", "-H", "Authorization: Bearer smoke-auth", "--data", "{}", "http://127.0.0.1:8080/v1/execute").Output()
		if err == nil && string(output) == "409" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("runtime identity contract probe failed")
}
