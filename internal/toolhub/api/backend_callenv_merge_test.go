package api

import (
	"context"
	"errors"
	"testing"
)

// MCPBackend.CallEnv fails closed: remote endpoints re-validate, telegram
// writes need the scoped env, file credentials need an admission release, and
// an injected environment needs a workload root.
func TestCallEnvGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := MCPBackend{}

	remote := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "remote-acme", Transport: RemoteMCP}}
	if _, err := backend.CallEnv(ctx, remote, ToolSpec{Name: "x"}, nil, nil); err == nil {
		t.Fatal("remote definition without endpoint must fail")
	}

	plain := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "plain", Transport: ProviderAPI}}
	if _, err := backend.CallEnv(ctx, plain, ToolSpec{Name: "x"}, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing connection must be ErrInvalid: %v", err)
	}

	telegram := EffectiveBinding{
		Definition: ToolDefinition{DefinitionID: "telegram-account-1", Transport: ContainerMCP},
		Connection: &Connection{Metadata: map[string]string{"telegram_account": "1"}},
	}
	if _, err := backend.CallEnv(ctx, telegram, ToolSpec{Name: "send_message", Effect: WriteEffect}, nil, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("telegram without scoped env must be ErrUnauthorized: %v", err)
	}

	mounts := EffectiveBinding{
		Definition:       ToolDefinition{DefinitionID: "plain", Transport: ContainerMCP},
		CredentialMounts: []Mount{{Source: "a", Target: "b"}},
	}
	if _, err := backend.CallEnv(ctx, mounts, ToolSpec{Name: "x"}, nil, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("mounts without admission release must be ErrIsolation: %v", err)
	}

	envFail := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "plain", Transport: ProviderAPI}, Connection: &Connection{}}
	if _, err := backend.CallEnv(ctx, envFail, ToolSpec{Name: "x"}, nil, map[string]string{"K": "v"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("injected env without root must fail isolation: %v", err)
	}
}
