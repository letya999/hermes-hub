package dns

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The propagation contract: poll authoritative NS until the record reads
// back, bounded by deadline; no NS configured is a config error.
func TestWaitAuthoritative(t *testing.T) {
	cases := []struct {
		name    string
		answers [][]string // successive lookup results; nil repeats forever
		ns      []string
		wantErr error
	}{
		{"found on second poll", [][]string{{"other"}, {"tok"}}, []string{"ns1.t"}, nil},
		{"never appears", nil, []string{"ns1.t"}, ErrPropagation},
		{"no nameservers", nil, nil, ErrInvalid},
	}
	for _, tc := range cases {
		calls := 0
		lookup := func(_ context.Context, _, _, _ string) ([]string, error) {
			if calls < len(tc.answers) {
				a := tc.answers[calls]
				calls++
				return a, nil
			}
			return nil, nil
		}
		err := WaitAuthoritative(context.Background(), "n.example.com", "TXT", "tok", tc.ns, time.Second, lookup)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("%s: want %v, got %v", tc.name, tc.wantErr, err)
		}
	}
}

// CheckDelegation compares the zone's live NS set with the configured
// nameservers (R6 delegation bootstrap verification).
func TestCheckDelegation(t *testing.T) {
	orig := LookupNS
	defer func() { LookupNS = orig }()
	cases := []struct {
		name    string
		live    []string
		wantErr bool
	}{
		{"delegated", []string{"ns1.digitalocean.com.", "NS2.DIGITALOCEAN.COM"}, false},
		{"partially delegated", []string{"ns1.digitalocean.com."}, true},
		{"none of them", []string{"ns1.reg.ru."}, true},
	}
	for _, tc := range cases {
		LookupNS = func(context.Context, string) ([]string, error) { return tc.live, nil }
		cfg := &Config{Base: "hub.example.com", NameServers: []string{"ns1.digitalocean.com", "ns2.digitalocean.com"}}
		err := cfg.CheckDelegation(context.Background())
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	LookupNS = func(context.Context, string) ([]string, error) { return nil, errors.New("nx") }
	cfg := &Config{Base: "hub.example.com", NameServers: []string{"ns1.digitalocean.com"}}
	if err := cfg.CheckDelegation(context.Background()); !errors.Is(err, ErrPropagation) {
		t.Fatalf("lookup failure must surface as propagation: %v", err)
	}
}

func TestLookupNSResolvesReally(t *testing.T) {
	// The default LookupNS must actually query DNS — an .invalid zone is
	// guaranteed unresolvable, so the default implementation errors.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := LookupNS(ctx, "nonexistent.invalid."); err == nil {
		t.Fatal("default LookupNS must perform a real NS query")
	}
}
