package toolhub

import (
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Every control tool ships a user-facing contract: a non-empty description,
// an object schema and the operation's declared properties. This is the
// surface Pinned Hermes sees — drift here silently breaks the agent.
func TestControlToolContractSchemas(t *testing.T) {
	for _, name := range []string{
		"discover", "prepare_source", "rotate", "disable", "revoke", "remove",
		"diagnostics", "grant_request", "status", "required_credentials", "confirm",
	} {
		description, schema := controlToolContract(name)
		if description == "" {
			t.Fatalf("%s: empty description", name)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s: schema not an object", name)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("%s: no properties", name)
		}
	}
	// Operation-specific contract details.
	description, schema := controlToolContract("discover")
	props := schema["properties"].(map[string]any)
	if len(props) != 1 || props["query"] == nil || !strings.Contains(description, "Read-only") {
		t.Fatalf("discover contract drifted: %v", props)
	}
	if _, schema := controlToolContract("diagnostics"); schema["properties"].(map[string]any)["severity"].(map[string]any)["enum"] == nil {
		t.Fatal("diagnostics severity enum missing")
	}
	removeDesc, removeSchema := controlToolContract("remove")
	if !strings.Contains(removeDesc, "remove_nonce") || removeSchema["properties"].(map[string]any)["remove_nonce"] == nil {
		t.Fatal("remove must declare its destructive preview nonce")
	}
	_, prepareSchema := controlToolContract("prepare_source")
	cli, ok := prepareSchema["properties"].(map[string]any)["cli"].(map[string]any)
	if !ok || cli["oneOf"] == nil || cli["required"] == nil {
		t.Fatal("cli spec must carry the XOR install schema")
	}
	if cliDesc, _ := cli["description"].(string); !strings.Contains(cliDesc, "github-release") {
		t.Fatal("cli contract lost its artifact-install guidance")
	}
	// An unknown operation still returns a generic object schema.
	if _, schema := controlToolContract("nonexistent"); schema["type"] != "object" {
		t.Fatal("unknown operation schema missing")
	}
}

// connector_edit is a meta-tool: it exists on the surface only when at least
// one of its lifecycle operations is allowed — never unconditionally.
func TestConnectorSurfaceAllowed(t *testing.T) {
	allowed := map[string]bool{"status": true}
	if !connectorSurfaceAllowed(allowed, "status") || connectorSurfaceAllowed(allowed, "remove") {
		t.Fatal("direct allowlist ignored")
	}
	if connectorSurfaceAllowed(allowed, "connector_edit") {
		t.Fatal("connector_edit surfaced with no lifecycle op")
	}
	allowed["connector_enable"] = true
	if !connectorSurfaceAllowed(allowed, "connector_edit") {
		t.Fatal("connector_edit not surfaced by a lifecycle op")
	}
	if connectorSurfaceAllowed(map[string]bool{}, "connector_edit") {
		t.Fatal("connector_edit surfaced on an empty allowlist")
	}
}

func TestCliSchemaBuilders(t *testing.T) {
	props := cliRequiredProperties("source", "binary")
	if len(props) != 2 || props["source"] == nil || props["binary"] == nil {
		t.Fatalf("required properties: %v", props)
	}
	install := cliInstallProperties()
	for key := range cliSpecKeys {
		if install[key] == nil {
			t.Fatalf("cli spec key %s missing from install properties", key)
		}
	}
	tools, ok := install["tools"].(map[string]any)
	if !ok {
		t.Fatal("tools schema missing")
	}
	items, ok := tools["items"].(map[string]any)
	if !ok || items["required"] == nil {
		t.Fatal("tools items must declare required name/effect")
	}
	if install["binary"].(map[string]any)["pattern"] != "^/" {
		t.Fatal("guest binary path must stay absolute")
	}
}

// The device page accepts a live credentials gate or an unused confirmation
// gate — spent, wrong-kind or missing tokens never open it.
func TestDeviceTokenValidBoundaries(t *testing.T) {
	var o api.Onboarding
	if deviceTokenValid(o, "anything") || deviceTokenValid(o, "") {
		t.Fatal("gate-less onboarding admitted")
	}
	o.ArmGate(api.GateCredentials, "cred-token", time.Now().Add(time.Hour))
	if !deviceTokenValid(o, "cred-token") {
		t.Fatal("live credentials gate rejected")
	}
	if deviceTokenValid(o, "other") {
		t.Fatal("foreign token admitted")
	}
	o.ArmGate(api.GateConfirmSetup, "confirm-token", time.Now().Add(time.Hour))
	if !deviceTokenValid(o, "confirm-token") {
		t.Fatal("live confirmation gate rejected")
	}
	o.ConfirmationUsed = true
	if deviceTokenValid(o, "confirm-token") {
		t.Fatal("spent confirmation token admitted")
	}
	o.ConfirmationUsed = false
	o.ArmGate(api.GateOAuth, "oauth-token", time.Now().Add(time.Hour))
	if deviceTokenValid(o, "oauth-token") {
		t.Fatal("oauth gate must not open the device page")
	}
}
