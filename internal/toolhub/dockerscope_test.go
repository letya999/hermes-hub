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

// Sibling-space runtimes share one toolhub but sit on their own agent net;
// the label proof (hermes-hub.context prefix == name suffix) is what admits
// them — not the network.
func TestAdmitSupervisedRuntime(t *testing.T) {
	ctx := context.Background()
	// names carry the first 16 hex of the context hash
	const full = "e3158aa0d8dec80bdd869c268fe01bf4b5abe146e587f0f316dc5a956224738f"
	objects := map[string]func(args []string) (string, error){
		// own-space runtime: agent net membership already owns it
		"hermes-context-aaaaaaaaaaaaaaaa": func(args []string) (string, error) {
			for i, a := range args {
				if a == "--format" && i+1 < len(args) && strings.Contains(args[i+1], "index") {
					return full[:0] + full[:16] + full[16:][:48] + "|deadbeef", nil
				}
			}
			return `{"hermes-hub.owner":"deadbeef"}|{"other-net":{"IPAddress":"10.9.0.2"}}`, nil
		},
		// sibling runtime on its own net, correct context proof
		"hermes-context-e3158aa0d8dec80b": func(args []string) (string, error) {
			for i, a := range args {
				if a == "--format" && i+1 < len(args) && strings.Contains(args[i+1], "index") {
					return full + "|dfedaaa1f321c04b8a72c7e605356cd35475353b3660429d30901c16f164a0f3", nil
				}
			}
			return `{"hermes-hub.context":"` + full + `","hermes-hub.owner":"dfedaaa1"}|{"hermes-hub-agent-sibling-dev":{"IPAddress":"172.30.0.3"}}`, nil
		},
		// name squatter: no labels at all
		"hermes-context-bbbbbbbbbbbbbbbb": func(args []string) (string, error) {
			for i, a := range args {
				if a == "--format" && i+1 < len(args) && strings.Contains(args[i+1], "index") {
					return "<no value>|<no value>", nil
				}
			}
			return `null|{"some-net":{"IPAddress":"10.1.0.2"}}`, nil
		},
		// wrong binding: context hash does not carry the name suffix
		"hermes-context-cccccccccccccccc": func(args []string) (string, error) {
			for i, a := range args {
				if a == "--format" && i+1 < len(args) && strings.Contains(args[i+1], "index") {
					return full + "|owner", nil
				}
			}
			return `{"hermes-hub.context":"` + full + `","hermes-hub.owner":"owner"}|{"other-net":{"IPAddress":"10.2.0.2"}}`, nil
		},
		// an unrelated foreign container that exists but is not supervised
		"not-a-runtime": func(args []string) (string, error) {
			for i, a := range args {
				if a == "--format" && i+1 < len(args) && strings.Contains(args[i+1], "index") {
					return "<no value>|<no value>", nil
				}
			}
			return `{"com.docker.compose.project":"other-stack"}|{"other-net":{"IPAddress":"10.0.0.2"}}`, nil
		},
	}
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		responder, ok := objects[args[len(args)-1]]
		if !ok {
			return nil, errors.New("No such object: " + args[len(args)-1])
		}
		body, err := responder(args)
		return []byte(body), err
	}
	scope := newDockerScope("hermes-hub-alice-prod", "hermes-hub-agent-alice-prod", run)
	if scope == nil {
		t.Fatal("scope must exist")
	}
	if !scope.admitSupervisedRuntime(ctx, "hermes-context-e3158aa0d8dec80b") {
		t.Fatal("sibling supervised runtime denied")
	}
	if scope.admitSupervisedRuntime(ctx, "hermes-context-bbbbbbbbbbbbbbbb") {
		t.Fatal("label-less squatter admitted")
	}
	if scope.admitSupervisedRuntime(ctx, "hermes-context-cccccccccccccccc") {
		t.Fatal("mismatched context hash admitted")
	}
	if scope.admitSupervisedRuntime(ctx, "not-a-runtime") {
		t.Fatal("non-runtime name admitted")
	}
}
