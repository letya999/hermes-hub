package dns

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Config validation contract: the four mandated profiles load; regru
// requires a static egress IP (fail closed); gcloud requires project +
// managed zone; base zone must sit inside the provider zone; record names
// are confined to the base zone.
func TestConfigLoad(t *testing.T) {
	write := func(body string) string {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"regru with static ip", "schema: 1\nprovider: regru\nzone: example.com\nbase: hub.example.com\ncredential_ref: file:creds.json\nstatic_egress_ip: 203.0.113.10\n", nil},
		{"digitalocean", "schema: 1\nprovider: digitalocean\nzone: hub.example.com\ncredential_ref: broker:dns-do\n", nil},
		{"gcloud delegated", "schema: 1\nprovider: gcloud\nzone: hub.example.com\nproject: my-proj\nmanaged_zone: hub-zone\ncredential_ref: broker:dns-gcp\n", nil},
		{"regru without static ip fails closed", "schema: 1\nprovider: regru\nzone: example.com\nbase: hub.example.com\ncredential_ref: file:creds.json\n", ErrDenied},
		{"gcloud missing managed zone", "schema: 1\nprovider: gcloud\nzone: hub.example.com\nproject: my-proj\ncredential_ref: broker:x\n", ErrInvalid},
		{"base outside zone", "schema: 1\nprovider: digitalocean\nzone: example.com\nbase: hub.other.net\ncredential_ref: broker:x\n", ErrInvalid},
		{"unknown provider", "schema: 1\nprovider: route53\nzone: example.com\ncredential_ref: broker:x\n", ErrInvalid},
		{"bad credential scheme", "schema: 1\nprovider: digitalocean\nzone: example.com\ncredential_ref: env:TOKEN\n", ErrInvalid},
		{"wrong schema", "schema: 2\nprovider: digitalocean\nzone: example.com\ncredential_ref: broker:x\n", ErrInvalid},
	}
	for _, tc := range cases {
		_, err := Load(write(tc.body))
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: want %v, got %v", tc.name, tc.wantErr, err)
		}
	}
	// File-level failures surface as errors, never a partial config.
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing file must error")
	}
	if _, err := Load(write("schema: 1\nprovider: [unclosed\n")); err == nil {
		t.Fatal("malformed yaml must error")
	}
}
