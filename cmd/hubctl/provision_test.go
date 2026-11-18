package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func provisionSpace(t *testing.T, config string) (parent, dir string) {
	t.Helper()
	parent = t.TempDir()
	dir = filepath.Join(parent, "tenant-a")
	if err := os.MkdirAll(filepath.Join(dir, "passports"), 0755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "passports", "config.yaml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return parent, dir
}

func TestProvisionPortsRewritesPlaceholdersSkippingSiblings(t *testing.T) {
	parent, dir := provisionSpace(t, "vnc: __PORT_BROWSER_VNC__\ncallback: __PORT_OAUTH_CALLBACK__\n")
	other := filepath.Join(parent, "tenant-b")
	if err := os.MkdirAll(filepath.Join(other, "passports"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "passports", "config.yaml"),
		[]byte("ports:\n  browser-vnc: 18300\n  oauth-callback: 8100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	allocated, err := provisionPorts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if allocated["browser-vnc"] != 18301 || allocated["oauth-callback"] != 8101 {
		t.Fatalf("sibling ports not skipped: %v", allocated)
	}
	body, err := os.ReadFile(filepath.Join(dir, "passports", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "__PORT_") || !strings.Contains(string(body), "18301") || !strings.Contains(string(body), "8101") {
		t.Fatalf("placeholders not materialized: %s", body)
	}
}

func TestProvisionPortsNoPlaceholdersIsNoop(t *testing.T) {
	_, dir := provisionSpace(t, "vnc: 18300\n")
	allocated, err := provisionPorts(dir)
	if err != nil || allocated != nil {
		t.Fatalf("placeholder-free passport must be untouched: %v %v", allocated, err)
	}
	if _, err = provisionPorts(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing config passport must surface as an error")
	}
}

func TestSameTextDirFoldsCase(t *testing.T) {
	if !sameTextDir("Tenant-A", "tenant-a") || sameTextDir("tenant-a", "tenant-b") {
		t.Fatal("directory identity fold broken")
	}
}

func TestProvisionPreflightAggregatesFindings(t *testing.T) {
	_, dir := provisionSpace(t, "")
	findings := provisionPreflight(dir, "tenant-a", "dev")
	joined := strings.Join(findings, "\n")
	for _, want := range []string{"passport stack", "passport config", "passport workspace", "secrets.dev.env", "no infra-owner sibling"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing finding %q in:\n%s", want, joined)
		}
	}
}

func TestProvisionPreflightCatchesUnresolvedTokensAndGrantVocabulary(t *testing.T) {
	_, dir := provisionSpace(t, "user: __USER__\n")
	for _, name := range []string{"stack", "models", "tools", "grants", "workspace"} {
		if err := os.WriteFile(filepath.Join(dir, "passports", name+".yaml"), []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "passports", "grants.yaml"),
		[]byte("allow:\n  totally-bogus-capability: [frobnicate]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	findings := provisionPreflight(dir, "tenant-a", "dev")
	joined := strings.Join(findings, "\n")
	if !strings.Contains(joined, "unresolved template tokens") {
		t.Fatalf("unresolved passport tokens not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "totally-bogus-capability.frobnicate is not in the pinned rights catalog") {
		t.Fatalf("foreign grant op not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "secrets.dev.env") {
		t.Fatalf("missing secrets not reported:\n%s", joined)
	}
}

func provisionProfile(t *testing.T, root, ceiling string) {
	t.Helper()
	profileDir := filepath.Join(root, "policies", "profiles", "test-profile")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "policy.yaml"), []byte(ceiling), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionCeilingStampsDigestAndDefaultLimits(t *testing.T) {
	root := t.TempDir()
	provisionProfile(t, root, `ceiling:
  - capability_id: cap.fs
    action: read
    resource: workspace
    connection_id: personal
    path_prefix: /inbox
  - capability_id: cap.exec
    action: run
    limits:
      output_bytes: 4096
      timeout_seconds: 30
`)
	rules, err := provisionCeiling(root, "test-profile", "digest-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("ceiling rules lost: %v", rules)
	}
	for _, rule := range rules {
		if rule.ImplementationDigest != "digest-1" {
			t.Fatalf("digest not stamped: %v", rule)
		}
	}
	if rules[0].Limits.OutputBytes != 2097152 || rules[0].Limits.TimeoutSeconds != 120 {
		t.Fatalf("default limits not applied: %+v", rules[0].Limits)
	}
	if rules[1].Limits.OutputBytes != 4096 || rules[1].Limits.TimeoutSeconds != 30 {
		t.Fatalf("profile limits not honoured: %+v", rules[1].Limits)
	}
	if rules[0].ConnectionID != "personal" || rules[0].PathPrefix != "/inbox" {
		t.Fatalf("ceiling scope fields dropped: %+v", rules[0])
	}
}

func TestProvisionCeilingRejectsMissingOrEmptyProfile(t *testing.T) {
	if _, err := provisionCeiling(t.TempDir(), "absent", "d"); err == nil {
		t.Fatal("missing profile must fail")
	}
	root := t.TempDir()
	provisionProfile(t, root, "ceiling: []\n")
	if _, err := provisionCeiling(root, "test-profile", "d"); err == nil {
		t.Fatal("empty ceiling must fail")
	}
}

func TestBuildProvisionBundleSortsRightsAndBindsIdentity(t *testing.T) {
	_, dir := provisionSpace(t, "{}\n")
	root := t.TempDir()
	provisionProfile(t, root, `ceiling:
  - capability_id: cap.fs
    action: read
`)
	if err := os.WriteFile(filepath.Join(dir, "passports", "grants.yaml"),
		[]byte("allow:\n  zebra: [write, read]\n  alpha: [peek]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	bundle, err := buildProvisionBundle(dir, "tenant-a", "dev", root, "test-profile", "1h", "issuer", now)
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil || len(bundle.rights) != 3 {
		t.Fatalf("rights not minted from grants: %+v", bundle)
	}
	got := []string{bundle.rights[0].Capability, bundle.rights[1].Capability, bundle.rights[2].Capability}
	if got[0] != "alpha" || got[1] != "zebra" || got[2] != "zebra" {
		t.Fatalf("rights not deterministically sorted: %v", got)
	}
	if bundle.rights[1].Operation != "read" || bundle.rights[2].Operation != "write" {
		t.Fatalf("operations not sorted inside a capability: %+v", bundle.rights)
	}
	if bundle.policy.PolicyID != "tenant-a-policy" || bundle.profile.PrincipalID != "tenant-a" ||
		bundle.profile.Environment != "dev" || bundle.principalEnvelope.PrincipalID != "tenant-a" {
		t.Fatalf("identity binding drifted: %+v %+v", bundle.policy, bundle.profile)
	}
	if len(bundle.policy.RightCeiling) != 3 || len(bundle.policy.Ceiling) != 1 {
		t.Fatalf("policy ceilings not assembled: %+v", bundle.policy)
	}
	if bundle.rights[0].ExpiresAt.IsZero() {
		t.Fatal("right grants must carry the provision expiry")
	}
	if _, err := buildProvisionBundle(dir, "tenant-a", "dev", root, "test-profile", "bogus", "issuer", now); err == nil {
		t.Fatal("invalid expiry must fail before any store write")
	}
}

func TestProvisionEquivalenceIgnoresIssueStamps(t *testing.T) {
	_, dir := provisionSpace(t, "{}\n")
	root := t.TempDir()
	provisionProfile(t, root, "ceiling:\n  - capability_id: cap.fs\n    action: read\n")
	if err := os.WriteFile(filepath.Join(dir, "passports", "grants.yaml"), []byte("allow:\n  a: [b]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := buildProvisionBundle(dir, "u", "dev", root, "test-profile", "1h", "issuer", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildProvisionBundle(dir, "u", "dev", root, "test-profile", "1h", "issuer", time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !provisionPolicyEquivalent(first.policy, second.policy) {
		t.Fatal("identical authority must converge across issue stamps")
	}
	if !provisionProfileEquivalent(first.profile, second.profile) {
		t.Fatal("identical profile must converge across issue stamps")
	}
	other, err := buildProvisionBundle(dir, "different-user", "dev", root, "test-profile", "1h", "issuer", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if provisionPolicyEquivalent(first.policy, other.policy) || provisionProfileEquivalent(first.profile, other.profile) {
		t.Fatal("authority divergence must not converge")
	}
}
