package api

import (
	"context"
	"errors"
	"testing"
)

// The Call wrappers project onto CallEnv; an unauthorized call surfaces the
// inner denial through each backend's wrapper.
func TestCallWrappersDelegate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "x", Transport: ProviderAPI}, Connection: &Connection{}}
	if _, err := (MCPBackend{}).Call(ctx, e, ToolSpec{Name: "x"}, nil); err == nil {
		t.Fatal("mcp call wrapper")
	}
	if _, err := (CalendarBackend{}).Call(ctx, e, ToolSpec{Name: "x"}, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("calendar wrapper: %v", err)
	}
	if _, err := (SlackBackend{}).Call(ctx, e, ToolSpec{Name: "x"}, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("slack wrapper: %v", err)
	}
	if _, err := (PersonalProviderBackend{}).Call(ctx, e, ToolSpec{Name: "x"}, nil); err == nil {
		t.Fatal("personal provider wrapper")
	}
}
