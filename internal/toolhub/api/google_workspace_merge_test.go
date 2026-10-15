package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestGoogleWorkspaceScopesAndBackendGuards(t *testing.T) {
	// every approved product resolves read-only scopes; unknown product rejected
	for product, tools := range WorkspaceReadTools {
		scopes, err := GoogleWorkspaceScopes(product)
		if err != nil || len(scopes) == 0 {
			t.Fatalf("%s scopes: %v", product, err)
		}
		if len(tools) == 0 {
			t.Fatal(product)
		}
		for _, s := range scopes {
			if !strings.HasPrefix(s, "https://www.googleapis.com/auth/") {
				t.Fatalf("%s scope not read-only googleapis: %s", product, s)
			}
		}
	}
	if _, err := GoogleWorkspaceScopes("admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown product: %v", err)
	}
	calendar, _ := GoogleWorkspaceScopes("calendar")
	if !strings.Contains(strings.Join(calendar, " "), CalendarReadScope) {
		t.Fatal("calendar scope missing")
	}

	backend := GoogleWorkspaceBackend{HTTP: &http.Client{}}
	e := EffectiveBinding{
		Definition: ToolDefinition{DefinitionID: "google-workspace-gmail-read", Transport: RemoteMCP,
			Source: DefinitionSource{URL: workspaceEndpoint("gmail")}},
		Connection: &Connection{Metadata: map[string]string{"google_sub": "sub-1"}},
	}
	tool := ToolSpec{Name: "gmail.search_threads", Effect: ReadEffect}
	env := map[string]string{"OAUTH_SCOPE": strings.Join(mustScopes(t, "gmail"), " "), "ACCESS_TOKEN": "tok"}

	// authority-shaped arguments are always rejected first
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"credential_ref": "x"}, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authority arg: %v", err)
	}
	// wrong transport, missing sub, write effect, undeclared tool, endpoint mismatch
	for name, mutate := range map[string]func(EffectiveBinding, ToolSpec) (EffectiveBinding, ToolSpec){
		"transport": func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) {
			b.Definition.Transport = ContainerMCP
			return b, s
		},
		"no sub": func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) {
			b.Connection = &Connection{}
			return b, s
		},
		"nil conn":   func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) { b.Connection = nil; return b, s },
		"write tool": func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) { s.Effect = WriteEffect; return b, s },
		"unlisted": func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) {
			s.Name = "gmail.delete_everything"
			return b, s
		},
		"endpoint": func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) {
			b.Definition.Source.URL = "https://evil.example/mcp"
			return b, s
		},
		"bad product": func(b EffectiveBinding, s ToolSpec) (EffectiveBinding, ToolSpec) {
			b.Definition.DefinitionID = "google-workspace-admin-read"
			return b, s
		},
	} {
		b2, t2 := mutate(e, tool)
		if _, err := backend.CallEnv(context.Background(), b2, t2, nil, env); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// granted scopes must cover every required scope
	if _, err := backend.CallEnv(context.Background(), e, tool, nil, map[string]string{"OAUTH_SCOPE": "openid", "ACCESS_TOKEN": "tok"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("partial scope accepted")
	}
}

func mustScopes(t *testing.T, product string) []string {
	t.Helper()
	scopes, err := GoogleWorkspaceScopes(product)
	if err != nil {
		t.Fatal(err)
	}
	return scopes
}
