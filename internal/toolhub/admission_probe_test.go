package toolhub

import (
	"context"
	"errors"
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
	originalLoader, originalList := storedArtifactLoader, localMCPToolList
	defer func() { storedArtifactLoader, localMCPToolList = originalLoader, originalList }()
	loaded := 0
	storedArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		loaded++
		return "loaded", nil
	}
	localMCPToolList = func(ctx context.Context, _ ImportedArtifact) ([]ToolSpec, error) {
		opts := probeOptionsFrom(ctx)
		if !opts.allowNetwork || opts.values["SERVICE_TOKEN"] == "" {
			t.Fatal("admission probe did not receive the submitted secret and network")
		}
		return []ToolSpec{{Name: "search", Effect: ReadEffect}}, nil
	}
	definition := statefulContainerDefinition()
	definition.Source.ArchiveDigest = "sha256:" + repeatHex('a')
	admitted, err := admitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{"SERVICE_TOKEN": "submitted"})
	if err != nil || loaded != 1 || len(admitted.Tools) != 1 || admitted.Tools[0].Name != "search" || admitted.Source.ToolContractSource != ToolContractPreflight {
		t.Fatalf("admitted=%+v loaded=%d err=%v", admitted.Tools, loaded, err)
	}
	localMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, errors.New("auth.test failed")
	}
	if _, err := admitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{"SERVICE_TOKEN": "submitted"}); err == nil {
		t.Fatal("failed tools/list registered")
	}
	storedArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return "", errors.New("archive missing")
	}
	if _, err := admitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{}); err == nil {
		t.Fatal("missing archive admitted")
	}
	definition.Source.ArchiveDigest = ""
	localMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "bad name", Effect: ReadEffect}}, nil
	}
	if _, err := admitWithSubmittedCredentials(context.Background(), t.TempDir(), definition, map[string]string{}); err == nil {
		t.Fatal("invalid tool name admitted")
	}
}

func TestPreflightCredentialArgsUsesSubmittedEnvFile(t *testing.T) {
	imported := ImportedArtifact{Definition: statefulContainerDefinition()}
	ctx := withProbeOptions(context.Background(), probeOptions{values: map[string]string{"SERVICE_TOKEN": "submitted-value"}})
	args, cleanup, err := preflightCredentialArgs(ctx, imported)
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
	bad := withProbeOptions(context.Background(), probeOptions{values: map[string]string{"SERVICE_TOKEN": "bad\nvalue"}})
	if _, _, err := preflightCredentialArgs(bad, imported); !errors.Is(err, ErrInvalid) {
		t.Fatalf("newline credential accepted: %v", err)
	}
	empty := withProbeOptions(context.Background(), probeOptions{values: map[string]string{}})
	args, cleanup, err = preflightCredentialArgs(empty, imported)
	if err != nil || strings.Contains(strings.Join(args, " "), "--env") {
		t.Fatalf("empty submission injected env: %v %v", args, err)
	}
	cleanup()
	var missing context.Context
	if probeOptionsFrom(missing).allowNetwork || probeOptionsFrom(context.Background()).allowNetwork {
		t.Fatal("empty context has probe options")
	}
	if !probeOptionsFrom(withProbeOptions(context.Background(), probeOptions{allowNetwork: true})).allowNetwork {
		t.Fatal("network probe option dropped")
	}
	if probeOptionsFrom(ctx).values["SERVICE_TOKEN"] == "" {
		t.Fatal("probe values dropped")
	}
	filtered := withoutNetworkNone([]string{"run", "--network", "none", "--read-only", "--network", "bridge"})
	if strings.Join(filtered, " ") != "run --read-only --network bridge" {
		t.Fatalf("network filter=%v", filtered)
	}
	if (*CredentialGate)(nil).Error() == "" || !strings.Contains((&CredentialGate{Names: []string{"SERVICE_TOKEN"}}).Error(), "SERVICE_TOKEN") {
		t.Fatal("credential gate error text")
	}
	if publicPrepareError(nil) != "" || !strings.Contains(publicPrepareError(errors.New("token xoxp-abcdefghijklmnop")), "[redacted]") || strings.Contains(publicPrepareError(errors.New("token xoxp-abcdefghijklmnop")), "abcdefghijklmnop") {
		t.Fatal("public error redaction")
	}
	long := publicPrepareError(errors.New(strings.Repeat("x", 500)))
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
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc"})
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
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc"})
	t.Setenv("HUB_COMMUNICATION_AUTH", "control-token")
	deliverPrepareOutcome(context.Background(), identity.Envelope{}, Onboarding{OnboardingID: "onboard-abc"})
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{})
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc", Phase: PhaseAwaitingCreds, DefinitionID: "user-mcp", SourceURL: "https://github.com/example/mcp", Error: "token xoxp-abcdefghijklmnop"})
	if !strings.Contains(saw, "Bearer control-token") || !strings.Contains(saw, "alice") || !strings.Contains(saw, "onboard-abc") || strings.Contains(saw, "abcdefghijklmnop") || !strings.Contains(saw, "[redacted]") {
		t.Fatal("prepare outcome request was not redacted")
	}
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc", Error: "status=500"})
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "http://127.0.0.1:1")
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc"})
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://supervisor:8099")
	t.Setenv("HUB_SUPERVISOR_AUTH", "supervisor-token")
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc"})
	if !strings.Contains(saw, "Bearer supervisor-token") {
		t.Fatal("supervisor auth was not used")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	deliverPrepareOutcome(context.Background(), identity.Envelope{PrincipalID: "alice"}, Onboarding{OnboardingID: "onboard-abc"})
	if !strings.Contains(saw, "Bearer runtime-token") {
		t.Fatal("runtime auth was not used")
	}
	if errString("") != nil || errString("plain").Error() != "plain" {
		t.Fatal("prepare error string")
	}
}
