package api

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestCredentialEgressHostsExtraction(t *testing.T) {
	hosts := CredentialEgressHosts(map[string]string{
		"SELF_HOSTED_URL":  "https://gitlab.corp.example.com:8443/api",
		"PLAIN_URL":        "http://insecure.example.com",
		"API_TOKEN":        "https://secret.example.com", // secret name skipped
		"DB_ENDPOINT":      "db.internal:5432",
		"SSH_ENDPOINT_2":   "10.1.2.3:22",
		"BAD_ENDPOINT":     "no-port",
		"BADPORT_ENDPOINT": "host:99999",
		"IPV6_ENDPOINT":    "[::1]:22",
		"SINGLE_ENDPOINT":  "singlelabel:443",
		"NOTHING":          "ignored",
		"QUERY_URL":        "https://x.example.com?y=1",
		"FRAG_URL":         "https://x.example.com#f",
		"USERINFO_URL":     "https://u:p@x.example.com",
		"SINGLE_URL":       "https://singlelabel",
	})
	slices.Sort(hosts)
	want := []string{"10.1.2.3:22", "db.internal:5432", "gitlab.corp.example.com:8443"}
	if !slices.Equal(hosts, want) {
		t.Fatalf("hosts: %v", hosts)
	}
	merged := MergeEgressHosts([]string{" Existing.com ", "a.com"}, []string{"existing.com", "b.com"})
	if !slices.Equal(merged, []string{"existing.com", "a.com", "b.com"}) {
		t.Fatalf("merged: %v", merged)
	}
	if got := MergeEgressHosts([]string{"a"}, nil); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("no-op merge: %v", got)
	}
}

func TestValidateBackendEndpointPrivacy(t *testing.T) {
	for _, good := range []string{
		"http://toolhive:8080/mcp", "https://vmcp:443/x",
		"http://localhost:8080", "http://host.docker.internal:9000",
		"http://127.0.0.1:8080", "http://10.0.0.5:9090", "https://192.168.1.1",
	} {
		if err := ValidateBackendEndpoint(good); err != nil {
			t.Fatalf("private endpoint %q rejected: %v", good, err)
		}
	}
	for _, bad := range []string{
		"ftp://toolhive", "http://", "http://u:p@toolhive:1",
		"http://toolhive?q=1", "http://toolhive#f", "not a url",
		"https://8.8.8.8/mcp", "https://example.com/",
	} {
		if err := ValidateBackendEndpoint(bad); err == nil {
			t.Fatalf("public/malformed endpoint %q admitted", bad)
		}
	}
}

func TestCliInputSchemaAndRouting(t *testing.T) {
	tool := ToolSpec{Arguments: []CLIArgument{
		{Name: "path", Type: "string", Required: true},
		{Name: "limit", Type: "integer"},
	}}
	schema := CliInputSchema(tool)
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema: %v", schema)
	}
	props := schema["properties"].(map[string]any)
	if props["path"].(map[string]any)["type"] != "string" {
		t.Fatalf("properties: %v", props)
	}
	if req := schema["required"].([]string); len(req) != 1 || req[0] != "path" {
		t.Fatalf("required: %v", req)
	}
	open := CliInputSchema(ToolSpec{})
	if open["additionalProperties"] != true {
		t.Fatalf("empty schema not open: %v", open)
	}
	// routing: transport selects backend, missing backend fails closed
	routing := RoutingBackend{}
	effective := EffectiveBinding{Definition: ToolDefinition{Transport: AgentTools}}
	if _, err := routing.Call(context.Background(), effective, tool, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing agent backend: %v", err)
	}
	effective.Definition.Transport = Transport("unknown")
	if _, err := routing.Call(context.Background(), effective, tool, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing mcp backend: %v", err)
	}
	called := false
	routing.CLI = stubBackend{fn: func() (BackendResult, error) {
		called = true
		return BackendResult{Text: "ok"}, nil
	}}
	effective.Definition.Transport = BoundedCLI
	if _, err := routing.Call(context.Background(), effective, tool, nil); err != nil || !called {
		t.Fatalf("cli route: %v called=%v", err, called)
	}
}

type stubBackend struct {
	fn func() (BackendResult, error)
}

func (b stubBackend) Call(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
	return b.fn()
}
