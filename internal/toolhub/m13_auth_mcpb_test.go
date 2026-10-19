package toolhub

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func zipFixture(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, body := range members {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// S2.3: a server.json advertising several remotes must not be picked from
// silently — the ambiguity fails closed with the structured code.
func TestServerManifestMultipleRemotesAreAmbiguous(t *testing.T) {
	files := map[string][]byte{
		"server.json": []byte(`{"name":"io.acme/multi","packages":[],"remotes":[{"type":"streamable-http","url":"https://a.example/mcp"},{"type":"sse","url":"https://b.example/sse"}]}`),
	}
	if _, _, _, err := api.ResolveLocalRecipe(files, api.ArtifactSource{}); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("multiple remotes lack ambiguity sentinel: %v", err)
	}
}

// S2.2: reviewed durable state on a generic install must reach the definition —
// a Dockerfile VOLUME becomes a writable connection-state mount so servers with
// self-refreshing token files actually persist them.
func TestReviewedStateMountsBecomeDefinitionState(t *testing.T) {
	config := api.DefaultSelfInstallConfig(api.ArtifactSource{Repository: "https://github.com/acme/stateful-mcp"})
	launch := api.LaunchRecipe{
		Transport: api.ContainerMCP,
		Mounts: []api.Mount{
			{Source: "connection-state", Target: "/state"},
			{Source: "pgdata", Target: "/var/lib/pg"},   // host path — dropped
			{Source: "connection-state", Target: "rel"}, // non-absolute — dropped
		},
		Environment: map[string]string{"ACME_MODE": "prod"},
	}
	api.ApplyLaunchState(&config, launch)
	if config.RuntimeEnvironment["ACME_MODE"] != "prod" {
		t.Fatalf("static launch env lost: %+v", config.RuntimeEnvironment)
	}
	if !config.Workload.Stateful {
		t.Fatal("reviewed connection-state mount did not mark the workload stateful")
	}
	if len(config.Execution.Mounts) != 1 || config.Execution.Mounts[0] != (api.Mount{Source: "connection-state", Target: "/state"}) {
		t.Fatalf("mounts=%+v, want exactly the reviewed connection-state mount", config.Execution.Mounts)
	}
}

// S2.4: a real MCPB manifest declares its launch under server.mcp_config and
// its credential contract under user_config — parse both, resolve ${__dirname}
// to the container install dir, and project user_config references in env onto
// connection fields.
func TestMCPBManifestRealSchema(t *testing.T) {
	manifest := `{
		"manifest_version": "0.3", "name": "acme-mcpb", "version": "1.2.0",
		"server": {
			"type": "node",
			"entry_point": "server/index.js",
			"mcp_config": {
				"command": "node",
				"args": ["${__dirname}/server/index.js", "--port", "3000"],
				"env": {"ACME_API_KEY": "${user_config.api_key}", "ACME_MODE": "prod"}
			}
		},
		"user_config": {
			"api_key": {"type": "string", "title": "API key", "sensitive": true, "required": true},
			"region": {"type": "string", "required": false}
		}
	}`
	launch, connection, err := api.ParseMCPBManifest([]byte(manifest), "manifest.json")
	if err != nil {
		t.Fatalf("real mcpb manifest rejected: %v", err)
	}
	want := []string{"node", "/app/server/index.js", "--port", "3000"}
	if len(launch.Entrypoint) == 0 || !slices.Equal(append([]string{launch.Entrypoint[0]}, launch.Args...), want) {
		t.Fatalf("entrypoint argv = %v %v", launch.Entrypoint, launch.Args)
	}
	if launch.Transport != api.ContainerMCP {
		t.Fatalf("transport=%s, want container-mcp for an mcp_config bundle", launch.Transport)
	}
	if launch.Environment["ACME_MODE"] != "prod" {
		t.Fatalf("static mcp_config env lost: %+v", launch.Environment)
	}
	var key *api.ConnectionField
	for i := range connection.Fields {
		if connection.Fields[i].Name == "ACME_API_KEY" {
			key = &connection.Fields[i]
		}
	}
	if key == nil || !key.Secret || !key.Required {
		t.Fatalf("user_config api_key not projected onto ACME_API_KEY: %+v", connection.Fields)
	}
}

// A .mcpb release asset is a zip bundle: bounded extraction hands every file
// to the build context and the manifest's mcp_config becomes the entrypoint.
func TestExtractMCPBBundleAndRecipe(t *testing.T) {
	manifest := `{
		"manifest_version": "0.3", "name": "acme-mcpb", "version": "1.2.0",
		"server": {"type": "node", "entry_point": "server/index.js",
			"mcp_config": {"command": "node", "args": ["${__dirname}/server/index.js"]}}
	}`
	bundle := zipFixture(t, map[string]string{
		"manifest.json":    manifest,
		"server/index.js":  "console.log('mcp')",
		"package.json":     `{"name":"acme-mcpb"}`,
		"dir/sub/link.txt": "ok",
	})
	files, err := api.ExtractMCPBBundle(bundle)
	if err != nil {
		t.Fatalf("bundle rejected: %v", err)
	}
	if _, ok := files["manifest.json"]; !ok {
		t.Fatal("manifest missing from bundle")
	}
	hostile := zipFixture(t, map[string]string{"manifest.json": manifest, "../escape.txt": "nope"})
	if _, err := api.ExtractMCPBBundle(hostile); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("path traversal bundle admitted: %v", err)
	}
	recipe, contextBytes, err := api.GenerateMCPBRecipe(files)
	if err != nil {
		t.Fatalf("recipe generation failed: %v", err)
	}
	if len(recipe.Entrypoint) != 2 || recipe.Entrypoint[0] != "node" || recipe.Entrypoint[1] != "/app/server/index.js" {
		t.Fatalf("recipe entrypoint=%v", recipe.Entrypoint)
	}
	if len(recipe.BaseImages) != 1 || !strings.Contains(recipe.BaseImages[0], "node") {
		t.Fatalf("node bundle without node base: %+v", recipe.BaseImages)
	}
	if err := recipe.VerifyContext(contextBytes); err != nil {
		t.Fatalf("generated context fails verification: %v", err)
	}
}

// An .mcpb release asset installs through the artifact pipeline without a
// spec binary — the manifest supplies the launch — and the registered
// definition is a container-mcp connector carrying the manifest's credential
// contract.
func TestFinishCLIArtifactMCPBProducesContainerMCP(t *testing.T) {
	fix := controlCLIFixture(t, true)
	fix.control.PrepareSyncWindow = -1
	auth := aliceAuth()
	fix.control.CLIArtifacts = &CLIArtifactPipeline{
		Build: func(context.Context, api.ArtifactSource, api.ArtifactImportConfig) (api.ImportedArtifact, error) {
			return api.ImportedArtifact{
				Recipe: api.ArtifactRecipe{Entrypoint: []string{"node", "/app/server/index.js"}},
				Definition: api.ToolDefinition{
					Transport:   api.ContainerMCP,
					Credentials: []api.CredentialInput{{Name: "ACME_API_KEY", Required: true}},
					Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "mcpb", ToolHiveVersion: "v0.48.0", SidecarImages: []string{api.RestrictedBuildProxyImage}},
					Execution:   api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64, Egress: []string{"127.0.0.1"}},
					Source: api.DefinitionSource{
						Image:        "hermes-cli-artifact/acme-mcpb",
						RecipeDigest: "sha256:" + strings.Repeat("4", 64), ReviewDigest: "sha256:" + strings.Repeat("5", 64),
					},
				},
				Artifact: api.StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("1", 64), Evidence: api.OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("9", 64), ProvenanceDigest: "sha256:" + strings.Repeat("2", 64), SBOMDigest: "sha256:" + strings.Repeat("3", 64)}},
			}, nil
		},
	}
	spec := map[string]any{
		"name": "acme-mcpb", "source": "github-release:acme/bundle@1.0.0", "asset": "acme.mcpb",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	if _, err := fix.control.prepareCLIArtifact(context.Background(), auth, spec, "mcpb-1", false); err != nil {
		t.Fatalf("mcpb install denied: %v", err)
	}
	definition := fix.store.definitions[definitionKey("acme-mcpb", "1.0.0")]
	if definition.Transport != api.ContainerMCP {
		t.Fatalf("mcpb registered as %s, want container-mcp", definition.Transport)
	}
	if definition.Source.Command != "node" || !slices.Contains(definition.Source.Args, "/app/server/index.js") {
		t.Fatalf("entrypoint=%s %v", definition.Source.Command, definition.Source.Args)
	}
	if len(definition.Credentials) != 1 || definition.Credentials[0].Name != "ACME_API_KEY" {
		t.Fatalf("manifest credentials lost: %+v", definition.Credentials)
	}
}

// The release pipeline end to end up to the restricted build: fetch, sha256
// verify, unzip, manifest parse and recipe generation all run; only the docker
// call is out of unit scope and fails closed on the relative seccomp path.
func TestImportMCPBReleaseArtifactPipeline(t *testing.T) {
	manifest := `{"manifest_version":"0.3","server":{"type":"binary","entry_point":"server","mcp_config":{"command":"/app/server"}}}`
	bundle := zipFixture(t, map[string]string{"manifest.json": manifest, "server": "ELF"})
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/acme/bundle/releases/tags/1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		sum := sha256.Sum256(bundle)
		fmt.Fprintf(w, `{"tag_name":"1.0.0","assets":[{"name":"acme.mcpb","browser_download_url":%q,"digest":"sha256:%x"}]}`, server.URL+"/dl/acme.mcpb", sum)
	})
	mux.HandleFunc("/dl/acme.mcpb", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) })
	server = httptest.NewServer(mux)
	defer server.Close()
	source, err := api.ParseGitHubReleaseSource("github-release:acme/bundle@1.0.0", "*.mcpb", "", "")
	if err != nil {
		t.Fatal(err)
	}
	build := api.RestrictedBuildConfig{SeccompPath: "relative", ArtifactDirectory: t.TempDir(), MaxArtifactBytes: 1 << 20}
	_, err = ImportMCPBReleaseArtifact(context.Background(), source, cliImportConfig(), build, server.Client(), server.URL)
	if !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("expected builder-path rejection after full mcpb pipeline, got %v", err)
	}
	// The cli-release entry point routes a .mcpb asset onto the bundle path —
	// a spec with no binary entrypoint still reaches fetch/verify, so a
	// digest-mismatched payload surfaces ErrStale, not the cli entrypoint gate.
	badServer := httptestNewRelease(t, "2.0.0", []string{"bad.mcpb"}, "not-a-zip")
	source.Asset = "*.mcpb"
	mismatch, err := api.ParseGitHubReleaseSource("github-release:o/r@2.0.0", "*.mcpb", "sha256:"+strings.Repeat("0", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	noEntry := cliImportConfig()
	noEntry.Entrypoint = nil
	_, err = ImportCLIReleaseArtifact(context.Background(), mismatch, noEntry, build, badServer.Client(), badServer.URL)
	if !errors.Is(err, api.ErrStale) {
		t.Fatalf(".mcpb asset did not reach fetch/verify (binary entrypoint gate won?): %v", err)
	}
}

// S2.1: an auth-required remote endpoint that advertises OAuth protected
// resource metadata must onboard through the browser flow — PRM discovery,
// RFC7591 registration, RFC8707 audience on authorize+token, PKCE — then the
// minted bearer reaches the endpoint on every call. Resubmitting the same
// prepared id stays idempotent.
func TestRemoteOAuthGenericFlow(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	alice := fix.session(t, aliceToken)

	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatalf("oauth-advertising endpoint prepare rejected: %v", err)
	}
	if prepared["phase"] != api.PhaseAwaitingOAuth {
		t.Fatalf("prepare phase=%v want awaiting-oauth", prepared["phase"])
	}
	onboardingID := prepared["onboarding_id"].(string)

	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": onboardingID}); err != nil {
		t.Fatalf("status: %v", err)
	}
	rawURL := providerAuthURLOf(t, fix.store, onboardingID)
	if rawURL == "" {
		t.Fatal("onboarding lacks provider authorization url")
	}
	authURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("resource") == "" || authURL.Query().Get("client_id") != "dcr-client" {
		t.Fatalf("authorize URL lacks RFC8707 resource or DCR client_id: %s", rawURL)
	}
	if err := fix.control.completePreparedOAuth(context.Background(), authURL.Query().Get("state"), "oauth-code"); err != nil {
		t.Fatalf("callback: %v", err)
	}
	onboarding := mustOnboarding(t, fix.store, onboardingID)
	if onboarding.Phase != api.PhaseEnabled {
		t.Fatalf("post-callback phase=%s err=%s", onboarding.Phase, onboarding.Error)
	}
	listed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projectedName := ""
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, "search") && strings.HasPrefix(tool.Name, "hub-remote-") {
			projectedName = tool.Name
		}
	}
	if projectedName == "" {
		t.Fatal("oauth-admitted remote tool not projected")
	}
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{"q": "x"}}}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got := fixture.lastAuth.Load(); got != "Bearer oauth-at-1" {
		t.Fatalf("endpoint saw Authorization %q, want minted bearer", got)
	}
}

// A remote-mcp onboarding parked in awaiting-oauth must mint an authorization
// URL through the discovered provider — status drives the handoff start.
func TestRemoteOAuthStatusHandsAuthorizationURL(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil || prepared["phase"] != api.PhaseAwaitingOAuth {
		t.Fatalf("prepare=%v err=%v", prepared, err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	rawURL := providerAuthURLOf(t, fix.store, prepared["onboarding_id"])
	if rawURL == "" {
		t.Fatal("onboarding lacks provider authorization url")
	}
	authURL, _ := url.Parse(rawURL)
	scope := authURL.Query().Get("scope")
	if authURL.Query().Get("client_id") != "dcr-client" || authURL.Query().Get("resource") == "" || !strings.Contains(scope, "mcp:read") || strings.Contains(scope, "mcp:admin") {
		t.Fatalf("authorize URL incomplete: %s", rawURL)
	}
}

// After enable the minted bearer is what the endpoint sees; when the endpoint
// stops accepting it, the connection's stored refresh token must mint a new
// one and the call retried — durable refresh, not a re-onboard.
func TestRemoteOAuthRefreshRetriesCall(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	fix.gateway.OAuthRefresh = remoteOAuthRefresh(fix.secrets, fix.control.OAuth, fix.store)
	alice := fix.session(t, aliceToken)

	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, fix.store, prepared["onboarding_id"]))
	if err := fix.control.completePreparedOAuth(context.Background(), authURL.Query().Get("state"), "oauth-code"); err != nil {
		t.Fatalf("callback: %v", err)
	}
	listed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projectedName := ""
	for _, tool := range listed.Tools {
		if strings.HasPrefix(tool.Name, "hub-remote-") {
			projectedName = tool.Name
		}
	}
	// Restart the hub: a fresh broker with empty provider/client memory over
	// the same secrets must still refresh — durable state lives in the
	// encrypted locator, not in-process maps.
	restarted := fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	fix.control.OAuth = restarted
	fix.gateway.OAuthRefresh = remoteOAuthRefresh(fix.secrets, restarted, fix.store)
	// The AS rotates the accepted token; the next call must refresh and retry.
	fixture.accepted.Store("oauth-at-2")
	invoked, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{"q": "x"}}})
	if err != nil || invoked.IsError {
		t.Fatalf("refresh-retry invoke=%+v err=%v", invoked, err)
	}
	if got := fixture.lastAuth.Load(); got != "Bearer oauth-at-2" {
		t.Fatalf("endpoint saw %q after rotation, want refreshed bearer", got)
	}
}

// Two bindings on OAuth connections of the same endpoint may refresh
// concurrently; the restore path must serialize its provider/client writes
// against the broker's own locked reads.
func TestRemoteOAuthRefreshConcurrentRestore(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, fix.store, prepared["onboarding_id"]))
	if err := fix.control.completePreparedOAuth(context.Background(), authURL.Query().Get("state"), "oauth-code"); err != nil {
		t.Fatalf("callback: %v", err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	caller := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", ConversationID: "alice", DeliveryTargetID: "alice", PolicyVersion: onboarding.PolicyVersion}
	connection, reference, err := fix.store.OwnedConnection(caller, onboarding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	// A restarted broker has no provider/client memory: both restores race.
	broker := fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	refresh := remoteOAuthRefresh(fix.secrets, broker, fix.store)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding := api.EffectiveBinding{
				Binding:    api.ToolBinding{PrincipalID: "alice"},
				Connection: &connection,
				Credential: &reference,
			}
			_, _ = refresh(context.Background(), binding)
		}()
	}
	wg.Wait()
}

// onboardRemoteOAuth runs the full prepare→authorize→callback pipeline and
// returns the enabled connection plus its credential reference.
func onboardRemoteOAuth(t *testing.T, fixture *remoteOAuthFixture, fix *remoteControlFixture) (api.Connection, api.CredentialReference) {
	t.Helper()
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, fix.store, prepared["onboarding_id"]))
	if err := fix.control.completePreparedOAuth(context.Background(), authURL.Query().Get("state"), "oauth-code"); err != nil {
		t.Fatalf("callback: %v", err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	caller := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", ConversationID: "alice", DeliveryTargetID: "alice", PolicyVersion: onboarding.PolicyVersion}
	connection, reference, err := fix.store.OwnedConnection(caller, onboarding.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	return connection, reference
}

// S6.2: the durable (connection, provider) refresh lease serializes
// concurrent expiry — N callers observe one AS exchange, the rest join on
// the rotated token instead of double-spending it.
func TestRefreshLeaseSingleExchangeUnderConcurrency(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	connection, reference := onboardRemoteOAuth(t, fixture, fix)
	broker := fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	refresh := remoteOAuthRefresh(fix.secrets, broker, fix.store)
	binding := api.EffectiveBinding{
		Binding:    api.ToolBinding{PrincipalID: "alice"},
		Connection: &connection,
		Credential: &reference,
	}
	before := fixture.tokenHits.Load()
	var wg sync.WaitGroup
	results := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshed, err := refresh(context.Background(), binding)
			if err != nil {
				t.Errorf("refresh: %v", err)
				return
			}
			results <- refreshed
		}()
	}
	wg.Wait()
	close(results)
	for refreshed := range results {
		if !refreshed {
			t.Fatal("joiner reported no refresh")
		}
	}
	if hits := fixture.tokenHits.Load() - before; hits != 1 {
		t.Fatalf("concurrent refreshes hit the AS %d times, want 1", hits)
	}
}

// S6.2: a connection revoked while its token exchange is in flight keeps
// nothing — the freshly minted token set is purged and the lease released.
func TestRefreshLeaseRevokeWins(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fixture.refreshHold = make(chan struct{})
	fix := newRemoteControlFixture(t)
	connection, reference := onboardRemoteOAuth(t, fixture, fix)
	broker := fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	refresh := remoteOAuthRefresh(fix.secrets, broker, fix.store)
	binding := api.EffectiveBinding{
		Binding:    api.ToolBinding{PrincipalID: "alice"},
		Connection: &connection,
		Credential: &reference,
	}
	done := make(chan error, 1)
	go func() {
		refreshed, err := refresh(context.Background(), binding)
		if refreshed {
			done <- fmt.Errorf("refreshed a revoked connection")
			return
		}
		done <- err
	}()
	for i := 0; i < 200 && !fixture.refreshEntered.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !fixture.refreshEntered.Load() {
		t.Fatal("refresh exchange never started")
	}
	if err := fix.store.SetConnectionStatus(connection.ConnectionID, api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	close(fixture.refreshHold)
	if err := <-done; !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("in-flight refresh after revoke: %v", err)
	}
	if _, err := fix.secrets.Get(reference.Locator, "alice"); err == nil {
		t.Fatal("revoked connection kept the minted token set")
	}
	// Revoked at entry too: a fresh attempt must not reach the AS.
	before := fixture.tokenHits.Load()
	if refreshed, err := refresh(context.Background(), binding); refreshed || !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("revoked connection refreshed: refreshed=%v err=%v", refreshed, err)
	}
	if fixture.tokenHits.Load() != before {
		t.Fatal("revoked connection reached the token endpoint")
	}
}

// S6.2: refresh is invoked from inside the admission callback, which runs
// under the registry read lock — the lease must never take that lock, or a
// rejected token wedges every later caller forever.
func TestRefreshLeaseAcquireInsideAdmissionLock(t *testing.T) {
	as := oauth.NewFixture()
	defer as.Close()
	store, auth, binding := seededStore(t)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "lease.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := json.Marshal(as.Provider())
	locator := effective.Credential.Locator
	if err := secrets.Put(locator, "alice", map[string]string{
		"REFRESH_TOKEN":      "rt-old",
		"OAUTH_PROVIDER":     "fixture",
		"OAUTH_CONNECTION":   "google-work",
		"OAUTH_PROVIDER_DOC": string(prov),
	}); err != nil {
		t.Fatal(err)
	}
	broker := oauth.NewBroker(secrets, nil)
	broker.HTTP = as.Server.Client()
	refresh := remoteOAuthRefresh(secrets, broker, store)
	done := make(chan error, 1)
	go func() {
		done <- store.Authorize(auth, binding.ToolBindingID, func(effective api.EffectiveBinding) error {
			refreshed, err := refresh(context.Background(), effective)
			if err != nil {
				return err
			}
			if !refreshed {
				return fmt.Errorf("refresh reported no rotation")
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if as.RefreshCount("rt-old") != 1 {
			t.Fatalf("refresh token exchanged %d times, want 1", as.RefreshCount("rt-old"))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("refresh lease deadlocked under the admission read lock")
	}
}

// S6.2: revoke landing while a refresh exchange is in flight — and the
// refresh itself is running inside the admission read lock — must still win.
// A status read that takes the registry lock deadlocks against the queued
// writer; the lease path reads the lock-free mirror instead.
type holdRefreshTransport struct {
	base    http.RoundTripper
	hold    chan struct{}
	entered *atomic.Bool
}

func (t holdRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/token") {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if strings.Contains(string(body), "grant_type=refresh_token") {
			t.entered.Store(true)
			<-t.hold
		}
	}
	return t.base.RoundTrip(r)
}

func TestRefreshLeaseRevokeWinsInsideAdmission(t *testing.T) {
	as := oauth.NewFixture()
	defer as.Close()
	store, auth, binding := seededStore(t)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "lease-revoke.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := json.Marshal(as.Provider())
	if err := secrets.Put(effective.Credential.Locator, "alice", map[string]string{
		"REFRESH_TOKEN":      "rt-old",
		"OAUTH_PROVIDER":     "fixture",
		"OAUTH_CONNECTION":   "google-work",
		"OAUTH_PROVIDER_DOC": string(prov),
	}); err != nil {
		t.Fatal(err)
	}
	broker := oauth.NewBroker(secrets, nil)
	transport := holdRefreshTransport{base: as.Server.Client().Transport, hold: make(chan struct{}), entered: &atomic.Bool{}}
	broker.HTTP = &http.Client{Transport: transport}
	refresh := remoteOAuthRefresh(secrets, broker, store)
	done := make(chan error, 1)
	go func() {
		done <- store.Authorize(auth, binding.ToolBindingID, func(effective api.EffectiveBinding) error {
			_, err := refresh(context.Background(), effective)
			return err
		})
	}()
	for i := 0; i < 200 && !transport.entered.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !transport.entered.Load() {
		t.Fatal("refresh exchange never started")
	}
	// The writer queues behind the admission read lock — it cannot land until
	// the call returns, but its pending state must not deadlock the in-flight
	// refresh's status checks.
	revoked := make(chan error, 1)
	go func() { revoked <- store.SetConnectionStatus("google-work", api.RevokedStatus) }()
	for i := 0; i < 200; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	close(transport.hold)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("refresh under a queued writer: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("refresh deadlocked against a queued revocation writer")
	}
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("revocation never applied after the refresh returned")
	}
}

// S6.2: a store re-loaded from disk feeds the status mirror — a restarted
// hub must lease against the persisted connection, not report NotFound.
func TestRefreshLeaseAfterStoreReload(t *testing.T) {
	as := oauth.NewFixture()
	defer as.Close()
	store, auth, binding := seededStore(t)
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "lease-reload.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := loaded.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := json.Marshal(as.Provider())
	if err := secrets.Put(effective.Credential.Locator, "alice", map[string]string{
		"REFRESH_TOKEN":      "rt-old",
		"OAUTH_PROVIDER":     "fixture",
		"OAUTH_CONNECTION":   "google-work",
		"OAUTH_PROVIDER_DOC": string(prov),
	}); err != nil {
		t.Fatal(err)
	}
	broker := oauth.NewBroker(secrets, nil)
	broker.HTTP = as.Server.Client()
	refresh := remoteOAuthRefresh(secrets, broker, loaded)
	refreshed, err := refresh(context.Background(), effective)
	if err != nil || !refreshed {
		t.Fatalf("refresh on reloaded store: refreshed=%v err=%v", refreshed, err)
	}
}

// S6.2: Reload re-syncs the status mirror — a revoke persisted by another
// writer must close the refresh path on the live store too.
func TestRefreshLeaseReloadSeesRevoke(t *testing.T) {
	as := oauth.NewFixture()
	defer as.Close()
	store, auth, binding := seededStore(t)
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	live, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// The effective binding resolves while the connection is still active —
	// the revoke then lands on disk like an in-flight caller would see it.
	effective, err := live.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	revoker, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := revoker.SetConnectionStatus("google-work", api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := revoker.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := live.Reload(); err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "lease-revoke.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := json.Marshal(as.Provider())
	if err := secrets.Put(effective.Credential.Locator, "alice", map[string]string{
		"REFRESH_TOKEN":      "rt-old",
		"OAUTH_PROVIDER":     "fixture",
		"OAUTH_CONNECTION":   "google-work",
		"OAUTH_PROVIDER_DOC": string(prov),
	}); err != nil {
		t.Fatal(err)
	}
	broker := oauth.NewBroker(secrets, nil)
	broker.HTTP = as.Server.Client()
	refresh := remoteOAuthRefresh(secrets, broker, live)
	refreshed, err := refresh(context.Background(), effective)
	if err == nil || refreshed {
		t.Fatalf("refresh after reloaded revoke: refreshed=%v err=%v", refreshed, err)
	}
}

// S6.2 unit: an expired lease is taken over by the next caller — the stale
// holder's release must not drop the successor's claim.
func TestRefreshLeaseExpiryTakeover(t *testing.T) {
	store, _, _ := seededStore(t)
	now := time.Now()
	first, acquired, err := store.AcquireRefreshLease("google-work", "fixture", time.Second, now)
	if err != nil || !acquired {
		t.Fatalf("first acquire: acquired=%v err=%v", acquired, err)
	}
	second, acquired, err := store.AcquireRefreshLease("google-work", "fixture", time.Second, now.Add(2*time.Second))
	if err != nil || !acquired {
		t.Fatalf("takeover after expiry: acquired=%v err=%v", acquired, err)
	}
	if second.Holder == first.Holder {
		t.Fatal("takeover reused the expired holder")
	}
	if err := store.ReleaseRefreshLease("google-work", "fixture", first.Holder); err != nil {
		t.Fatal(err)
	}
	if !store.refreshLeaseHeld("google-work", "fixture", now.Add(2500*time.Millisecond)) {
		t.Fatal("stale holder release dropped the live lease")
	}
}

// S6.2 unit: the lease ledger survives a store reload — a restarted hub still
// sees the in-flight claim instead of double-spending the refresh token.
func TestRefreshLeaseDurableAcrossRestart(t *testing.T) {
	store, _, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	live, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := live.AcquireRefreshLease("google-work", "fixture", time.Hour, time.Now())
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	next, acquired, err := restarted.AcquireRefreshLease("google-work", "fixture", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if acquired || next.Holder != lease.Holder {
		t.Fatalf("restart lost the lease: acquired=%v holder=%q want %q", acquired, next.Holder, lease.Holder)
	}
}

// S6.2 unit: missing and degraded connections can never take the lease.
func TestRefreshLeaseFailsClosed(t *testing.T) {
	store, _, _ := seededStore(t)
	if _, _, err := store.AcquireRefreshLease("ghost-conn-0000", "fixture", time.Second, time.Now()); err == nil {
		t.Fatal("lease acquired for a missing connection")
	}
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcquireRefreshLease("google-work", "fixture", time.Second, time.Now()); err == nil {
		t.Fatal("lease acquired for a degraded connection")
	}
}

// A remote endpoint may be declared shared at prepare time: scope=shared
// requires the install_shared control grant and registers a Shared-class,
// catalog-visible definition that a shared-credential policy can bind.
func TestRemoteSharedScopeGate(t *testing.T) {
	fixture := newRemoteMCPFixture(t, "")
	fix := newRemoteControlFixture(t)
	// carol holds every control operation except install_shared, plus the
	// owner-side self-install grant: the shared gate must still deny.
	carol := identity.TelegramEnvelope("carol", 9, "runtime", "policy-1")
	for _, op := range api.ControlOperations {
		if op != "install_shared" {
			grantTestControlOperations(t, fix.store, "carol", op)
		}
	}
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "carol", "", "")); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"remote_url": fixture.endpoint(), "scope": "shared"}
	if _, err := fix.control.Invoke(context.Background(), carol, "prepare_source", args); err == nil {
		t.Fatal("shared remote install without install_shared admitted")
	}
	if err := putGrant(t, fix.store, api.OperatorControlGrant("carol", "install_shared")); err != nil {
		t.Fatal(err)
	}
	prepared, err := fix.control.Invoke(context.Background(), carol, "prepare_source", args)
	if err != nil {
		t.Fatalf("operator shared remote prepare denied: %v", err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	definition := onboarding.Definition
	if definition == nil || definition.Workload.Class != api.Shared || !definition.Workload.Sharable {
		t.Fatalf("definition workload=%+v, want shared+sharable", definition)
	}
	if pub := fix.store.publications[definitionKey(definition.DefinitionID, definition.Version)]; pub.Visibility != api.PublicationCatalog {
		t.Fatalf("publication=%+v, want catalog", pub)
	}
}

// scope=shared also applies to repository installs: the reviewed definition
// promotes to a Shared-class catalog record a shared-credential policy can
// bind — and the gate is install_shared, never self-install.
func TestSourceSharedScopeGate(t *testing.T) {
	fix := newControlFixture(t, fixtureReviewer(catalogReadDefinition()))
	fix.control.SourceResolver = func(_ context.Context, raw string) (api.ArtifactSource, error) {
		return api.ArtifactSource{Repository: raw, CommitSHA: "0123456789abcdef0123456789abcdef01234567"}, nil
	}
	carol := identity.TelegramEnvelope("carol", 9, "runtime", "policy-1")
	for _, op := range api.ControlOperations {
		if op != "install_shared" {
			grantTestControlOperations(t, fix.store, "carol", op)
		}
	}
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "carol", "", "")); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"source": "https://github.com/acme/widgets", "scope": "shared"}
	if _, err := fix.control.Invoke(context.Background(), carol, "prepare_source", args); err == nil {
		t.Fatal("shared repo install without install_shared admitted")
	}
	if err := putGrant(t, fix.store, api.OperatorControlGrant("carol", "install_shared")); err != nil {
		t.Fatal(err)
	}
	prepared, err := fix.control.Invoke(context.Background(), carol, "prepare_source", args)
	if err != nil {
		t.Fatalf("operator shared repo prepare denied: %v", err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	stored, err := fix.store.Definition(onboarding.DefinitionID, onboarding.DefinitionVersion)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Workload.Class != api.Shared || !stored.Workload.Sharable {
		t.Fatalf("stored definition class=%s sharable=%v", stored.Workload.Class, stored.Workload.Sharable)
	}
	if pub := fix.store.publications[definitionKey(stored.DefinitionID, stored.Version)]; pub.Visibility != api.PublicationCatalog {
		t.Fatalf("publication=%+v, want catalog", pub)
	}
}

// A shared remote OAuth connector: alice signs in once under scope=shared,
// the operator binds her token locator to a policy, bob enables the catalog
// definition and calls it — and a mid-life token rotation refreshes through
// alice's locator on bob's call, no re-onboarding.
func TestRemoteSharedOAuthMemberCallsRefresh(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	fix.gateway.OAuthRefresh = remoteOAuthRefresh(fix.secrets, fix.control.OAuth, fix.store)
	alice := fix.session(t, aliceToken)

	args := map[string]any{"remote_url": fixture.endpoint(), "scope": "shared"}
	if err := putGrant(t, fix.store, api.OperatorControlGrant("alice", "install_shared")); err != nil {
		t.Fatal(err)
	}
	prepared, err := callControl(t, alice, "prepare_source", args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, fix.store, prepared["onboarding_id"]))
	if err := fix.control.completePreparedOAuth(context.Background(), authURL.Query().Get("state"), "oauth-code"); err != nil {
		t.Fatalf("callback: %v", err)
	}
	owner := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	if owner.Locator == "" {
		t.Fatal("oauth onboarding stored no credential locator")
	}
	// Operator binds alice's token set to all members of the shared def.
	policy := api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "policy-remote", DefinitionID: owner.DefinitionID, Locator: owner.Locator, StoreOwner: "alice", Principals: []string{"alice", "bob"}, IssuedBy: "operator", Status: api.ActiveStatus}
	if err := fix.store.PutSharedCredentialPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantCatalogDefault, "bob", "", "")); err != nil {
		t.Fatal(err)
	}
	bob := fix.session(t, bobToken)
	bprepared, err := callControl(t, bob, "prepare_source", map[string]any{"definition_id": owner.DefinitionID, "version": "1.0.0"})
	if err != nil {
		t.Fatalf("bob prepare: %v", err)
	}
	bobOnb := mustOnboarding(t, fix.store, bprepared["onboarding_id"].(string))
	if err := fix.control.SubmitCredentials(bobOnb.OnboardingID, bobOnb.FormNonce, map[string]string{"ACCESS_TOKEN": "member-dummy"}); err != nil {
		t.Fatalf("bob submit: %v", err)
	}
	if _, err := callControl(t, bob, "status", map[string]any{"onboarding_id": bobOnb.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, bob, "confirm", map[string]any{"onboarding_id": bobOnb.OnboardingID, "nonce": confirmNonceOf(t, fix.store, bobOnb.OnboardingID)}); err != nil {
		t.Fatalf("bob confirm: %v", err)
	}
	if _, err := callControl(t, bob, "enable", map[string]any{"onboarding_id": bobOnb.OnboardingID}); err != nil {
		t.Fatalf("bob enable: %v", err)
	}
	listed, err := bob.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projectedName := ""
	for _, tool := range listed.Tools {
		if strings.HasPrefix(tool.Name, "hub-remote-") {
			projectedName = tool.Name
		}
	}
	if projectedName == "" {
		t.Fatal("shared remote tool not projected for member")
	}
	if _, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{"q": "x"}}}); err != nil {
		t.Fatalf("bob invoke: %v", err)
	}
	if got := fixture.lastAuth.Load(); got != "Bearer oauth-at-1" {
		t.Fatalf("endpoint saw %q for member call, want shared bearer", got)
	}
	// Rotation: bob's next call must refresh alice's locator and retry once.
	fixture.accepted.Store("oauth-at-2")
	if _, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{"q": "x"}}}); err != nil {
		t.Fatalf("bob refresh-retry invoke: %v", err)
	}
	if got := fixture.lastAuth.Load(); got != "Bearer oauth-at-2" {
		t.Fatalf("endpoint saw %q after member refresh, want rotated bearer", got)
	}
}

// remoteOAuthFixture is one TLS server that is simultaneously the MCP
// endpoint (401 until "Bearer oauth-at-1"), the protected-resource metadata
// document and the authorization server (authorize/token/register).
type remoteOAuthFixture struct {
	endpointURL string
	lastAuth    atomic.Value
	// accepted holds the bearer the endpoint currently honors; rotation
	// simulates access-token expiry mid-life.
	accepted atomic.Value
	// challengeScope lands in the 401 WWW-Authenticate challenge; the
	// authorization request must ask for exactly it, nothing broader.
	challengeScope string
	// insufficient makes /mcp answer 403 insufficient_scope for any token —
	// a granted-set shortfall, not expiry, so refresh must not fire.
	insufficient atomic.Bool
	tokenHits    atomic.Int32
	dialAddr     string
	transport    *http.Transport
	// refreshHold, when non-nil, parks the refresh_token grant until closed —
	// revoke-during-refresh tests hold the exchange mid-flight.
	refreshHold    chan struct{}
	refreshEntered atomic.Bool
}

func newRemoteOAuthFixture(t *testing.T) *remoteOAuthFixture {
	t.Helper()
	fixture := &remoteOAuthFixture{}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "remote-oauth-fixture", Version: "1"}, nil)
	mcpServer.AddTool(&mcp.Tool{Name: "search", Description: "Search", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		fixture.lastAuth.Store(r.Header.Get("Authorization"))
		if fixture.insufficient.Load() {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="mcp:admin"`)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		want := "Bearer oauth-at-1"
		if accepted := fixture.accepted.Load(); accepted != nil {
			want = "Bearer " + accepted.(string)
		}
		if r.Header.Get("Authorization") != want {
			challenge := `Bearer resource_metadata="` + issuer + `/.well-known/oauth-protected-resource/mcp"`
			if fixture.challengeScope != "" {
				challenge += `, scope="` + fixture.challengeScope + `"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": issuer + "/mcp", "authorization_servers": []string{issuer}, "scopes_supported": []string{"mcp:read", "mcp:write"}})
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": issuer + "/mcp", "authorization_servers": []string{issuer}, "scopes_supported": []string{"mcp:read", "mcp:write"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "registration_endpoint": issuer + "/register", "scopes_supported": []string{"mcp:read", "mcp:admin"}})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-client"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		target, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := target.Query()
		q.Set("code", "oauth-code")
		q.Set("state", r.URL.Query().Get("state"))
		target.RawQuery = q.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fixture.tokenHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		switch {
		case form.Get("grant_type") == "authorization_code" && form.Get("code") == "oauth-code" && form.Get("code_verifier") != "":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "oauth-at-1", "refresh_token": "oauth-rt-1", "token_type": "Bearer", "expires_in": 3600})
		case form.Get("grant_type") == "refresh_token" && form.Get("refresh_token") != "":
			fixture.refreshEntered.Store(true)
			if fixture.refreshHold != nil {
				<-fixture.refreshHold
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "oauth-at-2", "refresh_token": "oauth-rt-2", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.Error(w, "invalid_grant", http.StatusBadRequest)
		}
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	issuer = server.URL
	fixture.dialAddr = strings.TrimPrefix(server.URL, "https://")
	fixture.endpointURL = "https://remote-fixture.example" + strings.TrimPrefix(fixture.dialAddr, "127.0.0.1") + "/mcp"
	fixture.transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fixture.dialAddr)
		},
	}
	originalResolve, originalClient := api.ResolveRemoteIP, api.RemoteHTTPClient
	t.Cleanup(func() { api.ResolveRemoteIP, api.RemoteHTTPClient = originalResolve, originalClient })
	api.ResolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	transport := fixture.transport
	api.RemoteHTTPClient = func(endpoint string, headers map[string]string) *http.Client {
		origin := ""
		if u, err := url.Parse(endpoint); err == nil {
			origin = api.RequestOrigin(u)
		}
		return &http.Client{
			Transport:     api.RemoteHeaderRoundTripper{Base: transport, Origin: origin, Headers: headers},
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return fixture
}

func (f *remoteOAuthFixture) endpoint() string { return f.endpointURL }

// broker hands the control-plane OAuth broker a client that dials the
// fixture for discovery, registration and token exchange. The redirect
// allowlist carries the fixture's own origin so StartAuth admits it.
func (f *remoteOAuthFixture) broker(secrets credstore.Backend, redirect string) *oauth.Broker {
	broker := oauth.NewBroker(secrets, []string{redirect})
	broker.HTTP = &http.Client{Transport: f.transport, Timeout: 10 * time.Second}
	return broker
}

// CHG-0127 S0.5: the 401 challenge declares the demanded scope — the
// authorization URL asks for exactly it, never the AS-advertised union.
func TestRemoteOAuthAsksChallengeScopeOnly(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fixture.challengeScope = "mcp:read"
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, fix.store, prepared["onboarding_id"].(string)))
	if got := authURL.Query().Get("scope"); got != "mcp:read" {
		t.Fatalf("authorize scope %q: challenge scope must win over AS scopes_supported", got)
	}
}

// CHG-0127 S0.5: a granted-set shortfall is a re-consent event — refreshing
// the same-scoped token cannot fix it and must not loop.
func TestInsufficientScopeDoesNotRefreshLoop(t *testing.T) {
	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	fix.gateway.OAuthRefresh = remoteOAuthRefresh(fix.secrets, fix.control.OAuth, fix.store)
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, fix.store, prepared["onboarding_id"].(string)))
	if err := fix.control.completePreparedOAuth(context.Background(), authURL.Query().Get("state"), "oauth-code"); err != nil {
		t.Fatalf("callback: %v", err)
	}
	listed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projectedName := ""
	for _, tool := range listed.Tools {
		if strings.HasPrefix(tool.Name, "hub-remote-") {
			projectedName = tool.Name
		}
	}
	if projectedName == "" {
		t.Fatal("remote tool not projected")
	}
	before := fixture.tokenHits.Load()
	fixture.insufficient.Store(true)
	_, err = alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{"q": "x"}}})
	if got := fixture.tokenHits.Load(); got != before {
		t.Fatalf("insufficient_scope triggered %d refresh attempts; want re-consent, no refresh", got-before)
	}
	if err == nil || !strings.Contains(err.Error(), "re-consent") {
		t.Fatalf("want re-consent surfacing, got %v", err)
	}
}
