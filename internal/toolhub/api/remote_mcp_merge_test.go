package api

import (
	"errors"
	"strings"
	"testing"
)

func TestRemoteDefinitionAndSelection(t *testing.T) {
	def, err := RemoteDefinitionBase(" https://mcp.example.com:8443/x ", "remote-acme", nil)
	if err != nil {
		t.Fatal(err)
	}
	if def.DefinitionID != "remote-acme" || def.Source.URL != "https://mcp.example.com:8443/x" ||
		def.Execution.Egress[0] != "mcp.example.com:8443" || def.Source.TLSMode != "required" {
		t.Fatalf("definition base: %+v", def)
	}
	named, err := RemoteDefinitionBase("https://other.example.com/", "", nil)
	if err != nil || named.DefinitionID != "remote-other-example-com" {
		t.Fatalf("default name: %v %q", err, named.DefinitionID)
	}
	for _, bad := range []string{"http://insecure.example.com", "not a url", "https://"} {
		if _, err := RemoteDefinitionBase(bad, "", nil); err == nil {
			t.Fatalf("bad endpoint %q accepted", bad)
		}
	}
	if _, err := RemoteDefinitionBase("https://x.example.com", "not-remote", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-remote id: %v", err)
	}
	tools := []ToolSpec{{Name: "a", Effect: ReadEffect}, {Name: "b", Effect: ReadEffect}}
	sel, err := SelectRemoteTools(tools, []string{"b"})
	if err != nil || len(sel) != 1 || sel[0].Name != "b" {
		t.Fatalf("select: %v %v", err, sel)
	}
	if _, err := SelectRemoteTools(tools, []string{"ghost"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unadvertised allowlist entry: %v", err)
	}
	def, err = RemoteMCPDefinition("https://mcp.example.com/x", "remote-acme", nil, tools, []string{"a"})
	if err != nil || len(def.Tools) != 1 {
		t.Fatalf("remote definition: %v %+v", err, def)
	}
	if _, err := RemoteMCPDefinition("https://mcp.example.com/x", "remote-acme", nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty selection: %v", err)
	}
}

func TestRemoteCredentialInputsContract(t *testing.T) {
	if got, err := RemoteCredentialInputs(map[string]any{}); err != nil || got != nil {
		t.Fatalf("no credentials: %v %v", got, err)
	}
	inputs, err := RemoteCredentialInputs(map[string]any{"credentials": []any{"API_TOKEN"}})
	if err != nil || len(inputs) != 1 || inputs[0].Target != "Authorization" || inputs[0].Prefix != "Bearer " || inputs[0].Delivery != "http_header" {
		t.Fatalf("string credential: %v %+v", err, inputs)
	}
	inputs, err = RemoteCredentialInputs(map[string]any{
		"credentials":       []any{map[string]any{"name": "KEY_TOKEN", "header": "X-Api-Key"}},
		"credential_header": "X-Fallback",
		"credential_prefix": "Token ",
	})
	if err != nil || inputs[0].Target != "X-Api-Key" || inputs[0].Prefix != "Token " {
		t.Fatalf("mapped credential: %v %+v", err, inputs)
	}
	for _, args := range []map[string]any{
		{"credentials": "not-a-list"},
		{"credentials": []any{42}},
		{"credentials": []any{"bad name"}},
		{"credentials": []any{"API_TOKEN", "API_TOKEN"}},
		{"credentials": []any{"A", "B", "C", "D", "E", "F", "G", "H", "I"}},
	} {
		if _, err := RemoteCredentialInputs(args); !errors.Is(err, ErrInvalid) {
			t.Fatalf("args %v: %v", args, err)
		}
	}
	inputs, err = RemoteCredentialInputs(map[string]any{"credentials": []string{"SLACK_TOKEN"}})
	if err != nil || len(inputs) != 1 {
		t.Fatalf("[]string credential: %v %v", err, inputs)
	}
}

func TestRemoteToolClassificationAndHints(t *testing.T) {
	if RemoteToolEffect("list_issues") != ReadEffect || RemoteToolEffect("get_file") != ReadEffect {
		t.Fatal("read tools misclassified")
	}
	for _, name := range []string{"create_issue", "delete-repo", "send.message", "merge_pr", "upload_file"} {
		if RemoteToolEffect(name) != WriteEffect {
			t.Fatalf("write tool %q misclassified", name)
		}
	}
	if RemotePlaceholders(nil) != nil || len(RemotePlaceholders([]string{"a"})) != 1 {
		t.Fatal("placeholders wrong")
	}
	hints := RemoteCredentialHints([]CredentialInput{{Name: "API_TOKEN", Target: "Authorization"}})
	if len(hints) != 1 || !hints[0].Secret || hints[0].Target != "Authorization" || hints[0].Delivery != "http_header" {
		t.Fatalf("hints: %+v", hints)
	}
	if !remoteDefinitionEndpoint(EffectiveBinding{Definition: ToolDefinition{DefinitionID: "remote-x"}}) {
		t.Fatal("definition endpoint not detected")
	}
	if remoteDefinitionEndpoint(EffectiveBinding{Definition: ToolDefinition{DefinitionID: "google-workspace-drive"}}) {
		t.Fatal("google-workspace wrongly treated as definition endpoint")
	}
	conn := &Connection{Metadata: map[string]string{"mcp_endpoint": "https://x"}}
	if remoteDefinitionEndpoint(EffectiveBinding{Connection: conn}) {
		t.Fatal("connection endpoint wrongly treated as definition endpoint")
	}
	long := strings.Repeat("x", 500) + " token=abc123secret "
	sanitized := sanitizeRemoteError(errors.New(long))
	if len(sanitized) > 200 {
		t.Fatalf("sanitize length %d", len(sanitized))
	}
}
