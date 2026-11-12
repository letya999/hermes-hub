package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

func settingsSpace(t *testing.T, right bool) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	if err := os.MkdirAll(filepath.Join(dir, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	docsBody := managedInfraDocs("alice", true)
	docsBody["config"] = "runtime: {timezone: UTC}\nmemory: {enabled: false}\nports: {oauth-callback: 8008}\nchannels:\n  telegram: {bot: {ref: telegram-bot-alice}}\n"
	docsBody["models"] = "default: upstream\nroutes: {upstream: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: upstream}]}\n"
	if right {
		docsBody["grants"] = "allow: {config: [edit_own]}\n"
	}
	for name, body := range docsBody {
		body = strings.TrimPrefix(body, "kind: "+name+"\n")
		if err := os.WriteFile(filepath.Join(dir, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(dir, "secrets.dev.env"):    "",
		filepath.Join(root, "config", "SOUL.md"): "# fixture\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if right {
		docs, err := catalog.Generate()
		if err != nil {
			t.Fatal(err)
		}
		gov := toolhub.NewGovernanceStore(filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub", "governance.json"))
		grant := api.NewRightGrant("rightgrant-alice-settings", "alice", "config", "edit_own", "", "fixture", "operator", time.Now().Add(time.Hour))
		if err := api.Confirm(&grant, "operator", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := gov.GrantRight(grant, docs.Rights, time.Now()); err != nil {
			t.Fatal(err)
		}
		rev, err := passport.CompileAuthored(dir, "dev", time.Now(), docs, stack.ReservedSiblingPorts(dir), map[string][]string{"config": {"edit_own"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := rev.Publish(dir); err != nil {
			t.Fatal(err)
		}
	} else {
		publishAuthored(t, dir, "dev")
	}
	current, err := passport.Current(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/respawn" {
			t.Errorf("unexpected effect path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", server.URL)
	return root, dir, current
}

func TestSettingsEditPublishesAndSurvivesRender(t *testing.T) {
	root, dir, previous := settingsSpace(t, true)
	value := "true"
	respawns := 0
	result, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, previous, func() error {
		record, err := readApplyJournal(dir)
		if err != nil || record.Phase != "effects" {
			t.Fatal("local runtime effect escaped publication journal", err)
		}
		respawns++
		return nil
	})
	if err != nil || result["changed"] != true {
		t.Fatalf("settings edit: %v %v", result, err)
	}
	if respawns != 1 {
		t.Fatalf("runtime effects=%d want 1", respawns)
	}
	current, err := passport.Current(dir)
	if err != nil || current == previous || result["published_revision"] != current {
		t.Fatalf("revision did not advance: %s %v %v", current, result, err)
	}
	effective, err := passport.LoadEffective(dir)
	if err != nil || effective.Config.Runtime.Compression["enabled"] != true {
		t.Fatalf("published config missing setting: %+v %v", effective.Config.Runtime.Compression, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "managed", "dev", "runtime", "self-settings.json"), []byte(`{"compression.enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("compression: {enabled: false}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stack.RenderEnvironment(dir, root, "dev"); err != nil {
		t.Fatal(err)
	}
	again, err := passport.LoadEffective(dir)
	if err != nil || again.Header.Metadata.Revision != current || again.Config.Runtime.Compression["enabled"] != true {
		t.Fatal("restart render changed published config", err)
	}
	generated, err := os.ReadFile(filepath.Join(dir, "generated", "hermes-effective.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		Compression map[string]any `yaml:"compression"`
	}
	if err := yaml.Unmarshal(generated, &rendered); err != nil || rendered.Compression["enabled"] != true {
		t.Fatal("rendered Hermes config lost published setting", err)
	}
	if _, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, previous, nil); err == nil {
		t.Fatal("stale base revision accepted")
	}
	after, _ := passport.Current(dir)
	if after != current {
		t.Fatal("stale edit moved current revision")
	}
}

func TestSettingsEditDeniedWithoutRightOrWithDraft(t *testing.T) {
	value := "true"
	root, dir, current := settingsSpace(t, false)
	before, _ := os.ReadFile(filepath.Join(dir, "passports", "config.yaml"))
	if _, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, current, nil); err == nil {
		t.Fatal("config edit without current right accepted")
	}
	if now, _ := passport.Current(dir); now != current {
		t.Fatal("denied edit moved revision")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "passports", "config.yaml")); string(after) != string(before) {
		t.Fatal("denied edit changed authored config")
	}
	root, dir, current = settingsSpace(t, true)
	config := filepath.Join(dir, "passports", "config.yaml")
	before, _ = os.ReadFile(config)
	draft := strings.Replace(string(before), "runtime: {timezone: UTC}", "runtime: {timezone: UTC, compression: {enabled: false}}", 1)
	if draft == string(before) {
		t.Fatal("fixture has no runtime block")
	}
	if err := os.WriteFile(config, []byte(draft), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, current, nil); err == nil {
		t.Fatal("unpublished authored draft accepted")
	}
	if now, _ := passport.Current(dir); now != current {
		t.Fatal("draft changed effective revision")
	}
}

func TestSettingsEditRejectsRevokedIssuedRight(t *testing.T) {
	root, dir, current := settingsSpace(t, true)
	gov := toolhub.NewGovernanceStore(filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub", "governance.json"))
	if err := gov.RevokeRight("rightgrant-alice-settings", "operator", "test revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	value := "true"
	if _, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, current, nil); err == nil {
		t.Fatal("published right survived issued revocation")
	}
	if now, _ := passport.Current(dir); now != current {
		t.Fatal("revoked edit moved effective revision")
	}
}

func TestPatchAuthoredConfigPreservesUnrelatedComments(t *testing.T) {
	before := []byte("# owner note\nruntime: # runtime note\n  timezone: UTC # keep zone\n  compression:\n    enabled: false\nmemory: {enabled: false}\n")
	after, doc, err := patchAuthoredConfig(before, "runtime.compression.enabled", true)
	if err != nil || doc["runtime"].(map[string]any)["compression"].(map[string]any)["enabled"] != true {
		t.Fatalf("config patch: %v %v", doc, err)
	}
	for _, note := range []string{"# owner note", "# runtime note", "# keep zone"} {
		if !strings.Contains(string(after), note) {
			t.Fatalf("config patch dropped %q", note)
		}
	}
	reset, _, err := patchAuthoredConfig(after, "runtime.compression.enabled", nil)
	if err != nil || strings.Contains(string(reset), "enabled: true") {
		t.Fatalf("config reset: %s %v", reset, err)
	}
}

func TestSettingsEditCrashRestoresAuthoredAndPublished(t *testing.T) {
	root, dir, previous := settingsSpace(t, true)
	oldBody, _ := os.ReadFile(filepath.Join(dir, "passports", "config.yaml"))
	value := "true"
	respawns := 0
	respawn := func() error { respawns++; return nil }
	prior := applyStep
	stopped := false
	applyStep = func(step string) {
		if step == "authored" {
			panic("synthetic stop")
		}
	}
	t.Cleanup(func() { applyStep = prior })
	func() {
		defer func() { stopped = recover() != nil }()
		_, _ = runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, previous, respawn)
	}()
	applyStep = prior
	if !stopped {
		t.Fatal("settings edit did not stop after authored write")
	}
	if _, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, previous, respawn); err == nil || !strings.Contains(err.Error(), "interrupted apply recovered") {
		t.Fatalf("settings edit did not recover interrupted publication: %v", err)
	}
	if respawns != 2 {
		t.Fatalf("recovery did not replay publisher respawn: %d", respawns)
	}
	current, err := passport.Current(dir)
	if err != nil || current != previous {
		t.Fatalf("crash recovery revision %s %v", current, err)
	}
	newBody, _ := os.ReadFile(filepath.Join(dir, "passports", "config.yaml"))
	if string(newBody) != string(oldBody) || strings.Contains(string(newBody), "enabled: true") {
		t.Fatal("crash recovery kept unpublished authored setting")
	}
}

func TestSettingsContractRebindsProfileAndRespawnsRuntime(t *testing.T) {
	root, dir, _ := settingsSpace(t, true)
	if err := os.WriteFile(filepath.Join(dir, "passports", "tools.yaml"), []byte("binding: {profile_id: alice-default, generation: 1}\ncore:\n  settings: {via: toolhub}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "passports", "grants.yaml"), []byte("allow: {config: [edit_own, read], tools: [use]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	govPath := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub", "governance.json")
	gov := toolhub.NewGovernanceStore(govPath)
	for _, right := range [][2]string{{"config", "read"}, {"tools", "use"}} {
		grant := api.NewRightGrant("rightgrant-alice-"+right[0]+"-"+right[1], "alice", right[0], right[1], "", "fixture", "operator", time.Now().Add(time.Hour))
		if err := api.Confirm(&grant, "operator", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := gov.GrantRight(grant, docs.Rights, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	allow := map[string][]string{"config": {"edit_own", "read"}, "tools": {"use"}}
	rev, err := passport.CompileAuthored(dir, "dev", time.Now(), docs, stack.ReservedSiblingPorts(dir), allow)
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEffective(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	policyVersion := stack.PolicyVersion(settings)
	now := time.Now().UTC()
	definition := agenttools.HubToolsDefinition()
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rules := []api.CapabilityRule{
		{CapabilityID: "settings.read", ImplementationDigest: digest, Action: "read", Resource: "settings", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 300}},
		{CapabilityID: "settings.edit", ImplementationDigest: digest, Action: "update", Resource: "settings", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 300}},
	}
	rights := []api.RightRule{{Capability: "config", Operation: "edit_own"}, {Capability: "config", Operation: "read"}, {Capability: "tools", Operation: "use"}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "settings-policy", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, Ceiling: rules, Defaults: rules, RightCeiling: rights}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice",
		Environment: "dev", Generation: 1, PolicyVersion: policyVersion, PassportRevision: rev.ID, PolicyID: policy.PolicyID, PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, Rights: rights}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "agent-tools", "--toolhub-store", storePath, "--principal", "alice", "--context", "alice", "--runtime", "alice", "--policy-version", policyVersion}); err != nil {
		t.Fatal(err)
	}
	var respawns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/respawn" {
			respawns.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", server.URL)
	value := "true"
	if _, err := runSettingsEdit(context.Background(), dir, root, "dev", "compression.enabled", &value, rev.ID, nil); err != nil {
		t.Fatal(err)
	}
	if respawns.Load() != 1 {
		t.Fatalf("runtime respawn effects = %d", respawns.Load())
	}
	loaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.RightsDoc, loaded.RightsLedger = docs.Rights, gov.Load
	auth := identity.TelegramEnvelope("alice", 7, "alice", policyVersion)
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	projected, err := loaded.ListProjectedTools(auth)
	if err != nil || !slices.ContainsFunc(projected, func(tool api.ProjectedTool) bool { return tool.Tool.Name == "settings_edit" }) ||
		!slices.ContainsFunc(projected, func(tool api.ProjectedTool) bool { return tool.Tool.Name == "settings_read" }) {
		t.Fatalf("new settings contract was not projected: %v %v", projected, err)
	}
}
