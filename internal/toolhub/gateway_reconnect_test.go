package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGatewayRefreshesOpenOwnerSessionWithoutCrossOwnerAccess(t *testing.T) {
	store, aliceAuth, binding := seededStore(t)
	const aliceToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const bobToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	bobAuth := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	var backendCalls atomic.Int32
	gateway := &Gateway{
		Store: store, Tokens: map[string]identity.Envelope{aliceToken: aliceAuth, bobToken: bobAuth},
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			backendCalls.Add(1)
			return BackendResult{Text: "ok"}, nil
		}),
	}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	changed := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "alice", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	alice, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + DefaultEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: aliceToken}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	bobClient := mcp.NewClient(&mcp.Implementation{Name: "bob", Version: "1"}, nil)
	bob, err := bobClient.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + DefaultEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: bobToken}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	name := ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
	if listed, err := alice.ListTools(context.Background(), nil); err != nil || !toolListed(listed, name) {
		t.Fatalf("alice initial tools/list: listed=%v err=%v", listed, err)
	}
	if listed, err := bob.ListTools(context.Background(), nil); err != nil || toolListed(listed, name) {
		t.Fatalf("bob saw alice tool: listed=%v err=%v", listed, err)
	}
	initialSession := alice.ID()
	if initialSession == "" {
		t.Fatal("gateway did not create a persistent MCP session")
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+DefaultEndpointPath, nil)
	request.Header.Set("Authorization", "Bearer "+bobToken)
	request.Header.Set("Mcp-Session-Id", initialSession)
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign session ID accepted: %d", response.StatusCode)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if err := gateway.RefreshProjection(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("open MCP session received no tools/list_changed notification")
	}
	if listed, err := alice.ListTools(context.Background(), nil); err != nil || toolListed(listed, name) {
		t.Fatalf("disabled tool still listed: listed=%v err=%v", listed, err)
	}
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "stale"}}); err == nil || backendCalls.Load() != 0 {
		t.Fatalf("disabled stale call reached backend: err=%v calls=%d", err, backendCalls.Load())
	}
	if alice.ID() != initialSession {
		t.Fatal("projection refresh replaced the MCP session")
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, ActiveStatus); err != nil {
		t.Fatal(err)
	}
	if err := gateway.RefreshProjection(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("restored tool did not notify the open MCP session")
	}
	if listed, err := alice.ListTools(context.Background(), nil); err != nil || !toolListed(listed, name) || alice.ID() != initialSession {
		t.Fatalf("restored tool unavailable in original session: listed=%v err=%v", listed, err)
	}
}

func TestGatewayRefreshesProjectionWrittenByAnotherStore(t *testing.T) {
	seed, auth, binding := seededStore(t)
	path := filepath.Join(t.TempDir(), "toolhub.json")
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	serving, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mutating, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Store: serving, Tokens: map[string]identity.Envelope{aliceToken: auth}, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{Text: "ok"}, nil
	})}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "external-store", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + DefaultEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: aliceToken}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
	if listed, err := session.ListTools(context.Background(), nil); err != nil || !toolListed(listed, name) {
		t.Fatalf("initial tool unavailable: listed=%v err=%v", listed, err)
	}
	if err := mutating.SetBindingStatus(binding.ToolBindingID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if err := gateway.RefreshProjection(); err != nil {
		t.Fatal(err)
	}
	if listed, err := session.ListTools(context.Background(), nil); err != nil || toolListed(listed, name) {
		t.Fatalf("external projection change was missed: listed=%v err=%v", listed, err)
	}
}
