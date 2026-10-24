package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestAdmitWithSubmittedCredentialsConfirmsListedTools(t *testing.T) {
	originalLoader, originalList := api.StoredArtifactLoader, api.LocalMCPToolList
	defer func() { api.StoredArtifactLoader, api.LocalMCPToolList = originalLoader, originalList }()
	loaded := 0
	api.StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		loaded++
		return "loaded", nil
	}
	api.LocalMCPToolList = func(ctx context.Context, _ api.ImportedArtifact) ([]api.ToolSpec, error) {
		opts := api.ProbeOptionsFrom(ctx)
		if !opts.AllowNetwork || opts.Values["SERVICE_TOKEN"] == "" {
			t.Fatal("admission probe did not receive the submitted secret and network")
		}
		return []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}, nil
	}
	definition := statefulContainerDefinition()
	definition.Source.ArchiveDigest = "sha256:" + repeatHex('a')
	admitted, err := api.AdmitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{"SERVICE_TOKEN": "submitted"})
	if err != nil || loaded != 1 || len(admitted.Tools) != 1 || admitted.Tools[0].Name != "search" || admitted.Source.ToolContractSource != api.ToolContractPreflight {
		t.Fatalf("admitted=%+v loaded=%d err=%v", admitted.Tools, loaded, err)
	}
	api.LocalMCPToolList = func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
		return nil, errors.New("auth.test failed")
	}
	if _, err := api.AdmitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{"SERVICE_TOKEN": "submitted"}); err == nil {
		t.Fatal("failed tools/list registered")
	}
	api.StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return "", errors.New("archive missing")
	}
	if _, err := api.AdmitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{}); err == nil {
		t.Fatal("missing archive admitted")
	}
	definition.Source.ArchiveDigest = ""
	api.LocalMCPToolList = func(context.Context, api.ImportedArtifact) ([]api.ToolSpec, error) {
		return []api.ToolSpec{{Name: "bad name", Effect: api.ReadEffect}}, nil
	}
	if _, err := api.AdmitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{}); err == nil {
		t.Fatal("invalid tool name admitted")
	}
}

func TestPreflightCredentialArgsUsesSubmittedEnvFile(t *testing.T) {
	imported := api.ImportedArtifact{Definition: statefulContainerDefinition()}
	ctx := api.WithProbeOptions(context.Background(), api.ProbeOptions{Values: map[string]string{"SERVICE_TOKEN": "submitted-value"}})
	args, cleanup, err := api.PreflightCredentialArgs(ctx, imported)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	placeholder := false
	leaked := false
	hasEnvFile := false
	for _, arg := range args {
		if arg == "--env-file" {
			hasEnvFile = true
		}
		if arg == "preflight" || strings.Contains(arg, "=preflight") {
			placeholder = true
		}
		if strings.Contains(arg, "submitted-value") {
			leaked = true
		}
	}
	if !hasEnvFile || placeholder || leaked {
		t.Fatal("submitted secret was not kept in an env file")
	}
	var path string
	for i, arg := range args {
		if arg == "--env-file" && i+1 < len(args) {
			path = args[i+1]
		}
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "SERVICE_TOKEN=submitted-value\n" {
		t.Fatalf("env file err=%v", err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("env file mode=%v err=%v", info.Mode().Perm(), err)
	}
	bad := api.WithProbeOptions(context.Background(), api.ProbeOptions{Values: map[string]string{"SERVICE_TOKEN": "bad\nvalue"}})
	if _, _, err := api.PreflightCredentialArgs(bad, imported); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("newline credential accepted: %v", err)
	}
	empty := api.WithProbeOptions(context.Background(), api.ProbeOptions{Values: map[string]string{}})
	args, cleanup, err = api.PreflightCredentialArgs(empty, imported)
	if err != nil || strings.Contains(strings.Join(args, " "), "--env") {
		t.Fatalf("empty submission injected env: %v %v", args, err)
	}
	cleanup()
	var missing context.Context
	if api.ProbeOptionsFrom(missing).AllowNetwork || api.ProbeOptionsFrom(context.Background()).AllowNetwork {
		t.Fatal("empty context has probe options")
	}
	if !api.ProbeOptionsFrom(api.WithProbeOptions(context.Background(), api.ProbeOptions{AllowNetwork: true})).AllowNetwork {
		t.Fatal("network probe option dropped")
	}
	if api.ProbeOptionsFrom(ctx).Values["SERVICE_TOKEN"] == "" {
		t.Fatal("probe values dropped")
	}
	filtered := api.WithoutNetworkNone([]string{"run", "--network", "none", "--read-only", "--network", "bridge"})
	if strings.Join(filtered, " ") != "run --read-only --network bridge" {
		t.Fatalf("network filter=%v", filtered)
	}
	if (*api.CredentialGate)(nil).Error() == "" || !strings.Contains((&api.CredentialGate{Names: []string{"SERVICE_TOKEN"}}).Error(), "SERVICE_TOKEN") {
		t.Fatal("credential gate error text")
	}
	if api.PublicPrepareError(nil) != "" || !strings.Contains(api.PublicPrepareError(errors.New("token xoxp-abcdefghijklmnop")), "[redacted]") || strings.Contains(api.PublicPrepareError(errors.New("token xoxp-abcdefghijklmnop")), "abcdefghijklmnop") {
		t.Fatal("public error redaction")
	}
	long := api.PublicPrepareError(errors.New(strings.Repeat("x", 500)))
	if len(long) != 400 {
		t.Fatalf("public error len=%d", len(long))
	}
	hints := credentialGateHints([]string{"SERVICE_TOKEN"}, [][]string{{"OTHER_TOKEN"}})
	if len(hints) != 1 || hints[0].Name != "SERVICE_TOKEN" || hints[0].AlternativeGroup != 0 {
		t.Fatalf("unmatched groups=%+v", hints)
	}
}

func TestDeliverPrepareOutcomePostsNotice(t *testing.T) {
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc"})
	var saw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		saw = r.Header.Get("Authorization") + "\n" + r.Header.Get("X-Hub-Principal") + "\n" + string(body)
		if strings.Contains(saw, "status=500") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc"})
	t.Setenv("HUB_COMMUNICATION_AUTH", "control-token")
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{}, api.Onboarding{OnboardingID: "onboard-abc"})
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{})
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc", Phase: api.PhaseAwaitingCreds, DefinitionID: "user-mcp", SourceURL: "https://github.com/example/mcp", Error: "token xoxp-abcdefghijklmnop"})
	if !strings.Contains(saw, "Bearer control-token") || !strings.Contains(saw, "alice") || !strings.Contains(saw, "onboard-abc") || strings.Contains(saw, "abcdefghijklmnop") || !strings.Contains(saw, "[redacted]") {
		t.Fatal("prepare outcome request was not redacted")
	}
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc", Error: "status=500"})
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "http://127.0.0.1:1")
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc"})
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://supervisor:8099")
	t.Setenv("HUB_SUPERVISOR_AUTH", "supervisor-token")
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc"})
	if !strings.Contains(saw, "Bearer supervisor-token") {
		t.Fatal("supervisor auth was not used")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	api.DeliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, api.Onboarding{OnboardingID: "onboard-abc"})
	if !strings.Contains(saw, "Bearer runtime-token") {
		t.Fatal("runtime auth was not used")
	}
	if api.ErrString("") != nil || api.ErrString("plain").Error() != "plain" {
		t.Fatal("prepare error string")
	}
}

func TestEnsureReadyProbeFailureKeepsProviderDetail(t *testing.T) {
	provider := mcp.NewServer(&mcp.Implementation{Name: "provider-fixture", Version: "1"}, nil)
	provider.AddTool(&mcp.Tool{Name: "browser_tabs", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "SyntaxError: Unexpected end of JSON input"}}}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return provider }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	definition := statefulContainerDefinition()
	definition.DefinitionID = "playwright-mcp"
	definition.Source.Repository = "https://github.com/microsoft/playwright-mcp"
	definition.Source.CommitSHA = "ab6d67cbf8b619338f096980806fdbe29432cc4d"
	definition.Tools = []api.ToolSpec{{Name: "browser_tabs", Effect: api.ReadEffect}}
	effective := api.EffectiveBinding{
		Definition: definition,
		WorkloadID: "work-probe",
		Connection: &api.Connection{Metadata: map[string]string{"mcp_endpoint": httpServer.URL + "/mcp"}},
	}
	backend := api.MCPBackend{
		HTTPClient: httpServer.Client(),
		AdmissionVerifier: func(context.Context, api.EffectiveBinding) (api.AdmissionReceipt, error) {
			return api.AdmissionReceipt{WorkloadID: "work-probe", State: "running", Enforced: true, ImageDigest: definition.Source.Digest, SidecarImages: definition.Workload.SidecarImages, Execution: definition.Execution}, nil
		},
		AdmissionRelease: func(context.Context, string) error { return nil },
	}
	err := backend.EnsureReady(context.Background(), effective, nil)
	if err == nil || !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("probe error=%v", err)
	}
	if !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("probe provider detail lost: %v", err)
	}
}
