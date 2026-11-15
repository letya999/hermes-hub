package agenttools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserSidecarSessionOwnerGrantAndOperations(t *testing.T) {
	provider := mcp.NewServer(&mcp.Implementation{Name: "browser-fixture", Version: "1"}, nil)
	var calls []string
	var idForCapture string
	for _, name := range []string{"browser_tabs", "browser_snapshot", "browser_click", "browser_take_screenshot"} {
		tool := name
		provider.AddTool(&mcp.Tool{Name: tool, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if tool == "browser_tabs" {
				var args struct{ Action, URL string }
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				if args.Action == "new" {
					if args.URL != "https://example.org" {
						t.Errorf("new tab URL = %v", args.URL)
					}
					calls = append(calls, "browser_tabs:new")
				} else {
					calls = append(calls, "browser_tabs:"+args.Action)
				}
			} else {
				if tool == "browser_take_screenshot" {
					var args struct{ Filename string }
					if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
						t.Fatal(err)
					}
					if args.Filename != "browser/"+idForCapture+".png" {
						t.Errorf("capture filename = %q", args.Filename)
					}
				}
				calls = append(calls, tool)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
		})
	}
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return provider }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer server.Close()
	old := browserEndpoint
	browserEndpoint = func(_, _ string) string { return server.URL }
	t.Cleanup(func() { browserEndpoint = old })
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_MANAGED_BROWSER", `{"persistent":true,"guest":true,"act":true}`)
	tools := &Tools{}
	opened, err := tools.browserCall(t.Context(), "browser_open", Input{URL: "https://example.org"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["session"].(string)
	idForCapture = id
	if _, err := tools.browserCall(t.Context(), "browser_read", Input{Session: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.browserCall(t.Context(), "browser_act", Input{Session: id, Action: "click", Target: "button"}); err != nil {
		t.Fatal(err)
	}
	captured, err := tools.browserCall(t.Context(), "browser_capture", Input{Session: id, Kind: "screenshot"})
	if err != nil || captured.(map[string]any)["path"] != "browser/"+id+".png" {
		t.Fatalf("capture = %v, %v", captured, err)
	}
	if len(calls) != 4 || calls[0] != "browser_tabs:new" || calls[1] != "browser_snapshot" || calls[2] != "browser_click" || calls[3] != "browser_take_screenshot" {
		t.Fatalf("sidecar calls = %v", calls)
	}
	t.Setenv("HUB_USER_ID", "bob")
	if _, err := tools.browserCall(t.Context(), "browser_read", Input{Session: id}); err == nil {
		t.Fatal("foreign owner read browser session")
	}
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_MANAGED_BROWSER", `{"persistent":true,"act":false}`)
	if _, err := tools.browserCall(t.Context(), "browser_act", Input{Session: id, Action: "click", Target: "button"}); err == nil {
		t.Fatal("revoked action used browser session")
	}
	if _, err := tools.browserCall(t.Context(), "browser_act", Input{Session: id, Action: "close"}); err == nil {
		t.Fatal("revoked action closed browser session")
	}
	t.Setenv("HUB_MANAGED_BROWSER", `{"persistent":true,"act":true}`)
	if _, err := tools.browserCall(t.Context(), "browser_act", Input{Session: id, Action: "close"}); err != nil {
		t.Fatal(err)
	}
	if calls[len(calls)-1] != "browser_tabs:close" {
		t.Fatalf("close did not close owned tab: %v", calls)
	}
	if _, err := tools.browserCall(t.Context(), "browser_read", Input{Session: id}); err == nil {
		t.Fatal("closed browser session was reusable")
	}
}

func TestBrowserOperationMapsReviewedPlaywrightTools(t *testing.T) {
	cases := []struct {
		name  string
		input Input
		want  string
	}{
		{"browser_act", Input{Action: "type", Target: "textbox", Value: "hello"}, "browser_type"},
		{"browser_act", Input{Action: "press", Value: "Enter"}, "browser_press_key"},
		{"browser_act", Input{Action: "select", Target: "listbox", Value: "one"}, "browser_select_option"},
		{"browser_act", Input{Action: "scroll", Value: "500"}, "browser_mouse_wheel"},
		{"browser_act", Input{Action: "wait", Value: "Ready"}, "browser_wait_for"},
		{"browser_capture", Input{Session: "browser-valid", Kind: "pdf"}, "browser_pdf_save"},
	}
	for _, tc := range cases {
		got, _, err := browserOperation(tc.name, tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%s/%s = %s, %v", tc.name, tc.input.Action, got, err)
		}
	}
	if _, _, err := browserOperation("browser_act", Input{Action: "scroll", Value: "999999"}); err == nil {
		t.Fatal("unbounded browser scroll admitted")
	}
	if name, args, err := browserOperation("browser_act", Input{Action: "wait", Value: "7"}); err != nil || name != "browser_wait_for" || args["time"] != float64(7) || args["text"] != nil {
		t.Fatalf("numeric wait = %s, %v, %v", name, args, err)
	}
	if name, args, err := browserOperation("browser_act", Input{Action: "wait", Value: "Ready"}); err != nil || name != "browser_wait_for" || args["text"] != "Ready" || args["time"] != nil {
		t.Fatalf("text wait = %s, %v, %v", name, args, err)
	}
	for _, value := range []string{"0", "31", "NaN", "Inf"} {
		if _, _, err := browserOperation("browser_act", Input{Action: "wait", Value: value}); err == nil {
			t.Errorf("out-of-range wait %q admitted", value)
		}
	}
}
