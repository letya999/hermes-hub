package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// The CLI is a trusted host entry: --user is selected by the authenticated
// operator, not exposed as a model tool argument.
func runConnector(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "catalog-enable" {
		return runCatalog(args[1:])
	}
	if len(args) > 0 && args[0] == "local-controller" {
		f := flag.NewFlagSet("local-controller", flag.ContinueOnError)
		configPath := f.String("config", "", "absolute private local controller configuration")
		listen := f.String("listen", "127.0.0.1:8544", "loopback admission address")
		tokenFile := f.String("token-file", "", "protected local controller token file")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || !filepath.IsAbs(*configPath) {
			return fmt.Errorf("absolute local controller config required")
		}
		raw, err := readSecretInput(*configPath)
		if err != nil {
			return err
		}
		var config api.LocalControllerConfig
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
			return fmt.Errorf("invalid local controller config")
		}
		token := os.Getenv("HUB_TOOLHIVE_ADMISSION_TOKEN")
		if *tokenFile != "" {
			token, err = readSecretInput(*tokenFile)
			if err != nil {
				return err
			}
		}
		return api.RunLocalController(ctx, config, *listen, token)
	}
	if len(args) > 0 && args[0] == "generic-controller" {
		f := flag.NewFlagSet("generic-controller", flag.ContinueOnError)
		configPath := f.String("config", "", "absolute private generic controller configuration")
		listen := f.String("listen", "127.0.0.1:8545", "loopback admission address")
		tokenFile := f.String("token-file", "", "protected generic controller token file")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || !filepath.IsAbs(*configPath) {
			return fmt.Errorf("absolute generic controller config required")
		}
		raw, err := readSecretInput(*configPath)
		if err != nil {
			return err
		}
		var config toolhub.GenericControllerConfig
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
			return fmt.Errorf("invalid generic controller config")
		}
		token := os.Getenv("HUB_TOOLHIVE_ADMISSION_TOKEN")
		if *tokenFile != "" {
			token, err = readSecretInput(*tokenFile)
			if err != nil {
				return err
			}
		}
		return toolhub.RunGenericController(ctx, config, *listen, token)
	}
	if len(args) > 0 && args[0] == "catalog-cli" {
		f := flag.NewFlagSet("catalog-cli", flag.ContinueOnError)
		storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "absolute ToolHub store path")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || !filepath.IsAbs(*storePath) {
			return fmt.Errorf("catalog-cli requires an absolute --toolhub-store")
		}
		registry, err := toolhub.Load(*storePath)
		fresh := false
		if os.IsNotExist(err) {
			registry, err, fresh = toolhub.NewStore(), nil, true
		}
		if err != nil {
			return err
		}
		// The definitions are compiled into this binary and reviewed with the
		// release; registering them into the protected store is an explicit
		// operator act, identical for a fresh space and an upgrade.
		for _, definition := range api.CLICatalogDefinitions() {
			if err := registry.RegisterDefinition(definition); err != nil {
				return err
			}
			if err := registry.PromoteToCatalog(definition.DefinitionID, definition.Version, "operator"); err != nil {
				return err
			}
		}
		// A fresh store carries no file binding, so the mutations above stayed
		// memory-only; one explicit Save lands them.
		if fresh {
			if err := registry.Save(*storePath); err != nil {
				return err
			}
		}
		ids := make([]string, 0, 2)
		for _, definition := range api.CLICatalogDefinitions() {
			ids = append(ids, definition.DefinitionID+"@"+definition.Version)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"catalog_cli": ids})
	}
	if len(args) == 0 {
		return fmt.Errorf("connector requires prepare, connect, status, link, call, refresh, revoke, local-controller, generic-controller or catalog-cli")
	}
	f := flag.NewFlagSet("connector", flag.ContinueOnError)
	user := f.String("user", "me", "authenticated owner")
	contextID := f.String("context", "", "authenticated context")
	runtimeID := f.String("runtime", "", "runtime ID")
	policy := f.String("policy", "policy-1", "current policy revision")
	registryPath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "absolute registry path")
	secretPath := f.String("store", os.Getenv("HUB_CREDENTIAL_STORE"), "absolute ciphertext path")
	keyPath := f.String("key-file", os.Getenv("HUB_CREDENTIAL_KEY_FILE"), "external encryption key file")
	localToken := f.String("local-token-file", "", "prepare a separate protected local service token")
	connectionID := f.String("connection", "", "connection ID")
	write := f.Bool("write", false, "explicit separate provider mutation grant")
	provider := f.String("provider", "google", "google, slack or telegram")
	officialMCP := f.Bool("official-mcp", false, "use official Google Workspace remote MCP")
	product := f.String("product", "calendar", "Google Workspace product")
	callbackPort := f.Int("callback-port", 0, "fixed loopback OAuth callback port registered with Google")
	manifest := f.String("deployment-file", "", "operator Telegram image/ToolHive execution manifest")
	endpoint := f.String("endpoint", "", "private Telegram ToolHive MCP endpoint")
	team := f.String("workspace", "", "expected Slack workspace ID")
	expected := f.String("account", "", "expected Google subject ID")
	clientFile := f.String("client-file", "", "protected OAuth CLIENT_ID/CLIENT_SECRET input")
	toolName := f.String("tool", "", "projected tool name")
	input := f.String("from-file", "", "tool argument JSON input")
	onboardingID := f.String("onboarding", "", "pending onboarding ID for link/confirm")
	origin := f.String("origin", "", "human action link origin (default: HUB_PUBLIC_ORIGIN or the toolhub listener)")
	confirm := f.Bool("confirm", false, "link: also confirm an awaiting-confirmation onboarding for scripts")
	profileID := f.String("profile", "", "capability profile carrying the connectors bundle for facade verbs")
	envID := f.String("env", "dev", "capability profile environment")
	generation := f.Uint64("generation", 0, "capability profile generation")
	target := f.String("target", "", "connector selector for facade verbs")
	operationID := f.String("operation", "", "operation ID for connector status")
	query := f.String("query", "", "connector search query")
	writesOn := f.Bool("on", false, "allow-writes: enable write tools (omit to disable)")

	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected connector arguments")
	}
	if *provider != "google" && *provider != "slack" && *provider != "telegram" {
		return fmt.Errorf("unsupported connector provider")
	}
	if *contextID == "" {
		*contextID = *user
	}
	if *runtimeID == "" {
		*runtimeID = *user
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: *user, ExternalIdentityID: *user, ContextID: *contextID, RuntimeID: *runtimeID, ConversationID: "connector", DeliveryTargetID: "connector", PolicyVersion: *policy}
	if *profileID != "" {
		auth.CapabilityProfile, auth.Environment, auth.Generation = *profileID, *envID, *generation
	}
	if err := auth.Validate(*user, *contextID, *runtimeID, *policy); err != nil {
		return err
	}
	if !filepath.IsAbs(*registryPath) || !filepath.IsAbs(*secretPath) || !filepath.IsAbs(*keyPath) {
		return fmt.Errorf("connector requires absolute --toolhub-store, --store and --key-file")
	}
	registry, err := toolhub.Load(*registryPath)
	if args[0] == "connect" {
		registry, err = toolhub.LoadStaged(*registryPath)
	}
	if os.IsNotExist(err) {
		registry, err = toolhub.NewStore(), nil
	}
	if err != nil {
		return err
	}
	if args[0] == "status" && *target == "" && *operationID == "" {
		catalog, err := registry.Catalog(auth)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(catalog)
	}
	if args[0] == "link" {
		if !identity.ValidID(*onboardingID) {
			return fmt.Errorf("connector requires a valid --onboarding")
		}
		onboarding, err := registry.OnboardingFor(auth, *onboardingID)
		if err != nil {
			return err
		}
		// The link is the whole payload — scripts read it raw. No pending
		// action prints the phase instead of a dead URL.
		if link := api.ActionURL(connectorActionOrigin(*origin), onboarding); link != "" {
			fmt.Println(link)
		} else {
			fmt.Println(onboarding.Phase)
		}
		if !*confirm || onboarding.Phase != api.PhaseAwaitingConfirm {
			return nil
		}
		return connectorConfirm(ctx, registry, auth, onboarding)
	}
	// Facade verbs run the identical code path the agent connector_* tools
	// and the control listener use; the connectors capability bundle on the
	// caller's profile admits them.
	facadeTool := map[string]string{
		"list": "connector_list", "disable": "connector_disable", "enable": "connector_enable",
		"search": "connector_search", "reauth": "connector_reauth", "remove": "connector_remove",
		"install": "connector_install", "allow-writes": "connector_allow_writes",
	}[args[0]]
	if args[0] == "status" && *target != "" {
		facadeTool = "connector_status"
	}
	if facadeTool != "" {
		facadeArgs := map[string]any{}
		if *target != "" {
			facadeArgs["target"] = *target
		}
		if *query != "" {
			facadeArgs["query"] = *query
		}
		if args[0] == "allow-writes" {
			facadeArgs["on"] = *writesOn
		}
		result, err := (&toolhub.ControlPlane{Store: registry}).InvokeConnector(ctx, auth, facadeTool, facadeArgs)
		if err != nil {
			return err
		}
		if err := registry.Save(*registryPath); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if err := ensureCredentialKey(*keyPath); err != nil {
		return err
	}
	backend, err := credstore.Open(credstore.Options{Path: *secretPath, KeyFile: *keyPath})
	if err != nil {
		return err
	}
	if args[0] == "prepare" {
		if *localToken != "" {
			if !filepath.IsAbs(*localToken) || filepath.Clean(*localToken) == filepath.Clean(*keyPath) {
				return fmt.Errorf("separate absolute local token file required")
			}
			if err := ensureCredentialKey(*localToken); err != nil {
				return err
			}
		}
		if err := registry.Save(*registryPath); err != nil {
			return err
		}
		fmt.Println("Local encrypted storage prepared; no accounts connected")
		return nil
	}
	if args[0] == "call" {
		admission, err := api.ControllerAdmissionVerifierFromEnv()
		if err != nil {
			return err
		}
		raw, err := readSecretInput(*input)
		if err != nil {
			return err
		}
		var arguments map[string]any
		if json.Unmarshal([]byte(raw), &arguments) != nil {
			return fmt.Errorf("invalid tool JSON")
		}
		if err := api.RejectAuthorityArguments(arguments); err != nil {
			return err
		}
		ledger, err := audit.Open(filepath.Join(filepath.Dir(*secretPath), "audit.jsonl"))
		if err != nil {
			return err
		}
		gateway := &toolhub.Gateway{Store: registry, Backend: api.RoutingBackend{Provider: api.PersonalProviderBackend{}, MCP: api.MCPBackend{Root: os.Getenv("HUB_STATE"), Token: os.Getenv("TOOLHIVE_VMCP_TOKEN"), AdmissionVerifier: admission}},
			Injector: func(_ context.Context, e api.EffectiveBinding) (api.CredentialInjection, error) {
				env, err := api.DecryptAuthorized(backend, e)
				return api.CredentialInjection{Environment: env}, err
			},
			AuditWrite: func(_ string, fields map[string]string) error {
				event := audit.NewEvent("tool-call", auth.PrincipalID, fields["outcome"])
				event.ContextID, event.RuntimeID, event.ConnectionID, event.PolicyRevision = auth.ContextID, auth.RuntimeID, fields["connection_id"], auth.PolicyVersion
				event.ToolCallID, event.Receipt = fields["tool_call_id"], fields["receipt"]
				return ledger.Append(event)
			}}
		result, err := gateway.CallAuthorized(ctx, auth, *toolName, arguments)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result.StructuredContent)
	}
	if !identity.ValidID(*connectionID) {
		return fmt.Errorf("connector requires a valid --connection")
	}
	if args[0] == "connect" {
		if *provider == "telegram" {
			return connectTelegram(ctx, registry, backend, auth, *connectionID, *expected, *input, *manifest, *endpoint, *registryPath, *write)
		}
		if *expected == "" || *clientFile == "" {
			return fmt.Errorf("connect requires --account Google subject and --client-file")
		}
		raw, err := readSecretInput(*clientFile)
		if err != nil {
			return err
		}
		client, err := parseAssignments(raw)
		if err != nil || client["CLIENT_ID"] == "" || client["CLIENT_SECRET"] == "" {
			return fmt.Errorf("OAuth client credentials required")
		}
		if *callbackPort < 0 || *callbackPort > 65535 {
			return fmt.Errorf("invalid callback port")
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *callbackPort))
		if err != nil {
			return err
		}
		defer listener.Close()
		redirect := "http://" + listener.Addr().String() + "/callback"
		broker := oauth.NewBroker(backend, []string{redirect})
		broker.Clients[*provider] = oauth.ClientCredential{ID: client["CLIENT_ID"], Secret: client["CLIENT_SECRET"], BasicAuth: *provider == "slack"}
		scope := api.CalendarReadScope
		if *write {
			scope = api.CalendarWriteScope
		}
		scopes := []string{"openid", scope}
		if strings.Contains(*expected, "@") {
			scopes = append(scopes, "email")
		}
		if *officialMCP {
			if *provider != "google" || *write {
				return fmt.Errorf("official Workspace MCP currently requires a Google read grant; Calendar writes use the verified REST grant")
			}
			grants, err := api.GoogleWorkspaceScopes(*product)
			if err != nil {
				return err
			}
			scopes = append([]string{"openid", "email"}, grants...)
		}
		accessType, prompt := "offline", "consent"
		if *provider == "slack" {
			if *team == "" {
				return fmt.Errorf("slack connect requires --workspace")
			}
			scopes = api.SlackScopes(*write)
			accessType, prompt = "", ""
		}
		start, err := broker.StartAuth(oauth.AuthRequest{Principal: *user, Context: *contextID, Connection: *connectionID, Provider: *provider, Redirect: redirect, Scopes: scopes, AccessType: accessType, Prompt: prompt})
		if err != nil {
			return err
		}
		fmt.Println(start.AuthorizeURL)
		completed := make(chan error, 1)
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" || r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			meta, err := broker.HandleCallback(*user, *contextID, *connectionID, r.URL.Query().Get("state"), r.URL.Query().Get("code"), redirect)
			if err == nil {
				if *provider == "slack" {
					err = bindSlack(r.Context(), registry, backend, auth, *connectionID, *team, *expected, meta.Locator, *write)
				} else {
					selected := ""
					if *officialMCP {
						selected = *product
					}
					err = bindGoogle(r.Context(), registry, backend, auth, *connectionID, *expected, meta.Locator, *write, selected)
				}
			}
			if err != nil {
				if meta.Locator != "" {
					_ = backend.Delete(meta.Locator, auth.PrincipalID)
				}
				http.Error(w, "Connection rejected", http.StatusForbidden)
			} else {
				err = registry.Save(*registryPath)
				if err != nil {
					_ = backend.Delete(meta.Locator, auth.PrincipalID)
					http.Error(w, "Connection not saved; restart setup", http.StatusConflict)
				} else {
					_, _ = io.WriteString(w, "Connected. Close this window.")
				}
			}
			// Flush before signalling completion: the deferred server.Close
			// would otherwise drop the buffered response and the browser sees
			// a closed connection instead of the confirmation page.
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case completed <- err:
			default:
			}
		})}
		defer server.Close()
		go func() { _ = server.Serve(listener) }()
		select {
		case err := <-completed:
			if err != nil {
				return err
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Minute):
			return fmt.Errorf("OAuth callback expired")
		}
	}
	// Serialize token lifecycle across CLI processes. Connect snapshot saves
	// separately reject stale writers at the registry boundary.
	lockPath := *registryPath + ".connection-" + api.CredentialReferenceID(*connectionID, 0) + ".lock"
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink lifecycle lock denied")
	}
	lifecycleLock := flock.New(lockPath)
	locked, err := lifecycleLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("connection lifecycle busy")
	}
	defer lifecycleLock.Unlock()
	c, ref, err := registry.OwnedConnection(auth, *connectionID)
	if err != nil {
		return err
	}
	connectionProvider := "google"
	if c.DefinitionID == "slack-data-read" || c.DefinitionID == "slack-data-write" {
		connectionProvider = "slack"
	} else if strings.HasPrefix(c.DefinitionID, "telegram-account-") {
		connectionProvider = "telegram"
	} else if c.DefinitionID != "google-calendar-read" && c.DefinitionID != "google-calendar-write" && !strings.HasPrefix(c.DefinitionID, "google-workspace-") {
		return fmt.Errorf("unsupported personal connection")
	}
	switch args[0] {
	case "revoke":
		// Cut authorization before contacting the provider; an outage cannot
		// leave a locally revoked connection usable.
		if err := registry.SetConnectionStatus(c.ConnectionID, api.RevokedStatus); err != nil {
			return err
		}
		if err := registry.Save(*registryPath); err != nil {
			return err
		}
		values, err := backend.Get(ref.Locator, c.Owner.ID)
		if err != nil {
			return err
		}
		if err := backend.SetStatus(ref.Locator, c.Owner.ID, credstore.StatusRevoked); err != nil {
			return err
		}
		if connectionProvider == "telegram" {
			fmt.Println("locally revoked; terminate the imported session in Telegram Devices to invalidate it at the provider")
			return nil
		}
		if connectionProvider == "slack" {
			if err := (api.SlackBackend{}).Revoke(ctx, values["ACCESS_TOKEN"]); err != nil {
				return fmt.Errorf("locally revoked; slack provider cleanup failed")
			}
			fmt.Println("revoked")
			return nil
		}
		form := url.Values{"token": {values["ACCESS_TOKEN"]}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/revoke", strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("locally revoked; provider revoke unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("locally revoked; provider revoke HTTP %d", resp.StatusCode)
		}
		fmt.Println("revoked")
		return nil
	case "refresh":
		if connectionProvider == "telegram" {
			return fmt.Errorf("telegram sessions do not use OAuth refresh; reconnect with a new protected session input")
		}
		if c.Status != api.ActiveStatus {
			return api.ErrRevoked
		}
		if *clientFile == "" {
			return fmt.Errorf("refresh requires --client-file")
		}
		raw, err := readSecretInput(*clientFile)
		if err != nil {
			return err
		}
		client, err := parseAssignments(raw)
		if err != nil || client["CLIENT_ID"] == "" || client["CLIENT_SECRET"] == "" {
			return fmt.Errorf("OAuth client credentials required")
		}
		broker := oauth.NewBroker(backend, nil)
		broker.Clients[connectionProvider] = oauth.ClientCredential{ID: client["CLIENT_ID"], Secret: client["CLIENT_SECRET"], BasicAuth: connectionProvider == "slack"}
		if _, err := broker.Refresh(c.Owner.ID, c.ConnectionID, connectionProvider); err != nil {
			_ = registry.MarkDegraded(c.ConnectionID)
			return err
		}
		fmt.Println("refreshed")
		return nil
	default:
		return fmt.Errorf("unknown connector operation %q", args[0])
	}
}

// connectorActionOrigin picks where human action links resolve: the explicit
// flag, the operator-published origin, or the toolhub listener's loopback.
func connectorActionOrigin(flag string) string {
	if strings.TrimSpace(flag) != "" {
		return strings.TrimRight(strings.TrimSpace(flag), "/")
	}
	if public := api.PublicOrigin(); public != "" {
		return public
	}
	return api.FormOriginForListen(os.Getenv("HUB_TOOLHUB_LISTEN"))
}

// connectorConfirm finishes an awaiting-confirmation onboarding the same way
// the /confirm page does; the stored nonce stays in the operator-side store.
func connectorConfirm(ctx context.Context, registry *toolhub.Store, auth identity.Envelope, onboarding api.Onboarding) error {
	body, err := (&toolhub.ControlPlane{Store: registry}).Invoke(ctx, auth, "confirm", map[string]any{
		"onboarding_id": onboarding.OnboardingID,
		"nonce":         onboarding.ConfirmationNonce,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(body)
}

func connectTelegram(ctx context.Context, registry *toolhub.Store, backend credstore.Backend, auth identity.Envelope, connection, expected, input, manifest, endpoint, registryPath string, write bool) error {
	id, err := strconv.ParseInt(expected, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("telegram connect requires --account numeric user ID")
	}
	if api.ValidateBackendEndpoint(endpoint) != nil || !filepath.IsAbs(os.Getenv("HUB_STATE")) {
		return fmt.Errorf("telegram requires private --endpoint and absolute HUB_STATE workload root")
	}
	raw, err := readSecretInput(input)
	if err != nil {
		return err
	}
	values, err := parseAssignments(raw)
	if err != nil {
		return err
	}
	for key := range values {
		if key != "TELEGRAM_API_ID" && key != "TELEGRAM_API_HASH" && key != "TELEGRAM_SESSION_STRING" {
			return fmt.Errorf("unexpected Telegram credential key")
		}
	}
	apiID, err := strconv.ParseInt(values["TELEGRAM_API_ID"], 10, 32)
	if err != nil || apiID <= 0 || values["TELEGRAM_API_HASH"] == "" || values["TELEGRAM_SESSION_STRING"] == "" {
		return fmt.Errorf("telegram API ID/hash and session string required")
	}
	values["TELEGRAM_ACCOUNT_ID"], values["TELEGRAM_WRITE"] = expected, strconv.FormatBool(write)
	deploymentRaw, err := readSecretInput(manifest)
	if err != nil {
		return err
	}
	var deployment api.ToolDefinition
	if json.Unmarshal([]byte(deploymentRaw), &deployment) != nil {
		return fmt.Errorf("invalid deployment manifest")
	}
	d, err := api.TelegramDefinition(deployment, write)
	if err != nil {
		return err
	}
	admission, err := api.ControllerAdmissionVerifierFromEnv()
	if err != nil {
		return err
	}
	if admission == nil {
		return fmt.Errorf("telegram requires a configured ToolHive admission controller")
	}
	locator, err := backend.NewLocator()
	if err != nil {
		return err
	}
	if err := backend.Put(locator, auth.PrincipalID, values); err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			_ = backend.Delete(locator, auth.PrincipalID)
		}
	}()
	if err := registry.RegisterDefinition(d); err != nil {
		return err
	}
	keys := []string{"TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING", "TELEGRAM_ACCOUNT_ID", "TELEGRAM_WRITE"}
	ref := api.CredentialReference{Schema: 1, CredentialRefID: api.CredentialReferenceID(connection, 1), ConnectionID: connection, Revision: 1, Backend: backend.Name(), Locator: locator, Keys: keys, Status: api.ActiveStatus}
	if err := registry.PutCredentialReference(ref); err != nil {
		return err
	}
	if err := registry.PutConnection(api.Connection{Schema: 1, ConnectionID: connection, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"telegram_account": expected, "mcp_endpoint": endpoint}}); err != nil {
		return err
	}
	binding, err := registry.Enable(auth, d.DefinitionID, d.Version)
	if err != nil {
		return err
	}
	effective, err := registry.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		return err
	}
	result, err := (api.MCPBackend{Root: os.Getenv("HUB_STATE"), Token: os.Getenv("TOOLHIVE_VMCP_TOKEN"), AdmissionVerifier: admission}).CallEnv(ctx, effective, api.ToolSpec{Name: "get_account", Effect: api.ReadEffect}, nil, values)
	if err != nil || result.IsError {
		return fmt.Errorf("telegram account verification failed")
	}
	response, _ := json.Marshal(result.Structured)
	var account struct {
		ID string `json:"account_id"`
	}
	if json.Unmarshal(response, &account) != nil || account.ID != expected {
		return fmt.Errorf("telegram account mismatch")
	}
	if err := registry.Save(registryPath); err != nil {
		return err
	}
	completed = true
	fmt.Println("Telegram account connected")
	return nil
}

func bindGoogle(ctx context.Context, registry *toolhub.Store, backend credstore.Backend, auth identity.Envelope, connection, expected, locator string, write bool, products ...string) error {
	values, err := backend.Get(locator, auth.PrincipalID)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = backend.Delete(locator, auth.PrincipalID)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+values["ACCESS_TOKEN"])
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("google account verification failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	var account struct {
		Sub      string `json:"sub"`
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if err != nil || len(raw) > 65536 || resp.StatusCode != 200 || json.Unmarshal(raw, &account) != nil || account.Sub == "" || (account.Sub != expected && (!account.Verified || !strings.EqualFold(account.Email, expected))) {
		return fmt.Errorf("google account mismatch")
	}
	scope := api.CalendarReadScope
	if write {
		scope = api.CalendarWriteScope
	}
	if (len(products) == 0 || products[0] == "") && !strings.Contains(" "+values["OAUTH_SCOPE"]+" ", " "+scope+" ") {
		return fmt.Errorf("google scope not granted")
	}
	d := api.CalendarDefinition(write)
	if len(products) > 0 && products[0] != "" {
		grants, err := api.GoogleWorkspaceScopes(products[0])
		if err != nil || write {
			return fmt.Errorf("invalid Workspace grant")
		}
		for _, grant := range grants {
			if !slices.Contains(strings.Fields(values["OAUTH_SCOPE"]), grant) {
				return fmt.Errorf("workspace scope not granted")
			}
		}
		d, err = api.DiscoverGoogleWorkspace(ctx, nil, products[0], values["ACCESS_TOKEN"])
		if err != nil {
			return err
		}
	}
	if err := registry.RegisterDefinition(d); err != nil {
		return err
	}
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connection, 1), ConnectionID: connection, Revision: 1, Backend: backend.Name(), Locator: locator, Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: api.ActiveStatus}
	if err := registry.PutCredentialReference(ref); err != nil {
		return err
	}
	if err := registry.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: connection, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"google_sub": account.Sub}}); err != nil {
		return err
	}
	if _, err := registry.Enable(auth, d.DefinitionID, d.Version); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func bindSlack(ctx context.Context, registry *toolhub.Store, backend credstore.Backend, auth identity.Envelope, connection, team, user, locator string, write bool) error {
	values, err := backend.Get(locator, auth.PrincipalID)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = backend.Delete(locator, auth.PrincipalID)
		}
	}()
	if values["OAUTH_TEAM"] != team || values["OAUTH_USER"] != user {
		return fmt.Errorf("slack OAuth identity mismatch")
	}
	if err := (api.SlackBackend{}).VerifyAccount(ctx, values["ACCESS_TOKEN"], team, user); err != nil {
		return err
	}
	for _, scope := range api.SlackScopes(write) {
		if !slices.Contains(strings.Fields(strings.ReplaceAll(values["OAUTH_SCOPE"], ",", " ")), scope) {
			return fmt.Errorf("slack scope not granted")
		}
	}
	d := api.SlackDefinition(write)
	if err := registry.RegisterDefinition(d); err != nil {
		return err
	}
	ref := api.CredentialReference{Schema: 1, CredentialRefID: api.CredentialReferenceID(connection, 1), ConnectionID: connection, Revision: 1, Backend: backend.Name(), Locator: locator, Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: api.ActiveStatus}
	if err := registry.PutCredentialReference(ref); err != nil {
		return err
	}
	if err := registry.PutConnection(api.Connection{Schema: 1, ConnectionID: connection, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"slack_team": team, "slack_user": user}}); err != nil {
		return err
	}
	if _, err := registry.Enable(auth, d.DefinitionID, d.Version); err != nil {
		return err
	}
	cleanup = false
	return nil
}
