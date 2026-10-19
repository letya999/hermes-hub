package api

import "testing"

// validateHeaderDelivery pins remote credentials to HTTP-header delivery and
// rejects Host/Content-Length overrides and newline prefixes.
func TestValidateHeaderDelivery(t *testing.T) {
	t.Parallel()
	if err := validateHeaderDelivery(CredentialInput{Name: "FOO", Delivery: "http_header", Target: "X-Api-Key", Prefix: "Bearer"}); err != nil {
		t.Fatalf("valid header delivery: %v", err)
	}
	for _, input := range []CredentialInput{
		{Name: "FOO", Delivery: "env", Target: "X-Key"},
		{Name: "FOO", Delivery: "http_header", Target: "bad header"},
		{Name: "FOO", Delivery: "http_header", Target: "Host"},
		{Name: "FOO", Delivery: "http_header", Target: "content-length"},
		{Name: "FOO", Delivery: "http_header", Target: "X-Key", Prefix: "a\nb"},
	} {
		if err := validateHeaderDelivery(input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
}

// Discovery rank orders evidence sources; unknown or missing evidence sinks.
func TestDiscoverySourceRank(t *testing.T) {
	t.Parallel()
	cases := map[string]int{"mcp-registry": 0, "toolhive": 1, "docker-mcp": 2, "smithery": 3, "other": 9}
	for source, want := range cases {
		got := discoverySourceRank(DiscoveryCandidate{Evidence: []RecipeEvidence{{Source: source}}})
		if got != want {
			t.Fatalf("%s: %d != %d", source, got, want)
		}
	}
	if discoverySourceRank(DiscoveryCandidate{}) != 9 {
		t.Fatal("no evidence must rank last")
	}
}

// Mount-source lookup and host extraction are exact contracts.
func TestHasMountSourceAndHostOfURL(t *testing.T) {
	t.Parallel()
	mounts := []Mount{{Source: "a"}, {Source: "b"}}
	if !HasMountSource(mounts, "b") || HasMountSource(mounts, "c") {
		t.Fatal("mount source membership")
	}
	if HostOfURL("https://EXAMPLE.com:8443/x") != "example.com" || HostOfURL("::bad::") != "" {
		t.Fatal("host extraction")
	}
}
