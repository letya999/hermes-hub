package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestEnrollSiblingRuntimeTokens(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "not-spaces", "alice")
	if err := Init(outside, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := EnrollSiblingRuntimeTokens(outside, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "toolhub-tokens.json")); !os.IsNotExist(err) {
		t.Fatal("enrollment scanned a directory that is not spaces")
	}

	spaces := filepath.Join(root, "spaces")
	owner := filepath.Join(spaces, "alice")
	sibling := filepath.Join(spaces, "bob")
	short := filepath.Join(spaces, "dave")
	if err := Init(owner, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := Init(sibling, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := Init(short, "dave"); err != nil {
		t.Fatal(err)
	}
	ownerToken := strings.Repeat("a", 64)
	oldBob := strings.Repeat("b", 64)
	newBob := strings.Repeat("d", 64)
	carol := strings.Repeat("c", 64)
	if err := os.WriteFile(filepath.Join(owner, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+ownerToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+newBob+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(short, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=short\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(owner, "toolhub-tokens.json")
	seed := map[string]identity.Envelope{
		oldBob:     {Schema: identity.Schema, PrincipalID: "bob", ExternalIdentityID: "bob", ContextID: "bob", RuntimeID: "bob", ConversationID: "toolhub", DeliveryTargetID: "toolhub", PolicyVersion: "policy-1"},
		carol:      {Schema: identity.Schema, PrincipalID: "carol", ExternalIdentityID: "carol", ContextID: "carol", RuntimeID: "carol", ConversationID: "toolhub", DeliveryTargetID: "toolhub", PolicyVersion: "policy-1"},
		ownerToken: {Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "toolhub", DeliveryTargetID: "toolhub", PolicyVersion: "policy-1"},
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnrollSiblingRuntimeTokens(owner, "prod"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("token file was replaced instead of updated in place")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]identity.Envelope
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got[ownerToken]; ok {
		t.Fatal("owner runtime token was written into the shared file")
	}
	if _, ok := got[oldBob]; ok {
		t.Fatal("rotated sibling token was kept")
	}
	if _, ok := got[strings.Repeat("e", 8)]; ok {
		t.Fatal("short token enrolled")
	}
	bob, ok := got[newBob]
	if !ok || bob.PrincipalID != "bob" || bob.ContextID != "bob" || bob.RuntimeID != "bob" || bob.ConversationID != "toolhub" || bob.DeliveryTargetID != "toolhub" || bob.Schema != identity.Schema {
		t.Fatalf("sibling envelope=%+v present=%v", bob, ok)
	}
	if _, ok := got[carol]; !ok || got[carol].PrincipalID != "carol" {
		t.Fatal("unknown principal dropped")
	}
	settings, err := ReadEnvironment(sibling, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if bob.PolicyVersion != PolicyVersion(settings) {
		t.Fatalf("policy=%s", bob.PolicyVersion)
	}
	kept := append([]byte(nil), body...)
	if err := EnrollSiblingRuntimeTokens(owner, "prod"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(kept) {
		t.Fatal("unchanged enrollment rewrote the file")
	}

	for _, envelope := range got {
		if envelope.PrincipalID == "alice" || envelope.PrincipalID == "dave" {
			t.Fatalf("unexpected principal %s", envelope.PrincipalID)
		}
	}

	lonely := filepath.Join(root, "lonely", "spaces", "erin")
	if err := Init(lonely, "erin"); err != nil {
		t.Fatal(err)
	}
	onlyToken := strings.Repeat("f", 64)
	onlyPath := filepath.Join(lonely, "toolhub-tokens.json")
	onlySeed := map[string]identity.Envelope{
		onlyToken: {Schema: identity.Schema, PrincipalID: "erin", ExternalIdentityID: "erin", ContextID: "erin", RuntimeID: "erin", ConversationID: "toolhub", DeliveryTargetID: "toolhub", PolicyVersion: "policy-1"},
	}
	raw, err = json.Marshal(onlySeed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lonely, "runtime.auth"), []byte("HUB_RUNTIME_AUTH="+onlyToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onlyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := EnrollSiblingRuntimeTokens(lonely, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(onlyPath); !os.IsNotExist(err) {
		t.Fatal("empty enrollment left the owner token file in place")
	}
}

func TestEnrollSiblingModelRelayAdmins(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	owner := filepath.Join(spaces, "infra")
	managed := filepath.Join(spaces, "alice")
	unmanaged := filepath.Join(spaces, "bob")
	for _, dir := range []string{owner, managed, unmanaged} {
		name := filepath.Base(dir)
		if err := Init(dir, name); err != nil {
			t.Fatal(err)
		}
	}
	// Only managed spaces have a model-relay; unmanaged siblings must not
	// appear in the admins file even when an auth file is present.
	agent := filepath.Join(managed, "agent.yaml")
	body, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), `model: ""`, "model: default", 1)
	text = strings.Replace(text, `model_url: ""`, "model_url: http://model-relay:8318/v1", 1)
	text += "capability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(agent, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	admin := strings.Repeat("m", 64)
	if err := os.WriteFile(filepath.Join(managed, "model-relay.auth"), []byte("HUB_MODEL_RELAY_AUTH=x\nHUB_MODEL_RELAY_ADMIN="+admin+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unmanaged, "model-relay.auth"), []byte("HUB_MODEL_RELAY_ADMIN="+strings.Repeat("n", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "model-relay.auth"), []byte("HUB_MODEL_RELAY_ADMIN="+strings.Repeat("o", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(owner, "model-relay-admins.json")
	if err := EnrollSiblingModelRelayAdmins(owner, "prod"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]struct {
		URL   string `json:"url"`
		Admin string `json:"admin"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["alice"].Admin != admin || got["alice"].URL != "http://model-relay-alice:8318" {
		t.Fatalf("managed sibling admin not enrolled: %v", got)
	}
	if _, ok := got["bob"]; ok {
		t.Fatal("unmanaged sibling admin enrolled")
	}
	if _, ok := got["infra"]; ok {
		t.Fatal("owner's own admin enrolled into the shared file")
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnrollSiblingModelRelayAdmins(owner, "prod"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged enrollment rewrote the admins file")
	}

	// Once no managed sibling carries a usable admin the file goes away.
	if err := os.WriteFile(filepath.Join(managed, "model-relay.auth"), []byte("HUB_MODEL_RELAY_AUTH=x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := EnrollSiblingModelRelayAdmins(owner, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("empty enrollment left the admins file in place")
	}
}

// RenderEnvironment on the infra space must produce the shared gateway's
// relay admin map, the same way it produces toolhub-tokens.json.
func TestRenderEnvironmentEnrollsModelRelayAdmins(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	infra := filepath.Join(spaces, "infra")
	managed := filepath.Join(spaces, "alice")
	if err := Init(infra, "infra"); err != nil {
		t.Fatal(err)
	}
	if err := Init(managed, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(infra)
	if err != nil {
		t.Fatal(err)
	}
	settings.Model, settings.ModelURL = "test", "http://model.invalid/v1"
	if err := WriteSpace(infra, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "secrets.prod.env"), []byte("OPENAI_API_KEY=x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(managed, "agent.yaml")
	body, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), `model: ""`, "model: default", 1)
	text = strings.Replace(text, `model_url: ""`, "model_url: http://model-relay:8318/v1", 1)
	text += "capability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(agent, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	admin := strings.Repeat("m", 64)
	if err := os.WriteFile(filepath.Join(managed, "model-relay.auth"), []byte("HUB_MODEL_RELAY_AUTH=x\nHUB_MODEL_RELAY_ADMIN="+admin+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(infra, root, "prod"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(infra, "model-relay-admins.json"))
	if err != nil {
		t.Fatalf("render did not enroll relay admins: %v", err)
	}
	if !strings.Contains(string(raw), `"alice"`) {
		t.Fatalf("managed sibling missing from enrolled admins: %s", raw)
	}
}

func TestRuntimeTokenEnvelopeUsesOrganizationContext(t *testing.T) {
	settings := Settings{Schema: 1, User: "bob", Organization: "acme", Environment: "prod", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	envelope, err := runtimeTokenEnvelope(settings)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.ContextID != "acme" || envelope.PrincipalID != "bob" || envelope.RuntimeID != "bob" {
		t.Fatalf("envelope=%+v", envelope)
	}
}

func TestManagedRuntimeTokenEnvelopeBindsGeneration(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 3}
	envelope, err := runtimeTokenEnvelope(s)
	if err != nil || envelope.CapabilityProfile != "alice-default" || envelope.Environment != "dev" || envelope.Generation != 3 {
		t.Fatalf("managed envelope=%+v err=%v", envelope, err)
	}
	s.CapabilityGeneration = 0
	if _, err := runtimeTokenEnvelope(s); err == nil {
		t.Fatal("managed token admitted without generation")
	}
}
