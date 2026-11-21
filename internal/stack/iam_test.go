package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

func iamSettings() Settings {
	// Infra == nil renders infra: the shared gateway lives here.
	return Settings{
		Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000,
		Tools: testTools("telegram"), Ingress: testIngress("telegram"),
	}
}

func iamEffective(services map[string]string) spawnv1.Effective {
	specs := map[string]spawnv1.ServiceSpec{}
	for name, mode := range services {
		specs[name] = spawnv1.ServiceSpec{Mode: mode}
	}
	var eff spawnv1.Effective
	eff.Stack.Services = specs
	return eff
}

func TestApplyIAMExternalProjectsIssuerAndClient(t *testing.T) {
	s := iamSettings()
	in := &spawnv1.IAM{Mode: "external", Issuer: "https://idp.example.com/application/o/hermes", ClientID: " hermes "}
	if err := s.applyIAM(in, iamEffective(nil)); err != nil {
		t.Fatal(err)
	}
	if s.PassportIAM == nil || s.PassportIAM.Issuer != "https://idp.example.com/application/o/hermes" || s.PassportIAM.ClientID != "hermes" {
		t.Fatalf("unexpected projection: %#v", s.PassportIAM)
	}
}

func TestApplyIAMExternalRejectsBundledServices(t *testing.T) {
	s := iamSettings()
	in := &spawnv1.IAM{Mode: "external", Issuer: "https://idp.example.com", ClientID: "hermes"}
	services := map[string]string{"identity-hub": "shared", "hub-postgres": "shared"}
	if err := s.applyIAM(in, iamEffective(services)); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("external must not run bundled services: %v", err)
	}
}

func TestApplyIAMExternalRequiresIssuerAndClient(t *testing.T) {
	s := iamSettings()
	if err := s.applyIAM(&spawnv1.IAM{Mode: "external", Issuer: " ", ClientID: "x"}, iamEffective(nil)); err == nil {
		t.Fatal("empty issuer must fail closed")
	}
	s = iamSettings()
	if err := s.applyIAM(&spawnv1.IAM{Mode: "external", Issuer: "https://idp.example.com", ClientID: " "}, iamEffective(nil)); err == nil || !strings.Contains(err.Error(), "client_id") {
		t.Fatalf("external requires client_id: %v", err)
	}
	if err := s.applyIAM(&spawnv1.IAM{Mode: "external", Issuer: "http://idp.example.com", ClientID: "x"}, iamEffective(nil)); err == nil {
		t.Fatal("plain http non-loopback issuer must fail")
	}
}

func TestApplyIAMBundledRequiresSharedServices(t *testing.T) {
	in := &spawnv1.IAM{Mode: "bundled"}
	s := iamSettings()
	if err := s.applyIAM(in, iamEffective(nil)); err == nil || !strings.Contains(err.Error(), "identity-hub") {
		t.Fatalf("bundled without services must fail: %v", err)
	}
	// Both must be shared — a per-space postgres breaks the topology.
	s = iamSettings()
	if err := s.applyIAM(in, iamEffective(map[string]string{
		"identity-hub": "shared", "hub-postgres": "per-space",
	})); err == nil {
		t.Fatal("per-space hub-postgres must fail")
	}
}

func TestApplyIAMBundledIssuerShape(t *testing.T) {
	services := map[string]string{"identity-hub": "shared", "hub-postgres": "shared"}
	s := iamSettings()
	if err := s.applyIAM(&spawnv1.IAM{Mode: "bundled", Issuer: "https://sso.example.com", ClientID: "x"}, iamEffective(services)); err != nil {
		t.Fatal(err)
	}
	// A path issuer breaks the host-level ZITADEL contract.
	s = iamSettings()
	if err := s.applyIAM(&spawnv1.IAM{Mode: "bundled", Issuer: "https://sso.example.com/realms/x", ClientID: "x"}, iamEffective(services)); err == nil {
		t.Fatal("bundled issuer must not carry a path")
	}
	// Empty issuer is legal: the derived dev issuer lands at render.
	s = iamSettings()
	if err := s.applyIAM(&spawnv1.IAM{Mode: "bundled"}, iamEffective(services)); err != nil {
		t.Fatal(err)
	}
}

func TestApplyIAMRejectsUnknownModeAndSiblingSpace(t *testing.T) {
	s := iamSettings()
	if err := s.applyIAM(&spawnv1.IAM{Mode: "saml"}, iamEffective(nil)); err == nil {
		t.Fatal("unknown mode must fail closed")
	}
	infra := false
	s = iamSettings()
	s.Infra = &infra
	err := s.applyIAM(&spawnv1.IAM{Mode: "external", Issuer: "https://idp.example.com", ClientID: "x"}, iamEffective(nil))
	if err == nil || !strings.Contains(err.Error(), "infra") {
		t.Fatalf("iam on a sibling space renders nothing — must fail: %v", err)
	}
}

func TestComposeBundledRendersIdentityHubAndPostgres(t *testing.T) {
	s := iamSettings()
	s.PassportIAM = &IAMSettings{Mode: "bundled"}
	s.PassportServices = map[string]string{
		"communication-hub": "shared", "identity-hub": "shared", "hub-postgres": "shared",
	}
	doc := Compose(s, t.TempDir(), t.TempDir())
	services := doc["services"].(M)
	zitadel, ok := services["identity-hub"].(M)
	if !ok {
		t.Fatalf("identity-hub missing: %v", serviceKeys(services))
	}
	if zitadel["image"] != identityHubImage {
		t.Fatalf("unpinned image: %#v", zitadel["image"])
	}
	env := zitadel["environment"].(M)
	if env["ZITADEL_DATABASE_POSTGRES_HOST"] != "hub-postgres" || env["ZITADEL_EXTERNALDOMAIN"] != "idp.localhost" {
		t.Fatalf("zitadel env drift: %#v", env)
	}
	// The derived issuer publishes on every interface — the gateway reaches
	// it through host-gateway, the browser through 127.0.0.1.
	ports := zitadel["ports"].([]string)
	if ports[0] != "8091:8091" {
		t.Fatalf("derived issuer must publish all interfaces, got %#v", ports)
	}
	postgres := services["hub-postgres"].(M)
	if postgres["environment"].(M)["POSTGRES_DB"] != "zitadel" {
		t.Fatalf("postgres env: %#v", postgres["environment"])
	}
	if _, ok := doc["volumes"].(M)["hub-postgres-data"]; !ok {
		t.Fatal("shared postgres volume missing")
	}
	// No client_id yet → bootstrap leaves SSO off, never half-configured.
	gw := services["communication-hub"].(M)["environment"].(M)
	if gw["HUB_OIDC_ISSUER"] != "" || gw["HUB_OIDC_CLIENT_ID"] != "" {
		t.Fatalf("bundled without client_id must render empty env: %#v", gw)
	}
	// *.localhost is loopback inside containers — the derived issuer name
	// resolves to the host, where the published port lives.
	hosts := services["communication-hub"].(M)["extra_hosts"].(M)
	if hosts["idp.localhost"] != "host-gateway" {
		t.Fatalf("derived issuer needs the host-gateway mapping: %#v", hosts)
	}
}

func TestComposeBundledClientIDEnablesGateway(t *testing.T) {
	s := iamSettings()
	s.PassportIAM = &IAMSettings{Mode: "bundled", ClientID: "hermes"}
	gw := Compose(s, t.TempDir(), t.TempDir())["services"].(M)["communication-hub"].(M)["environment"].(M)
	if gw["HUB_OIDC_ISSUER"] != "http://idp.localhost:8091" || gw["HUB_OIDC_CLIENT_ID"] != "hermes" {
		t.Fatalf("derived issuer must reach the gateway: %#v", gw)
	}
}

func TestComposeExternalIAMSetsGatewayEnvOnly(t *testing.T) {
	s := iamSettings()
	s.PassportIAM = &IAMSettings{Mode: "external", Issuer: "https://idp.example.com/application/o/h", ClientID: "hermes"}
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	gw := services["communication-hub"].(M)["environment"].(M)
	if gw["HUB_OIDC_ISSUER"] != "https://idp.example.com/application/o/h" || gw["HUB_OIDC_CLIENT_ID"] != "hermes" {
		t.Fatalf("external iam must reach the gateway: %#v", gw)
	}
	if _, ok := services["identity-hub"]; ok {
		t.Fatal("external mode must not render the bundled hub")
	}
}

func TestEnsureIAMAuthPersistsAndKeepsMasterkey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity-hub.auth")
	if err := ensureIAMAuth(path); err != nil {
		t.Fatal(err)
	}
	first, err := ReadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"POSTGRES_PASSWORD", "ZITADEL_DATABASE_POSTGRES_USER_PASSWORD", "ZITADEL_MASTERKEY", "ZITADEL_SESSION_COOKIE_SECRET"} {
		if first[key] == "" {
			t.Fatalf("%s missing", key)
		}
	}
	if len(first["ZITADEL_MASTERKEY"]) != 32 {
		t.Fatalf("masterkey must be exactly 32 chars, got %d", len(first["ZITADEL_MASTERKEY"]))
	}
	if first["ZITADEL_DATABASE_POSTGRES_USER_PASSWORD"] != first["POSTGRES_PASSWORD"] {
		t.Fatal("shared postgres serves one consumer — passwords must match")
	}
	if err := ensureIAMAuth(path); err != nil {
		t.Fatal(err)
	}
	second, _ := ReadSecrets(path)
	if second["ZITADEL_MASTERKEY"] != first["ZITADEL_MASTERKEY"] {
		t.Fatal("masterkey rotated on rerender — the store would be unreadable")
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("auth file must be operator-only, got %o", info.Mode().Perm())
	}
}
