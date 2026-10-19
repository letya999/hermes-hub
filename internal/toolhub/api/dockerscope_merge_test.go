package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDockerScopeGuardContract(t *testing.T) {
	ctx := context.Background()
	// inspect output: project label for "ours", agent-net membership for "net-obj", nothing for "foreign"
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		name := args[len(args)-1]
		switch name {
		case "ours":
			return []byte(`{"com.docker.compose.project":"proj1"}|{}`), nil
		case "net-obj":
			return []byte(`{}|{"agent-net":{"IPAddress":"10.0.0.2"}}`), nil
		case "missing":
			return nil, fmt.Errorf("no such object")
		case "ctx-ok":
			return []byte(strings.Repeat("a", 64) + "|owner-1"), nil
		default:
			return []byte(`{"other":"x"}|{"bridge":{}}`), nil
		}
	}
	scope := NewDockerScope("proj1", "agent-net", run)
	if scope == nil {
		t.Fatal("nil scope")
	}
	if NewDockerScope("  ", "n", run) != nil {
		t.Fatal("empty project must disable the guard")
	}

	// ownership by label, agent-net, inspect failure, registration
	if !scope.Owns(ctx, "container", "ours") || !scope.Owns(ctx, "container", "net-obj") ||
		!scope.Owns(ctx, "container", "missing") || scope.Owns(ctx, "container", "foreign") {
		t.Fatal("container ownership")
	}
	// registered names pass without inspect
	scope.Register("spawned")
	if !scope.Owns(ctx, "container", "spawned") {
		t.Fatal("registered name denied")
	}
	// foreign argv is denied; own and missing names pass
	if err := scope.GuardDocker(ctx, []string{"rm", "foreign"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("rm foreign: %v", err)
	}
	if err := scope.GuardDocker(ctx, []string{"rm", "ours"}); err != nil {
		t.Fatalf("rm ours: %v", err)
	}
	if err := scope.GuardDocker(ctx, []string{"exec", "-e", "FOO=1", "foreign", "cmd"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("exec foreign: %v", err)
	}
	// flag values are skipped; operands after -- are not inspected
	if err := scope.GuardDocker(ctx, []string{"inspect", "--format", "foreign", "ours"}); err != nil {
		t.Fatalf("format flag value treated as operand: %v", err)
	}
	if err := scope.GuardDocker(ctx, []string{"network", "connect", "bridge", "foreign"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("network connect foreign: %v", err)
	}
	if err := scope.GuardDocker(ctx, []string{"cp", "foreign:/x", "/tmp"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("cp foreign: %v", err)
	}
	if err := scope.GuardDocker(ctx, nil); err != nil {
		t.Fatal("empty argv")
	}
	// supervised runtime: name prefix + 64-hex context label proof
	ctxName := "hermes-context-" + strings.Repeat("a", 16)
	badName := "hermes-context-" + strings.Repeat("b", 16)
	// owns fails (no scope labels), so the context-label proof decides
	runProof := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		name := args[len(args)-1]
		if strings.Contains(strings.Join(args, " "), "hermes-hub.context") {
			if name == ctxName {
				return []byte(strings.Repeat("a", 64) + "|owner-1"), nil
			}
			return []byte("deadbeef|owner-1"), nil
		}
		return []byte(`{}|{}`), nil
	}
	scopeCtx := NewDockerScope("proj1", "", runProof)
	if !scopeCtx.AdmitSupervisedRuntime(ctx, ctxName) {
		t.Fatal("supervised runtime denied")
	}
	if scopeCtx.AdmitSupervisedRuntime(ctx, badName) {
		t.Fatal("context-label mismatch admitted")
	}
	if scopeCtx.AdmitSupervisedRuntime(ctx, "hermes-context-zz") {
		t.Fatal("bad suffix admitted")
	}
	// RunScoped: docker argv is guarded; foreign binaries only on rm
	out, err := scope.RunScoped(ctx, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte("ran"), nil
	}, "docker", "inspect", "ours")
	if err != nil || string(out) != "ran" {
		t.Fatalf("scoped run: %v %v", out, err)
	}
	stubCmd := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if _, err := scope.RunScoped(ctx, stubCmd, "toolhive", "rm", "foreign"); !errors.Is(err, ErrIsolation) {
		t.Fatalf("toolhive rm foreign: %v", err)
	}
	if _, err := scope.RunScoped(ctx, stubCmd, "toolhive", "rm", "ours"); err != nil {
		t.Fatalf("toolhive rm ours: %v", err)
	}
	// process-wide scope install/clear
	SetDockerScope("proj1", "")
	if CurrentDockerScope() == nil {
		t.Fatal("scope not installed")
	}
	SetDockerScope("", "")
	if CurrentDockerScope() != nil {
		t.Fatal("scope not cleared")
	}
}
