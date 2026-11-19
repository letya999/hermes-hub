package devcheck

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// depsBaseline is the shrink-only snapshot of every binary's module-local
// dependency closure (ADR-0105 D6, SPEC-0100 R3).
const depsBaseline = "internal/devcheck/deps-baseline.txt"

// forbiddenDeps are hard rules that no baseline entry can allow.
var forbiddenDeps = map[string][]string{
	"cmd/runtime":       {"internal/toolhub", "internal/toolhub/automation"},
	"cmd/supervisor":    {"internal/toolhub", "internal/toolhub/automation"},
	"cmd/communication": {"internal/toolhub", "internal/toolhub/automation"},
}

// Deps checks the dependency closure of every cmd/* binary and contracts/*
// package against the forbidden rules and the shrink-only baseline.
func Deps(root string) error {
	graph, err := depGraph(root)
	if err != nil {
		return err
	}
	baseline := map[string]bool{}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(depsBaseline)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" && !strings.HasPrefix(line, "#") {
			baseline[line] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if problems := checkDeps(graph, baseline); len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

// WriteDepsBaseline records the current closure after the forbidden rules
// pass; use it to accept a reviewed new dependency or to shrink the file.
func WriteDepsBaseline(root string) error {
	graph, err := depGraph(root)
	if err != nil {
		return err
	}
	var lines []string
	for _, problem := range checkDeps(graph, nil) {
		if strings.HasPrefix(problem, "new dependency: ") {
			lines = append(lines, strings.TrimSuffix(strings.TrimPrefix(problem, "new dependency: "), " (shrink-only baseline "+depsBaseline+")"))
		} else {
			return fmt.Errorf("%s", problem)
		}
	}
	slices.Sort(lines)
	header := "# Shrink-only module dependency baseline (ADR-0105 D6). Regenerate with\n# `go run ./cmd/devcheck deps-baseline` only for a reviewed change.\n"
	return os.WriteFile(filepath.Join(root, filepath.FromSlash(depsBaseline)), []byte(header+strings.Join(lines, "\n")+"\n"), 0o600)
}

// depGraph includes third-party edges so contracts cannot hide dependencies.
// Module-local package names have the module prefix removed.
func depGraph(root string) (map[string][]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}} {{join .Imports \" \"}}{{end}}", "./cmd/...", "./contracts/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	module := mainModuleAt(root) + "/"
	graph := map[string][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var imports []string
		for _, imp := range fields[1:] {
			imports = append(imports, strings.TrimPrefix(imp, module))
		}
		graph[strings.TrimPrefix(fields[0], module)] = imports
	}
	for pkg, imports := range graph {
		graph[pkg] = slices.DeleteFunc(imports, func(imp string) bool {
			_, nonstandard := graph[imp]
			return !nonstandard
		})
	}
	return graph, nil
}

// closure maps every package reachable from start to its BFS parent.
func closure(graph map[string][]string, start string) map[string]string {
	parent := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		for _, next := range graph[pkg] {
			if _, seen := parent[next]; !seen {
				parent[next] = pkg
				queue = append(queue, next)
			}
		}
	}
	delete(parent, start)
	return parent
}

func depPath(parent map[string]string, start, pkg string) string {
	path := []string{pkg}
	for p := parent[pkg]; p != ""; p = parent[p] {
		path = append(path, p)
	}
	path = append(path, start)
	slices.Reverse(path)
	return strings.Join(slices.Compact(path), " > ")
}

func checkDeps(graph map[string][]string, baseline map[string]bool) []string {
	var problems []string
	seen := map[string]bool{}
	roots := make([]string, 0)
	for pkg := range graph {
		if strings.HasPrefix(pkg, "cmd/") && strings.Count(pkg, "/") == 1 || strings.HasPrefix(pkg, "contracts/") {
			roots = append(roots, pkg)
		}
	}
	slices.Sort(roots)
	for _, bin := range roots {
		parent := closure(graph, bin)
		pkgs := make([]string, 0, len(parent))
		for pkg := range parent {
			pkgs = append(pkgs, pkg)
		}
		slices.Sort(pkgs)
		for _, pkg := range pkgs {
			via := depPath(parent, bin, pkg)
			if slices.Contains(forbiddenDeps[bin], pkg) {
				problems = append(problems, "forbidden: "+bin+" -> "+pkg+" via "+via)
				continue
			}
			if strings.HasPrefix(bin, "contracts/") {
				if !strings.HasPrefix(pkg, "contracts/") && pkg != "internal/identity" {
					problems = append(problems, "forbidden: "+bin+" -> "+pkg+" via "+via)
				}
				continue
			}
			if !strings.HasPrefix(pkg, "internal/") && !strings.HasPrefix(pkg, "contracts/") && !strings.HasPrefix(pkg, "cmd/") {
				continue
			}
			edge := bin + " " + pkg
			seen[edge] = true
			if !baseline[edge] {
				problems = append(problems, "new dependency: "+edge+" (shrink-only baseline "+depsBaseline+")")
			}
		}
	}
	stale := make([]string, 0)
	for edge := range baseline {
		if !seen[edge] {
			stale = append(stale, "stale baseline entry: "+edge+" (delete it)")
		}
	}
	slices.Sort(stale)
	return append(problems, stale...)
}
