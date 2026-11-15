package hosting

import "testing"

func TestPrivateEndpointAcceptsGeneratedUnderscoreOwner(t *testing.T) {
	if !PrivateEndpoint("http://hosting-artem_two-dev:8182", "8182", "") {
		t.Fatal("generated hosting service name rejected")
	}
	for _, endpoint := range []string{
		"http://hosting-artem_two-dev.example:8182",
		"http://hosting-artem_two-dev:80",
		"http://hosting-artem_two-dev:8182/other",
	} {
		if PrivateEndpoint(endpoint, "8182", "") {
			t.Fatalf("nonprivate endpoint admitted: %s", endpoint)
		}
	}
}
