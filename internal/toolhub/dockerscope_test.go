package toolhub

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scopeRun fakes the docker CLI surface the guard consumes: each name maps to
// the JSON `inspect --format` would print, absent names error out.
func scopeRun(objects map[string]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		name := args[len(args)-1]
		if body, ok := objects[name]; ok {
			return []byte(body), nil
		}
		return nil, errors.New("No such object: " + name)
	}
}

const scopeInspectFormat = `{"com.docker.compose.project":"hermes-hub-alice-prod","hermes-hub.scope":"hermes-hub-alice-prod"}|{"hermes-hub-agent-alice-prod":{"IPAddress":"172.20.0.5"}}`

func scopeFixture() map[string]string {
	return map[string]string{
		"own-compose":  scopeInspectFormat,
		"own-labelled": `{"hermes-hub.scope":"hermes-hub-alice-prod"}|{}`,
		"own-agent":    `{"hermes-hub.owner":"deadbeef"}|{"hermes-hub-agent-alice-prod":{"IPAddress":"172.20.0.9"}}`,
		"foreign":      `{"com.docker.compose.project":"other-stack"}|{"other-net":{"IPAddress":"10.0.0.2"}}`,
		"foreign-bare": `null|{}`,
		"own-net":      `{"hermes-hub.scope":"hermes-hub-alice-prod"}`,
		"foreign-net":  `{"hermes-hub.role":"generic-mcp"}`,
		"own-vol":      `{"hermes-hub.scope":"hermes-hub-alice-prod"}`,
	}
}

func testScope(t *testing.T) *dockerScope {
	t.Helper()
	scope := newDockerScope("hermes-hub-alice-prod", "hermes-hub-agent-alice-prod", scopeRun(scopeFixture()))
	if scope == nil {
		t.Fatal("scope must exist with a project")
	}
	return scope
}

func TestDockerScopeAllowsOwnObjects(t *testing.T) {
	scope := testScope(t)
	ctx := context.Background()
	for _, name := range []string{"own-compose", "own-labelled", "own-agent"} {
		if !scope.owns(ctx, "container", name) {
			t.Fatalf("own container %s denied", name)
		}
	}
	if !scope.owns(ctx, "network", "own-net") || !scope.owns(ctx, "volume", "own-vol") {
		t.Fatal("own network/volume denied")
	}
	// An inspectable-missing object is a no-op operand: allow it through.
	if !scope.owns(ctx, "container", "does-not-exist") {
		t.Fatal("missing object should pass as a no-op")
	}
	// A name this process spawned stays owned even though ToolHive cannot
	// stamp labels on it.
	scope.register("thv-workload-1")
	if !scope.owns(ctx, "container", "thv-workload-1") {
		t.Fatal("registered spawn denied")
	}
}

func TestDockerScopeDeniesForeignObjects(t *testing.T) {
	scope := testScope(t)
	ctx := context.Background()
	for _, name := range []string{"foreign", "foreign-bare"} {
		if scope.owns(ctx, "container", name) {
			t.Fatalf("foreign container %s allowed", name)
		}
	}
	if scope.owns(ctx, "network", "foreign-net") || scope.owns(ctx, "volume", "foreign-net") {
		t.Fatal("foreign network/volume allowed")
	}
}

func TestDockerScopeGuardDeniesCrossProjectOps(t *testing.T) {
	scope := testScope(t)
	ctx := context.Background()
	for _, args := range [][]string{
		{"exec", "-i", "foreign", "hubctl", "tools-exec"},
		{"inspect", "foreign", "--format", "{{.Id}}"},
		{"logs", "--tail", "10", "foreign"},
		{"rm", "--force", "foreign"},
		{"network", "rm", "foreign-net"},
		{"volume", "rm", "foreign-net"},
		{"network", "connect", "bridge", "foreign"},
		{"cp", "conf", "foreign:/etc/squid/squid.conf"},
	} {
		if err := scope.guardDocker(ctx, args); err == nil {
			t.Fatalf("foreign operand allowed: %v", args)
		} else if !errors.Is(err, ErrIsolation) {
			t.Fatalf("denial must carry ErrIsolation: %v", err)
		}
	}
	for _, args := range [][]string{
		{"exec", "-i", "own-agent", "hubctl", "tools-exec"},
		{"inspect", "own-compose"},
		{"rm", "-f", "own-labelled"},
		{"network", "rm", "own-net"},
		{"volume", "rm", "own-vol"},
		{"network", "connect", "bridge", "own-labelled"},
		{"cp", "conf", "own-labelled:/etc/squid/squid.conf"},
		{"inspect", "does-not-exist"},
		{"image", "inspect", "repo:tag"},
		{"pull", "repo:tag"},
	} {
		if err := scope.guardDocker(ctx, args); err != nil {
			t.Fatalf("own/no-op operand denied: %v: %v", args, err)
		}
	}
}

func TestDockerScopeRunScopedChecksToolHiveRm(t *testing.T) {
	scope := testScope(t)
	ctx := context.Background()
	if _, err := scope.runScoped(ctx, scopeRun(scopeFixture()), "thv", "rm", "foreign"); err == nil {
		t.Fatal("toolhive rm of a foreign container allowed")
	}
	if _, err := scope.runScoped(ctx, scopeRun(scopeFixture()), "thv", "version"); err == nil {
		t.Fatal("toolhive version should reach the fake binary path")
	} else if !strings.Contains(err.Error(), "No such object") {
		// runScoped passes non-rm calls straight to the runner; the fake
		// errors on the last arg, which here is "version".
		t.Fatalf("unexpected passthrough error: %v", err)
	}
}
