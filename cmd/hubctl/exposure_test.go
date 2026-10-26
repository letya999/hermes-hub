package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exposureConfigYAML = `schema: 1
provider: sish
relay:
  host: tunnel.example.com
  port: 2222
  user: alice
  domain: hub.example.com
  host_keys:
    - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa6/V1v39RJXNZvJSJp7hEDWdTsFc9n/k8G+sMFBqGX"
zone: alice
key_ref: "file:sish-key.pem"
endpoints:
  onboarding:
    target: hub_public_listener
`

func writeExposureConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgDir := filepath.Join(dir, "connections", "exposure")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(path, []byte(exposureConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIExposureEnrol(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExposureConfig(t, dir)
	out := captureOutput(t, func() error {
		return run(ctx, []string{"exposure", "enrol", "--dir", dir})
	})
	if !strings.Contains(out, "ssh-ed25519 ") || !strings.Contains(out, "*.alice.hub.example.com") {
		t.Fatalf("enrol output missing pubkey/dns steps:\n%s", out)
	}
	if strings.Contains(out, "PRIVATE KEY") {
		t.Fatal("private half leaked into output")
	}
	key, err := os.ReadFile(filepath.Join(dir, "connections", "exposure", "sish-key.pem"))
	if err != nil || !strings.Contains(string(key), "PRIVATE KEY") {
		t.Fatalf("key material: %v", err)
	}
}

func TestCLIExposureEnrolCloudflared(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "connections", "exposure")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	body := `schema: 1
provider: cloudflared
key_ref: "broker:grant-01"
endpoints:
  a:
    target: hub_public_listener
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"exposure", "enrol", "--dir", dir}); err == nil {
		t.Fatal("cloudflared enrol must fail: credentials are account-side")
	}
}
