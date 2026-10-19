package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDecodeMCPToolListStream(t *testing.T) {
	// startup noise, a wrong-id response, then the id=2 tools/list reply
	stream := strings.Join([]string{
		`plain startup log`,
		`{"level":"error","error":"noise"}`,
		`{"id":1,"result":{"serverInfo":{}}}`,
		`{"id":2,"result":{"tools":[` +
			`{"name":"list_items","description":"read","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}},` +
			`{"name":"delete_item","annotations":{"destructiveHint":true,"readOnlyHint":true}},` +
			`{"name":"update_item","description":"` + strings.Repeat("x", 1100) + `"}` +
			`]}}`,
	}, "\n")
	tools, err := DecodeMCPToolList(strings.NewReader(stream))
	if err != nil || len(tools) != 3 {
		t.Fatalf("tools: %v %v", err, tools)
	}
	if tools[0].Effect != ReadEffect || tools[1].Effect != WriteEffect || tools[2].Effect != WriteEffect {
		t.Fatalf("effects: %+v", tools)
	}
	if tools[2].Description != "" {
		t.Fatal("oversized description kept")
	}
	// RPC error envelope fails closed
	_, err = DecodeMCPToolList(strings.NewReader(`{"id":2,"error":{"message":"auth required"}}`))
	if !errors.Is(err, ErrIsolation) || !strings.Contains(err.Error(), "auth required") {
		t.Fatalf("rpc error: %v", err)
	}
	// invalid tool name rejected
	_, err = DecodeMCPToolList(strings.NewReader(`{"id":2,"result":{"tools":[{"name":"bad name!"}]}}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad tool name: %v", err)
	}
	// empty tools → ErrEmptyToolList
	_, err = DecodeMCPToolList(strings.NewReader(`{"id":2,"result":{"tools":[]}}`))
	if !errors.Is(err, ErrEmptyToolList) {
		t.Fatalf("empty list: %v", err)
	}
	// oversized input schema rejected
	big := `{"id":2,"result":{"tools":[{"name":"t","inputSchema":{"x":"` + strings.Repeat("y", 66000) + `"}}]}}`
	if _, err := DecodeMCPToolList(strings.NewReader(big)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize schema: %v", err)
	}
	// no tools/list reply at all
	if _, err := DecodeMCPToolList(strings.NewReader("noise only\n")); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing reply: %v", err)
	}
}

func TestParseMCPHelpCredentialsSection(t *testing.T) {
	help := `Usage: server [flags]

Environment Variables:
  API_TOKEN    API token (required)
  ORG_SECRET   Org secret (recommended)
  VERBOSE      Enable debug logging

Other section
`
	creds := ParseMCPHelpCredentials(help)
	if len(creds) != 2 || creds[0].Name != "API_TOKEN" || !creds[0].Required || creds[1].Name != "ORG_SECRET" {
		t.Fatalf("help creds: %+v", creds)
	}
	if got := ParseMCPHelpCredentials("no env section"); len(got) != 0 {
		t.Fatalf("no-section help: %v", got)
	}
}

func TestRetryListWithPromotedCredentialsContract(t *testing.T) {
	base := ImportedArtifact{Definition: ToolDefinition{
		Credentials: []CredentialInput{
			{Name: "API_TOKEN"},     // optional secret
			{Name: "OPTIONAL_FLAG"}, // optional non-secret
		},
	}}
	list := func(ctx context.Context, i ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "t", Effect: ReadEffect}}, nil
	}
	// error names a credential → that secret promoted
	out, tools, err := RetryListWithPromotedCredentials(context.Background(), base, errors.New("set API_TOKEN first"), list)
	if err != nil || len(tools) != 1 {
		t.Fatalf("retry: %v", err)
	}
	if !out.Definition.Credentials[0].Required || out.Definition.Credentials[1].Required {
		t.Fatalf("promotion: %+v", out.Definition.Credentials)
	}
	// error names an undeclared credential → appended as required
	out, _, err = RetryListWithPromotedCredentials(context.Background(), base, errors.New("MISSING_KEY is required"), list)
	if err != nil {
		t.Fatal(err)
	}
	var appended bool
	for _, c := range out.Definition.Credentials {
		if c.Name == "MISSING_KEY" && c.Required {
			appended = true
		}
	}
	if !appended {
		t.Fatalf("undeclared credential not appended: %+v", out.Definition.Credentials)
	}
	// unnamed error → all optional secrets promoted
	out, _, err = RetryListWithPromotedCredentials(context.Background(), base, errors.New("auth failed"), list)
	if err != nil || !out.Definition.Credentials[0].Required {
		t.Fatalf("blanket promotion: %v %+v", err, out.Definition.Credentials)
	}
	// nothing promotable → ErrInvalid
	plain := ImportedArtifact{}
	if _, _, err := RetryListWithPromotedCredentials(context.Background(), plain, errors.New("x"), list); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no-op retry: %v", err)
	}
	// retry failure propagates
	failErr := errors.New("still failing")
	if _, _, err := RetryListWithPromotedCredentials(context.Background(), base, errors.New("set API_TOKEN"), func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, failErr
	}); !errors.Is(err, failErr) {
		t.Fatalf("retry failure: %v", err)
	}
}
