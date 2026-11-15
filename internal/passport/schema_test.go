package passport_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

func normative(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "normative", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The six normative examples are the v2 contract (SPEC-0092): strict decode
// accepts exactly their key shapes — no body wrapper, contract keys top-level.
func TestNormativeExamplesParseStrict(t *testing.T) {
	for _, name := range passport.Names {
		if _, err := passport.Decode(name, normative(t, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	sd, err := passport.Decode("stack", normative(t, "stack"))
	if err != nil {
		t.Fatal(err)
	}
	stack := sd.(*passport.StackDoc)
	if stack.Kind != "stack" || stack.Metadata.Revision != "r-0123456789abcdef" || stack.Metadata.Catalogs != "4" {
		t.Fatalf("stack header: %+v", stack.Metadata)
	}
	if stack.Identity.User != "alice" || stack.Identity.Organization != "acme" {
		t.Fatalf("stack identity: %+v", stack.Identity)
	}
	if stack.Services["cliproxy-google-1"].Mode != "per-space" || stack.Services["hermes-runtime"].Mode != "supervised" {
		t.Fatalf("service modes lost: %+v", stack.Services)
	}

	cd, err := passport.Decode("config", normative(t, "config"))
	if err != nil {
		t.Fatal(err)
	}
	config := cd.(*passport.ConfigDoc)
	if config.Ports["oauth-callback"] != 8008 || config.Runtime.Context.ParallelLanes != 2 {
		t.Fatalf("config runtime/ports lost: %+v", config.Config)
	}
	if config.Channels["telegram"].Voice.In != "transcribe" || config.Channels["telegram"].Bot.Ref != "telegram-bot-alice" {
		t.Fatalf("channel contract lost: %+v", config.Channels)
	}
	if config.Supervision.Watchers["stack-health"].Protected != true || config.Memory.NightlyRollup != "03:00" {
		t.Fatalf("supervision/memory lost: %+v", config.Config)
	}

	md, err := passport.Decode("models", normative(t, "models"))
	if err != nil {
		t.Fatal(err)
	}
	models := md.(*passport.ModelsDoc)
	if models.Default != "everyday" || len(models.Routes["everyday"].Ladder) != 4 {
		t.Fatalf("routes lost: %+v", models.Routes)
	}
	g1 := models.Services["google-pro-1"]
	if g1.Via != "cliproxy" || g1.Auth.Upstream.Kind != "oauth-session" || g1.Auth.ProxyAccess.Ref != "cliproxy-google-1" {
		t.Fatalf("service auth contract lost: %+v", g1)
	}
	if g1.Limits[0].Of != "provider_quota" || g1.Limits[0].Amount != 50 || g1.Limits[0].Clock != "provider" {
		t.Fatalf("limits lost: %+v", g1.Limits)
	}
	if or := models.Services["openrouter-key"]; or.Auth.ProxyAccess != nil || or.Auth.Upstream.Kind != "broker-key" {
		t.Fatalf("optional proxy_access must stay absent: %+v", or.Auth)
	}

	wd, err := passport.Decode("workspace", normative(t, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	ws := wd.(*passport.WorkspaceDoc)
	if len(ws.Writable) != 9 || len(ws.ReadOnly) != 6 || ws.Mounts["/shared/acme-docs"].Mode != "ro" {
		t.Fatalf("workspace bounds lost: %+v", ws.Workspace)
	}
	if !bytes.Contains([]byte(ws.Tree), []byte("├── config/")) {
		t.Fatalf("ascii tree lost")
	}

	td, err := passport.Decode("tools", normative(t, "tools"))
	if err != nil {
		t.Fatal(err)
	}
	tools := td.(*passport.ToolsDoc)
	if tools.Core["discovery"].Via != "toolhub" || tools.Core["skills"].Access != "read" {
		t.Fatalf("core toolsets lost: %+v", tools.Core)
	}
	rule := tools.Core["files"].Rules["file_write"].Scope.Paths
	if len(rule) != 5 || rule[0] != "/workspace/work" {
		t.Fatalf("per-tool rule scope lost: %v", rule)
	}
	if tools.Extended["browser"].Via != "toolhub" || tools.Extended["ssh"].Tools.Include[0] != "ssh_list" {
		t.Fatalf("extended toolsets lost: %+v", tools.Extended)
	}
	if tools.Extended["browser"].Scope.URLs[0] != "https://docs.acme.example" {
		t.Fatalf("url scope lost: %+v", tools.Extended["browser"].Scope)
	}

	gd, err := passport.Decode("grants", normative(t, "grants"))
	if err != nil {
		t.Fatal(err)
	}
	grants := gd.(*passport.GrantsDoc)
	if len(grants.Allow) != 11 || grants.Allow["models"][2] != "switch" || grants.Allow["workspace"][2] != "create_project" {
		t.Fatalf("capability grants lost: %+v", grants.Allow)
	}
}

// decode -> encode -> decode is a fixed point and the emitted key order is
// deterministic, so a republished revision diff is clean.
func TestNormativeExamplesRoundTripStable(t *testing.T) {
	for _, name := range passport.Names {
		doc, err := passport.Decode(name, normative(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		enc1, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		doc2, err := passport.Decode(name, enc1)
		if err != nil {
			t.Fatalf("%s re-decode: %v", name, err)
		}
		if !reflect.DeepEqual(doc, doc2) {
			t.Fatalf("%s round trip changed the document", name)
		}
		enc2, err := yaml.Marshal(doc2)
		if err != nil || !bytes.Equal(enc1, enc2) {
			t.Fatalf("%s re-encode not deterministic: %v", name, err)
		}
	}
}

// Strict decode fails closed: a v1 body wrapper, a legacy key, a kind that
// does not match the file name or a non-v2 envelope all reject.
func TestUnknownPassportKeyRejected(t *testing.T) {
	header := "apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: alice, environment: dev, revision: r-1, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\n"
	for label, body := range map[string]string{
		"body wrapper":      header + "body: {identity: {user: alice}}\nidentity: {user: alice}\nservices: {}\n",
		"legacy features":   header + "features: [files]\nidentity: {user: alice}\nservices: {}\n",
		"legacy sections":   "apiVersion: six-passport/v2\nkind: tools\nmetadata: {space: alice, environment: dev, revision: r-1, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\nsections: [files]\n",
		"unknown grant key": "apiVersion: six-passport/v2\nkind: grants\nmetadata: {space: alice, environment: dev, revision: r-1, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\nallow: {}\ndeny: {}\n",
	} {
		name := "stack"
		if label == "legacy sections" {
			name = "tools"
		} else if label == "unknown grant key" {
			name = "grants"
		}
		if _, err := passport.Decode(name, []byte(body)); err == nil {
			t.Fatalf("%s decoded without error", label)
		}
	}

	// A file whose kind does not match the passport name rejects.
	if _, err := passport.Decode("stack", []byte("apiVersion: six-passport/v2\nkind: config\nmetadata: {space: alice, environment: dev, revision: r-1, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\nidentity: {user: alice}\nservices: {}\n")); err == nil {
		t.Fatal("kind mismatch decoded")
	}
	// A v1 envelope rejects even with a v2-shaped body.
	v1 := "apiVersion: six-passport/v1\nkind: stack\nmetadata: {space: alice, environment: dev, revision: r-1, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\nidentity: {user: alice}\nservices: {}\n"
	if _, err := passport.Decode("stack", []byte(v1)); err == nil {
		t.Fatal("v1 envelope decoded as v2")
	}
	// A missing revision id fails the envelope check.
	if _, err := passport.Decode("stack", []byte("apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: alice, environment: dev, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\nidentity: {user: alice}\nservices: {}\n")); err == nil {
		t.Fatal("revision-less envelope decoded")
	}
}
