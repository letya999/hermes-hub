package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestCapabilityCLIProtectedPublicationAndRevoke(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	write := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Explicit empty starting policy", Status: toolhub.ActiveStatus}
	write(policy)
	args := []string{"capability", "--kind", "policy", "--file", input, "--toolhub-store", storePath, "--confirm"}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	profile := toolhub.CapabilityProfile{Schema: toolhub.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Explicit empty profile", Status: toolhub.ActiveStatus}
	write(profile)
	args[2] = "profile"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = profile.ProfileID, "dev", 1
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("empty published profile: %v %v", tools, err)
	}
	policy.Revision++
	policy.Status = toolhub.RevokedStatus
	write(policy)
	args[2] = "policy"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectedTools(auth); !errors.Is(err, toolhub.ErrUnauthorized) {
		t.Fatalf("existing reader retained revoked policy: %v", err)
	}
	policy.Revision = 1
	policy.Status = toolhub.ActiveStatus
	write(policy)
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), args); !errors.Is(err, toolhub.ErrConflict) {
		t.Fatalf("stale operator input accepted: %v", err)
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected input changed registry/history")
	}
}

func TestCapabilityCLIRequiresExplicitConfirmation(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed", Status: toolhub.ActiveStatus}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath}
	if err := runCapability(args); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unconfirmed write committed: %v", err)
	}
	if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unconfirmed write created registry")
	}
	if err := runCapability(append(args, "--issuer", "model", "--confirm")); err == nil {
		t.Fatal("model-issuer confirmation accepted")
	}
	if err := runCapability(append(args, "--confirm")); err != nil {
		t.Fatal(err)
	}
	if _, err := toolhub.Load(storePath); err != nil {
		t.Fatal(err)
	}
	// An operator-supplied confirmation that does not cover this record is void.
	var stored struct {
		Changes []struct {
			Policy *toolhub.CapabilityPolicy `json:"policy"`
		} `json:"capability_changes"`
	}
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored.Changes) != 1 || stored.Changes[0].Policy == nil || stored.Changes[0].Policy.Confirmation == nil {
		t.Fatalf("confirmation not persisted: %v", err)
	}
}

func TestCapabilityCLIGroupAuthoringIsValidationOnly(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "group.json")
	group := toolhub.CapabilityGroup{GroupID: "workspace-reads", Revision: 1, Members: []toolhub.CapabilityRule{
		{CapabilityID: "files.read", ImplementationDigest: "sha256:" + strings.Repeat("a", 64), Action: "read", Resource: "workspace", Limits: toolhub.CapabilityLimits{OutputBytes: 1024, TimeoutSeconds: 5}},
	}}
	body, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "group", "--file", input}); err != nil {
		t.Fatal(err)
	}
	bad := toolhub.CapabilityGroup{GroupID: "workspace-reads", Revision: 0}
	body, _ = json.Marshal(bad)
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "group", "--file", input}); err == nil {
		t.Fatal("malformed group accepted")
	}
}

func TestCapabilityCLIRejectsIncompleteAndMalformedRecords(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_STORE", "")
	for _, args := range [][]string{nil, {"--unknown"}, {"--kind", "grant"}, {"--kind", "policy", "--file", "x", "--toolhub-store", "x", "extra"}} {
		if err := runCapability(args); err == nil {
			t.Fatalf("bad flags: %v", args)
		}
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "record.json")
	storePath := filepath.Join(dir, "store.json")
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath}
	if err := runCapability(args); err == nil {
		t.Fatal("missing input accepted")
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, `{}`, strings.Repeat(" ", (4<<20)+1)} {
		if err := os.WriteFile(input, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runCapability(args); err == nil {
			t.Fatal("bad input accepted")
		}
		if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("bad input created registry")
		}
	}
	args[1] = "profile"
	if err := os.WriteFile(input, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability(args); err == nil {
		t.Fatal("bad profile accepted")
	}
	if err := os.WriteFile(storePath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability(args); err == nil {
		t.Fatal("malformed protected store replaced")
	}
}

// --kind connectors emits the reviewed recommendation manifest without
// touching any store; admission stays opt-in by construction.
func TestCapabilityCLIConnectorsManifest(t *testing.T) {
	if err := runCapability([]string{"--kind", "connectors"}); err != nil {
		t.Fatal(err)
	}
}

// --kind tools is the operator surface over the unified `tools:` map: it
// lists the backend vocabulary, prints the compiled plan for a settings.yaml,
// requires --confirm to write, and re-validates through the spawn parser.
func TestCapabilityCLITools(t *testing.T) {
	out := captureOutput(t, func() error {
		return runCapability([]string{"--kind", "tools"})
	})
	for _, want := range []string{"native", "toolhub", "mcp", "off", "terminal", "memory"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tools vocabulary missing %q: %s", want, out)
		}
	}
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.yaml")
	managed := "schema: 1\nuser: alice\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\nmodel: synthetic\nmodel_url: http://model-relay:8318/v1\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nmemory: false\n"
	if err := os.WriteFile(settingsPath, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without --set the compiled plan prints and nothing changes.
	plan := captureOutput(t, func() error {
		return runCapability([]string{"--kind", "tools", "--settings", settingsPath})
	})
	if !strings.Contains(plan, `"plan"`) {
		t.Fatalf("plan output missing: %s", plan)
	}
	// Grant without --confirm prints the diff and refuses.
	args := []string{"--kind", "tools", "--settings", settingsPath, "--set", "terminal=native,memory=native"}
	if err := runCapability(args); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unconfirmed grant committed: %v", err)
	}
	if body, _ := os.ReadFile(settingsPath); strings.Contains(string(body), "terminal: native") {
		t.Fatal("unconfirmed grant changed settings")
	}
	if err := runCapability(append(args, "--confirm")); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.Read(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"terminal", "memory"} {
		if settings.Tools[name].Via != "native" {
			t.Fatalf("grant for %q not applied: %+v", name, settings.Tools)
		}
	}
	// Upsert adds an entry without dropping the existing ones; the written
	// form stays a compact scalar map and other settings lines survive.
	if err := runCapability([]string{"--kind", "tools", "--settings", settingsPath, "--set", "todo=native,browser=mcp:playwright", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	settings, err = stack.Read(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Tools["todo"].Via != "native" || settings.Tools["browser"].Via != "mcp" || settings.Tools["browser"].Server != "playwright" {
		t.Fatalf("upsert lost entries: %+v", settings.Tools)
	}
	if settings.Tools["terminal"].Via != "native" || settings.Tools["memory"].Via != "native" {
		t.Fatalf("upsert dropped earlier grants: %+v", settings.Tools)
	}
	body, _ := os.ReadFile(settingsPath)
	if !strings.Contains(string(body), "capability_mode: managed") || !strings.Contains(string(body), "tools:") {
		t.Fatalf("surgical write corrupted settings:\n%s", body)
	}
	// A non-reviewed native name and an unknown backend are refused before
	// the file is touched.
	before, _ := os.ReadFile(settingsPath)
	for _, set := range []string{"delegation=native", "file=bogus", "not-a-pair"} {
		if err := runCapability([]string{"--kind", "tools", "--settings", settingsPath, "--set", set, "--confirm"}); err == nil {
			t.Fatalf("invalid --set %q accepted", set)
		}
	}
	if after, _ := os.ReadFile(settingsPath); !slices.Equal(before, after) {
		t.Fatal("refused --set changed the file")
	}
	// Empty backends remove entries; emptying the map revokes everything.
	if err := runCapability([]string{"--kind", "tools", "--settings", settingsPath, "--set", "terminal=,memory=,todo=,browser=", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	settings, err = stack.Read(settingsPath)
	if err != nil || len(settings.Tools) != 0 {
		t.Fatalf("revoke left grants: %+v %v", settings.Tools, err)
	}
	// tools: is the universal surface — unmanaged settings accept it too.
	plain := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plain, []byte("schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nmemory: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "tools", "--settings", plain, "--set", "terminal=native", "--confirm"}); err != nil {
		t.Fatalf("tools write rejected on plain settings: %v", err)
	}
	settings, err = stack.Read(plain)
	if err != nil || settings.Tools["terminal"].Via != "native" {
		t.Fatalf("plain settings lost the grant: %+v %v", settings.Tools, err)
	}
}

// The same CLI edits a split space: --settings resolves a directory to its
// workspace.yaml, the write is staged against agent.yaml, validated as a
// pair and swapped atomically; access/mcp-raw suffixes land in the entry.
func TestCapabilityCLIToolsSplitSpace(t *testing.T) {
	dir := t.TempDir()
	s := stack.Settings{
		Schema: 3, User: "alice", Environment: "prod", Model: "synthetic",
		ModelURL: "http://model-relay:8318/v1", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000, SpaceDir: dir,
		Tools: map[string]stack.ToolEntry{"memory": {Via: "native"}},
		MCP:   map[string]stack.MCPServer{"gitea": {URL: "https://gitea.example/mcp"}},
	}
	if err := stack.WriteSpace(dir, s); err != nil {
		t.Fatal(err)
	}
	agentBefore, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// A bare directory resolves to workspace.yaml for the tools surface.
	plan := captureOutput(t, func() error {
		return runCapability([]string{"--kind", "tools", "--settings", dir})
	})
	if !strings.Contains(plan, "memory") {
		t.Fatalf("dir plan missing native entries: %s", plan)
	}
	// Unconfirmed edits print the plan and leave both files untouched.
	args := []string{"--kind", "tools", "--settings", dir, "--set", "file=toolhub+ro,gitea=mcp-raw:gitea"}
	if err := runCapability(args); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unconfirmed split edit committed: %v", err)
	}
	if err := runCapability(append(args, "--confirm")); err != nil {
		t.Fatal(err)
	}
	back, err := stack.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Tools["file"].Access != "ro" || back.Tools["file"].Via != "toolhub" {
		t.Fatalf("+ro access lost: %+v", back.Tools["file"])
	}
	if back.Tools["gitea"].Via != "mcp-raw" || back.Tools["gitea"].Server != "gitea" {
		t.Fatalf("mcp-raw server lost: %+v", back.Tools["gitea"])
	}
	// The write touched workspace.yaml only — agent.yaml is byte-identical.
	agentAfter, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(agentBefore, agentAfter) {
		t.Fatal("tools edit modified agent.yaml")
	}
	// Pointing at workspace.yaml directly resolves the same target.
	if err := runCapability([]string{"--kind", "tools", "--settings", filepath.Join(dir, "workspace.yaml"), "--set", "file=", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	back, err = stack.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Tools["file"]; ok {
		t.Fatalf("removal left entry: %+v", back.Tools)
	}
	// An edit that would invalidate the pair refuses before swapping.
	wsBefore, _ := os.ReadFile(filepath.Join(dir, "workspace.yaml"))
	if err := runCapability([]string{"--kind", "tools", "--settings", dir, "--set", "memory=native+ro", "--confirm"}); err == nil {
		t.Fatal("ro on native accepted")
	}
	if wsAfter, _ := os.ReadFile(filepath.Join(dir, "workspace.yaml")); !slices.Equal(wsBefore, wsAfter) {
		t.Fatal("invalid edit changed workspace.yaml")
	}
}

// --kind preview diffs an unconfirmed draft against the stored profile using
// the dispatch evaluator and never writes the store.
func TestCapabilityCLIPreview(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	write := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed", Status: toolhub.ActiveStatus}
	write(policy)
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath, "--confirm"}
	if err := runCapability(args); err != nil {
		t.Fatal(err)
	}
	profile := toolhub.CapabilityProfile{Schema: toolhub.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed", Status: toolhub.ActiveStatus}
	write(profile)
	args[1] = "profile"
	if err := runCapability(args); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	// The draft bumps the revision and carries no confirmation: preview must
	// still evaluate it and must not persist anything.
	draft := profile
	draft.Revision++
	write(draft)
	if err := runCapability([]string{"--kind", "preview", "--file", input, "--toolhub-store", storePath}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preview wrote to the protected store")
	}
	// A stale revision previews to the same conflict apply would raise.
	write(profile)
	if err := runCapability([]string{"--kind", "preview", "--file", input, "--toolhub-store", storePath}); err == nil {
		t.Fatal("stale preview admitted")
	}
}
