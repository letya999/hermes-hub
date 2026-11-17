package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionPortsAllocatesUnusedPorts(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	for name, cfg := range map[string]string{
		"owner": "ports: {browser-vnc: 18300, oauth-callback: 8100}\n",
		"two":   "ports: {browser-vnc: 18301, oauth-callback: 8101}\n",
	} {
		dir := filepath.Join(spaces, name, "passports")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(spaces, "newbie", "passports")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	body := "ports:\n    browser-vnc: __PORT_BROWSER_VNC__\n    oauth-callback: __PORT_OAUTH_CALLBACK__\n"
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	allocated, err := provisionPorts(filepath.Join(spaces, "newbie"))
	if err != nil {
		t.Fatal(err)
	}
	if allocated["browser-vnc"] != 18302 || allocated["oauth-callback"] != 8102 {
		t.Fatalf("allocated ports collide or skip: %v", allocated)
	}
	written, err := os.ReadFile(filepath.Join(target, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(written); got != "ports:\n    browser-vnc: 18302\n    oauth-callback: 8102\n" {
		t.Fatalf("config passport not materialized: %q", got)
	}
	// Rerun is a no-op: placeholders are gone, the space keeps its ports.
	if again, err := provisionPorts(filepath.Join(spaces, "newbie")); err != nil || again != nil {
		t.Fatalf("rerun changed allocation: %v %v", again, err)
	}
}

func TestProvisionCeilingStampsBoundDigest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "policies", "profiles", "standard-user")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := "ceiling:\n  - {capability_id: files.read, action: read, resource: files}\n  - {capability_id: files.list, action: list, resource: files, limits: {output_bytes: 64, timeout_seconds: 5}}\n"
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	rules, err := provisionCeiling(root, "standard-user", "sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules: %v", rules)
	}
	for _, rule := range rules {
		if rule.ImplementationDigest != "sha256:abc" {
			t.Fatalf("rule not stamped with bound digest: %+v", rule)
		}
	}
	if rules[0].Limits.OutputBytes != 2097152 || rules[1].Limits.OutputBytes != 64 || rules[1].Limits.TimeoutSeconds != 5 {
		t.Fatalf("limits not defaulted/overridden: %+v", rules)
	}
	if _, err := provisionCeiling(root, "missing-profile", "sha256:abc"); err == nil {
		t.Fatal("missing profile ceiling reported success")
	}
}
