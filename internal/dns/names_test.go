package dns

import (
	"errors"
	"testing"
)

// RecordName confines relative names to DNS-label shape and maps them onto
// the provider zone (base suffix appended when the base is a subzone);
// FQDN renders the absolute owner name. Escapes and malformed names fail
// closed.
func TestRecordNameAndFQDN(t *testing.T) {
	cfg := &Config{Zone: "example.com", Base: "hub.example.com"}
	delegated := &Config{Zone: "hub.example.com", Base: "hub.example.com"}
	cases := []struct {
		name    string
		cfg     *Config
		in      string
		want    string
		wantErr error
	}{
		{"wildcard user under delegated zone", delegated, "*.alice", "*.alice", nil},
		{"acme challenge under base", cfg, "_acme-challenge.alice", "_acme-challenge.alice.hub", nil},
		{"wildcard under subzone of parent zone", cfg, "*.alice", "*.alice.hub", nil},
		{"apex of base", cfg, "@", "@.hub", nil},
		{"empty name", cfg, "", "", ErrInvalid},
		{"absolute name rejected", cfg, "x.hub.example.com.", "", ErrInvalid},
		{"double dot rejected", cfg, "a..b", "", ErrInvalid},
		{"bad chars rejected", cfg, "a;b", "", ErrInvalid},
		{"leading dot rejected", cfg, ".alice", "", ErrInvalid},
	}
	for _, tc := range cases {
		got, err := tc.cfg.RecordName(tc.in)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: want %v, got %v", tc.name, tc.wantErr, err)
		}
		if tc.wantErr == nil && got != tc.want {
			t.Fatalf("%s: want %q, got %q", tc.name, tc.want, got)
		}
	}
	if fqdn := delegated.FQDN("*.alice"); fqdn != "*.alice.hub.example.com" {
		t.Fatalf("FQDN: got %q", fqdn)
	}
}
