package toolhub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
)

func TestPreflightCredentialGateWhenToolsListNeedsRealSecrets(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "slack-mcp-server"
	definition.Version = "0.0.2"
	definition.Credentials = nil
	definition.Tools = []ToolSpec{{Name: "mcp", Effect: ReadEffect}}
	packet := ImportedArtifact{Definition: definition, Artifact: StoredOCIArtifact{ArchiveDigest: "sha256:" + repeatHex('a')}}
	originalLoader, originalList := storedArtifactLoader, localMCPToolList
	defer func() { storedArtifactLoader, localMCPToolList = originalLoader, originalList }()
	storedArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return packet.Definition.Source.Image, nil
	}
	text := "Authentication required: Either SLACK_MCP_XOXP_TOKEN, SLACK_MCP_XOXB_TOKEN, or both SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN must be provided"
	attempts := 0
	localMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		attempts++
		return nil, fmt.Errorf("%w: %s", ErrIsolation, text)
	}
	preflighted, contract, err := PreflightImportedArtifact(context.Background(), packet, t.TempDir())
	var gate *CredentialGate
	if attempts != 2 || !errors.As(err, &gate) || !errors.Is(err, ErrIsolation) || contract.Source != "" || len(contract.Tools) != 0 {
		t.Fatalf("attempts=%d contract=%+v err=%v", attempts, contract, err)
	}
	if len(preflighted.Definition.Tools) != 0 || preflighted.Definition.Source.ToolContractSource != "" || preflighted.Definition.Source.ToolContractDigest != "" {
		t.Fatal("credential gate kept a tool contract")
	}
	required := map[string]bool{}
	for _, input := range preflighted.Definition.Credentials {
		if input.Required {
			required[input.Name] = true
		}
	}
	for _, name := range []string{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXB_TOKEN", "SLACK_MCP_XOXC_TOKEN", "SLACK_MCP_XOXD_TOKEN"} {
		if !required[name] {
			t.Fatalf("missing required %s in %+v", name, preflighted.Definition.Credentials)
		}
	}
	groups := map[string]bool{}
	for _, group := range gate.Groups {
		groups[strings.Join(group, "+")] = true
	}
	if len(gate.Groups) != 3 || !groups["SLACK_MCP_XOXC_TOKEN+SLACK_MCP_XOXD_TOKEN"] || !groups["SLACK_MCP_XOXP_TOKEN"] || !groups["SLACK_MCP_XOXB_TOKEN"] {
		t.Fatalf("groups=%v", gate.Groups)
	}

	plain := statefulContainerDefinition()
	plain.Credentials = nil
	if _, _, ok := credentialGateFrom(ImportedArtifact{Definition: plain}, errors.New("authentication required")); ok {
		t.Fatal("auth text without secret names became a credential gate")
	}
}

// mcp-atlassian answers tools/list with zero tools until JIRA_*/CONFLUENCE_*
// env config exists. The probe must promote that answered-empty response into
// a credential gate on the declared inputs instead of failing the review.
func TestCredentialGateFromAnsweredEmptyToolList(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.Tools = []ToolSpec{{Name: "mcp", Effect: ReadEffect}}
	definition.Credentials = []CredentialInput{
		{Name: "JIRA_URL", Required: true},
		{Name: "JIRA_USERNAME", Required: true},
		{Name: "JIRA_API_TOKEN", Required: true},
		{Name: "CONFLUENCE_URL", Required: true},
		{Name: "CONFLUENCE_USERNAME", Required: true},
		{Name: "CONFLUENCE_API_TOKEN", Required: true},
		{Name: "ATLASSIAN_OAUTH_CLIENT_ID"},
	}
	emptyList := fmt.Errorf("%w: MCP tools/list empty (%w): %v: %s", ErrIsolation, errEmptyToolList, error(nil), "server noise")
	gated, gate, ok := credentialGateFrom(ImportedArtifact{Definition: definition}, emptyList)
	if !ok || gate == nil {
		t.Fatal("answered-empty tools/list with required inputs did not gate")
	}
	if len(gated.Definition.Tools) != 0 || gated.Definition.Source.ToolContractDigest != "" {
		t.Fatal("empty-tools gate kept a tool contract")
	}
	names := map[string]bool{}
	for _, name := range gate.Names {
		names[name] = true
	}
	for _, name := range []string{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN", "CONFLUENCE_URL", "CONFLUENCE_USERNAME", "CONFLUENCE_API_TOKEN"} {
		if !names[name] {
			t.Fatalf("required input %s missing from gate names %v", name, gate.Names)
		}
	}
	if names["ATLASSIAN_OAUTH_CLIENT_ID"] {
		t.Fatalf("optional input joined the gate: %v", gate.Names)
	}
	groups := map[string]bool{}
	for _, group := range gate.Groups {
		groups[strings.Join(group, "+")] = true
	}
	if len(gate.Groups) != 2 || !groups["JIRA_URL+JIRA_USERNAME+JIRA_API_TOKEN"] || !groups["CONFLUENCE_URL+CONFLUENCE_USERNAME+CONFLUENCE_API_TOKEN"] {
		t.Fatalf("prefix alternatives not synthesized: %v", gate.Groups)
	}
	hints := credentialGateHints(gate.Names, gate.Groups)
	byName := map[string]CredentialHint{}
	for _, hint := range hints {
		byName[hint.Name] = hint
	}
	if len(hints) != 6 || byName["JIRA_URL"].Secret || byName["JIRA_URL"].Type != "string" || byName["JIRA_URL"].AlternativeGroup != 1 || !byName["JIRA_API_TOKEN"].Secret || byName["CONFLUENCE_API_TOKEN"].AlternativeGroup != 2 {
		t.Fatalf("gate hints lost non-secret config or groups: %+v", hints)
	}

	// A process that died before answering tools/list is not a credential gate.
	eof := fmt.Errorf("%w: MCP tools/list empty: %s", ErrIsolation, "EOF: <stderr>")
	if _, _, ok := credentialGateFrom(ImportedArtifact{Definition: definition}, eof); ok {
		t.Fatal("unanswered tools/list became a credential gate")
	}
	// Optional-only inputs cannot satisfy anything; keep the failure explicit.
	optional := statefulContainerDefinition()
	optional.Credentials = []CredentialInput{{Name: "ATLASSIAN_OAUTH_CLIENT_ID"}}
	if _, _, ok := credentialGateFrom(ImportedArtifact{Definition: optional}, emptyList); ok {
		t.Fatal("answered-empty tools/list without required inputs became a gate")
	}
	bare := statefulContainerDefinition()
	bare.Credentials = nil
	if _, _, ok := credentialGateFrom(ImportedArtifact{Definition: bare}, emptyList); ok {
		t.Fatal("answered-empty tools/list without declared inputs became a gate")
	}
}

func TestCredentialGateDefersRegistrationUntilSubmittedSecrets(t *testing.T) {
	store := NewStore()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "gate.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, ConfirmationTTL: time.Hour, FormOrigin: "http://127.0.0.1:8090"}
	preparing := Onboarding{
		Schema: SchemaVersion, OnboardingID: "onboard-gate1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: OnboardingSelfInstall, Phase: PhasePreparing, SourceURL: "https://github.com/example/slack-mcp-server",
		DefinitionID: "slack-mcp-server", DefinitionVersion: "0.0.2", Revision: 1, CreatedAt: time.Now(),
	}
	if err := store.PutOnboarding(preparing); err != nil {
		t.Fatal(err)
	}
	definition := userMCPDefinition()
	definition.DefinitionID = "slack-mcp-server"
	definition.Version = "0.0.2"
	definition.Tools = []ToolSpec{{Name: "mcp", Effect: ReadEffect}}
	definition.Credentials = []CredentialInput{
		{Name: "SLACK_MCP_XOXP_TOKEN", Required: true},
		{Name: "SLACK_MCP_XOXB_TOKEN", Required: true},
		{Name: "SLACK_MCP_XOXC_TOKEN", Required: true},
		{Name: "SLACK_MCP_XOXD_TOKEN", Required: true},
	}
	definition.ProxyEnvironment = []string{"SLACK_MCP_PROXY"}
	review := SourceReview{
		Definition: definition, AdmissionPending: true,
		AdmissionDetail: "Authentication required: Either SLACK_MCP_XOXP_TOKEN, SLACK_MCP_XOXB_TOKEN, or both SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN must be provided",
		AdmissionGroups: [][]string{{"SLACK_MCP_XOXC_TOKEN", "SLACK_MCP_XOXD_TOKEN"}, {"SLACK_MCP_XOXP_TOKEN"}, {"SLACK_MCP_XOXB_TOKEN"}},
	}
	body, err := control.acceptCredentialGate(t.Context(), aliceAuth(), preparing, review)
	instructions, _ := body["instructions"].(string)
	if err != nil || body["phase"] != PhaseAwaitingCreds || body["tools_confirmed"] != false || strings.Contains(instructions, "one token field") || !strings.Contains(instructions, "field names") {
		t.Fatalf("body=%v err=%v", body, err)
	}
	if _, err := store.Definition("slack-mcp-server", "0.0.2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("definition registered before tools/list: %v", err)
	}
	stored, err := store.onboarding(preparing.OnboardingID)
	if err != nil || stored.Definition == nil || len(stored.Definition.Tools) != 0 || !stored.AdmissionPending || stored.FormNonce == "" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if len(stored.Definition.CredentialGroups) != 3 || stored.Definition.CredentialGroups[0][0] != "SLACK_MCP_XOXC_TOKEN" {
		t.Fatalf("credential alternatives not persisted on the draft: %+v", stored.Definition.CredentialGroups)
	}
	if len(stored.Definition.ProxyEnvironment) != 1 || stored.Definition.ProxyEnvironment[0] != "SLACK_MCP_PROXY" {
		t.Fatalf("proxy environment not persisted on the draft: %+v", stored.Definition.ProxyEnvironment)
	}
	requiredBody, err := control.requiredCredentials(aliceAuth(), map[string]any{"onboarding_id": preparing.OnboardingID})
	if err != nil || !strings.Contains(fmt.Sprint(requiredBody["form_url"]), "/credentials/"+preparing.OnboardingID) {
		t.Fatalf("required=%v err=%v", requiredBody, err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/credentials/"+preparing.OnboardingID+"?nonce="+stored.FormNonce, nil)
	req.Host = "127.0.0.1"
	gateway.serveCredentials(rec, req)
	formHTML := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(formHTML, "самый короткий") || strings.Contains(formHTML, "required") || strings.Contains(formHTML, "JSON-файл Google") || strings.Contains(formHTML, "credential_choice") || strings.Contains(formHTML, "User token") || strings.Contains(formHTML, "delivery:") {
		t.Fatalf("form status=%d body=%s", rec.Code, formHTML)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'nonce-") {
		t.Fatalf("csp=%q", csp)
	}
	nonceStart := strings.Index(csp, "script-src 'nonce-") + len("script-src 'nonce-")
	nonceEnd := strings.Index(csp[nonceStart:], "'") + nonceStart
	cspNonce := csp[nonceStart:nonceEnd]
	if !strings.Contains(formHTML, `<script nonce="`+cspNonce+`">`) || !strings.Contains(formHTML, `id="wait"`) || !strings.Contains(formHTML, "Проверяю данные и запускаю MCP") {
		t.Fatalf("form progress missing or nonce mismatch: %s", formHTML)
	}
	xoxp := strings.Index(formHTML, "SLACK MCP XOXP TOKEN")
	xoxc := strings.Index(formHTML, "SLACK MCP XOXC TOKEN")
	if strings.Count(formHTML, `name="SLACK_MCP_`) != 4 || strings.Count(formHTML, "<form ") != 3 || xoxp < 0 || xoxc < xoxp || !strings.Contains(formHTML, "<details><summary>Другой набор:") {
		t.Fatalf("form did not open the shortest named set: %s", formHTML)
	}
	collapsed, err := filterSubmittedCredentials(stored.Required, map[string]string{
		"SLACK_MCP_XOXC_TOKEN": "xoxp-copied",
		"SLACK_MCP_XOXD_TOKEN": "xoxp-copied",
		"SLACK_MCP_XOXP_TOKEN": "xoxp-copied",
		"SLACK_MCP_XOXB_TOKEN": "xoxp-copied",
	})
	if err != nil || len(collapsed) != 1 || collapsed["SLACK_MCP_XOXP_TOKEN"] != "xoxp-copied" {
		t.Fatalf("copied token was not kept as one way: %+v %v", collapsed, err)
	}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/credentials/"+preparing.OnboardingID+"?nonce="+stored.FormNonce, strings.NewReader("SLACK_MCP_XOXP_TOKEN=one&SLACK_MCP_XOXB_TOKEN=two"))
	post.Host = "127.0.0.1"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	gateway.serveCredentials(rec, post)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "Данные сохранены") || !strings.Contains(rec.Body.String(), "один полный набор") {
		t.Fatalf("rejected submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	noted, err := store.onboarding(preparing.OnboardingID)
	if err != nil || !strings.Contains(noted.Error, "один полный набор") || strings.Contains(noted.Error, "one") {
		t.Fatalf("form rejection was not recorded: %+v %v", noted.Error, err)
	}
	if strings.Contains(credentialFieldMarkup(CredentialHint{Name: "SLACK_MCP_XOXP_TOKEN", AlternativeGroup: 2}), "required") {
		t.Fatal("alternative field is required")
	}
	nonce := stored.FormNonce
	if err := control.SubmitCredentials(preparing.OnboardingID, nonce, map[string]string{"SLACK_MCP_XOXC_TOKEN": "only-half"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial alternative accepted: %v", err)
	}
	control.AdmitWithCredentials = func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error) {
		return ToolDefinition{}, errors.New("auth.test failed")
	}
	if err := control.SubmitCredentials(preparing.OnboardingID, nonce, map[string]string{"SLACK_MCP_XOXP_TOKEN": "xoxp-real-token-value"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("probe failure=%v", err)
	}
	stored, _ = store.onboarding(preparing.OnboardingID)
	if stored.Phase != PhaseAwaitingCreds || stored.FormNonce != nonce || stored.Error == "" || strings.Contains(stored.Error, "xoxp-real-token-value") {
		t.Fatalf("probe failure mutated onboarding: %+v", stored)
	}
	if _, err := store.Definition("slack-mcp-server", "0.0.2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed probe registered a definition")
	}
	control.AdmitWithCredentials = func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error) {
		admitted := userMCPDefinition()
		admitted.DefinitionID = "slack-mcp-server"
		admitted.Version = "0.0.2"
		admitted.Tools = []ToolSpec{{Name: "search", Effect: ReadEffect}}
		return admitted, nil
	}
	if err := control.SubmitCredentials(preparing.OnboardingID, nonce, map[string]string{"SLACK_MCP_XOXP_TOKEN": "xoxp-real-token-value"}); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.onboarding(preparing.OnboardingID)
	admitted, err := store.Definition("slack-mcp-server", "0.0.2")
	if err != nil || stored.Phase != PhaseAwaitingConfirm || stored.AdmissionPending || stored.Error != "" || len(admitted.Tools) != 1 || admitted.Tools[0].Name != "search" {
		t.Fatalf("admitted=%+v stored=%+v err=%v", admitted.Tools, stored, err)
	}
}

func TestCredentialSubmitPageDoesNotClaimSavedSecrets(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("%w: expired form", ErrUnauthorized),
		fmt.Errorf("%w: tools/list with the submitted credentials failed", ErrIsolation),
	} {
		_, _, message := credentialSubmitPage(err)
		if strings.Contains(message, "Данные сохранены") {
			t.Fatalf("page claims a save: %s", message)
		}
	}
	if _, _, message := credentialSubmitPage(fmt.Errorf("%w: incomplete credential alternative: SLACK_MCP_XOXD_TOKEN", ErrInvalid)); !strings.Contains(message, "не все поля") || !strings.Contains(message, "SLACK MCP XOXD TOKEN") {
		t.Fatal(message)
	}
	if _, title, message := credentialSubmitPage(fmt.Errorf("%w: tools/list empty: {\"message\":\"Authentication failed - check your Slack tokens\",\"stacktrace\":\"long\"}", ErrIsolation)); title != "Проверка не прошла" || !strings.Contains(message, "Authentication failed") || strings.Contains(message, "stacktrace") || strings.Contains(message, "Данные сохранены") {
		t.Fatalf("probe page=%s %s", title, message)
	}
	if _, title, message := credentialSubmitPage(fmt.Errorf("%w: %w", errCredentialSaved, fmt.Errorf("%w: credential broker credential", ErrUnauthorized))); title != "Подключение не завершено" || !strings.Contains(message, "Данные сохранены") || strings.Contains(message, "больше не действует") || strings.Contains(message, "не сохранён") {
		t.Fatalf("saved secret page=%s %s", title, message)
	}
	if _, _, message := credentialSubmitPage(fmt.Errorf("%w: immutable definition slack-mcp-server@0.0.2", ErrConflict)); strings.Contains(message, "Данные сохранены") || !strings.Contains(message, "не сохранён") {
		t.Fatal(message)
	}
	if intro := credentialFormIntro([]CredentialHint{{Name: "GOOGLE_OAUTH_CREDENTIALS"}}); !strings.Contains(intro, "JSON-файл Google") {
		t.Fatal(intro)
	}
}

func admittedGateDefinition(version, tool string) ToolDefinition {
	admitted := userMCPDefinition()
	admitted.DefinitionID = "slack-mcp-server"
	admitted.Version = version
	admitted.Credentials = []CredentialInput{{Name: "SLACK_MCP_XOXP_TOKEN", Required: true}}
	admitted.Tools = []ToolSpec{{Name: tool, Effect: ReadEffect}}
	return admitted
}

func TestCredentialGateConfirmsLocalSecretWhenBrokerHasNoContract(t *testing.T) {
	store := NewStore()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "gate.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	seenLocal := false
	local := func(context.Context, EffectiveBinding) (CredentialInjection, error) {
		seenLocal = true
		return CredentialInjection{Environment: map[string]string{"TOKEN": "local"}}, nil
	}
	injector := mergeCredentialInjectors(local, func(context.Context, EffectiveBinding) (CredentialInjection, error) {
		t.Fatal("broker injector used for a contract-less admission")
		return CredentialInjection{}, nil
	}, true)
	control := &ControlPlane{
		Store: store, Secrets: secrets, Now: time.Now, ConfirmationTTL: time.Hour,
		Broker: &credentialbroker.Config{URL: "https://broker.example"},
		Ready: func(ctx context.Context, effective EffectiveBinding) error {
			if effective.Credential == nil || effective.Credential.Backend != credstore.BackendLocal || effective.Credential.BrokerGrantID != "" {
				t.Fatalf("reference=%+v", effective.Credential)
			}
			_, err := injector(ctx, effective)
			return err
		},
	}
	preparing := Onboarding{
		Schema: SchemaVersion, OnboardingID: "onboard-gate-local", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: OnboardingSelfInstall, Phase: PhasePreparing, SourceURL: "https://github.com/example/slack-mcp-server",
		DefinitionID: "slack-mcp-server", DefinitionVersion: "0.0.2", Revision: 1, CreatedAt: time.Now(),
	}
	if err := store.PutOnboarding(preparing); err != nil {
		t.Fatal(err)
	}
	definition := admittedGateDefinition("0.0.2", "channels_list")
	definition.Tools = nil
	definition.Credentials = []CredentialInput{
		{Name: "SLACK_MCP_XOXP_TOKEN", Required: true},
		{Name: "SLACK_MCP_XOXB_TOKEN", Required: true},
	}
	if _, err := control.acceptCredentialGate(t.Context(), aliceAuth(), preparing, SourceReview{
		Definition: definition, AdmissionPending: true, AdmissionDetail: "token required",
		AdmissionGroups: [][]string{{"SLACK_MCP_XOXP_TOKEN"}, {"SLACK_MCP_XOXB_TOKEN"}},
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.onboarding(preparing.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	control.AdmitWithCredentials = func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error) {
		return admittedGateDefinition("0.0.2", "channels_list"), nil
	}
	if err := control.AuthorizeCredentials(t.Context(), preparing.OnboardingID, stored.FormNonce, map[string]string{"SLACK_MCP_XOXP_TOKEN": "xoxp-real-token-value"}); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.onboarding(preparing.OnboardingID)
	if stored.Phase != PhaseEnabled || stored.BrokerCredentialID != "" || !seenLocal {
		t.Fatalf("phase=%s broker=%q local=%v", stored.Phase, stored.BrokerCredentialID, seenLocal)
	}
	contracted := admittedGateDefinition("1.0.0", "channels_list")
	contracted.DefinitionID = "contracted-mcp"
	contracted.CredentialContractID = "github-pat"
	contracted.CredentialContractRevision = 1
	if _, err := control.materializeBinding(t.Context(), aliceAuth(), Onboarding{OnboardingID: "onboard-contract", Locator: "locator-1", Required: []CredentialHint{{Name: "SLACK_MCP_XOXP_TOKEN"}}}, contracted); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "credential broker credential") {
		t.Fatalf("contracted definition skipped the broker: %v", err)
	}
}

func TestCredentialAdmitAssignsNextPatchForAnotherUser(t *testing.T) {
	store := NewStore()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "gate.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, ConfirmationTTL: time.Hour}
	open := func(id, principal string) Onboarding {
		onboarding := Onboarding{
			Schema: SchemaVersion, OnboardingID: id, PrincipalID: principal, ContextID: principal, RuntimeID: "runtime", PolicyVersion: "policy-1",
			Mode: OnboardingSelfInstall, Phase: PhasePreparing, DefinitionID: "slack-mcp-server", DefinitionVersion: "0.0.2", Revision: 1, CreatedAt: time.Now(),
		}
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
		draft := admittedGateDefinition("0.0.2", "channels_list")
		draft.Tools = nil
		auth := aliceAuth()
		auth.PrincipalID, auth.ContextID = principal, principal
		if _, err := control.acceptCredentialGate(t.Context(), auth, onboarding, SourceReview{Definition: draft, AdmissionPending: true, AdmissionDetail: "token required", AdmissionGroups: [][]string{{"SLACK_MCP_XOXP_TOKEN"}}}); err != nil {
			t.Fatal(err)
		}
		return onboarding
	}
	alice := open("onboard-alice", "alice")
	stored, _ := store.onboarding(alice.OnboardingID)
	control.AdmitWithCredentials = func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error) {
		return ToolDefinition{DefinitionID: "not valid"}, nil
	}
	if err := control.SubmitCredentials(alice.OnboardingID, stored.FormNonce, map[string]string{"SLACK_MCP_XOXP_TOKEN": "xoxp-real-token-value"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid manifest: %v", err)
	}
	stored, _ = store.onboarding(alice.OnboardingID)
	if stored.Phase != PhaseAwaitingCreds || stored.FormNonce == "" || stored.Error == "" || strings.Contains(stored.Error, "Проверяю поля") {
		t.Fatalf("register failure was not recorded: %+v", stored.Error)
	}
	control.AdmitWithCredentials = func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error) {
		return admittedGateDefinition("0.0.2", "channels_list"), nil
	}
	if err := control.SubmitCredentials(alice.OnboardingID, stored.FormNonce, map[string]string{"SLACK_MCP_XOXP_TOKEN": "xoxp-real-token-value"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Definition("slack-mcp-server", "0.0.2"); err != nil {
		t.Fatal(err)
	}
	owner, userOwned := store.userPublicationOwner("slack-mcp-server", "0.0.2")
	if !userOwned || owner != "alice" {
		t.Fatalf("alice publication=%s owned=%v", owner, userOwned)
	}
	bob := open("onboard-bob", "bob")
	bobStored, _ := store.onboarding(bob.OnboardingID)
	if err := control.SubmitCredentials(bob.OnboardingID, bobStored.FormNonce, map[string]string{"SLACK_MCP_XOXP_TOKEN": "xoxp-real-token-value"}); err != nil {
		t.Fatal(err)
	}
	bobStored, _ = store.onboarding(bob.OnboardingID)
	if bobStored.DefinitionVersion != "0.0.3" || bobStored.Phase != PhaseAwaitingConfirm {
		t.Fatalf("bob onboarding=%+v", bobStored.DefinitionVersion)
	}
	owner, userOwned = store.userPublicationOwner("slack-mcp-server", "0.0.2")
	if !userOwned || owner != "alice" {
		t.Fatalf("alice publication replaced: %s", owner)
	}
	owner, userOwned = store.userPublicationOwner("slack-mcp-server", "0.0.3")
	if !userOwned || owner != "bob" {
		t.Fatalf("bob publication=%s owned=%v", owner, userOwned)
	}
}
