package passport

import (
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/catalog"
)

// TestEveryPlaceableToolsetCompiles is the catalog-integrity sweep (SPEC-0092
// R6): every toolset carrying a governance section must be admittable under
// each executor its catalog entry declares, and a toolset with no section is
// denied by construction — a catalog entry compile could never admit would be
// dead vocabulary. The ceiling authors the admission bound; declaring each
// candidate inside it exercises the full checkTools path for real.
func TestEveryPlaceableToolsetCompiles(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	placeable, denied := 0, 0
	for name, ts := range docs.Toolsets.Toolsets {
		if ts.Section == "" {
			denied++
			in := v2FixtureInputs(t)
			in.PlatformCeiling.Docs["tools"]["extended"].(map[string]any)[name] = map[string]any{"via": "native"}
			if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "denied by construction") {
				t.Fatalf("denied-by-construction toolset %s admitted or mis-rejected: %v", name, err)
			}
			continue
		}
		placeable++
		for _, via := range ts.Via {
			in := v2FixtureInputs(t)
			in.PlatformCeiling.Docs["tools"]["extended"].(map[string]any)[name] = map[string]any{"via": via}
			if _, err := Compile(in); err != nil {
				t.Fatalf("placeable toolset %s via %s rejected: %v", name, via, err)
			}
		}
		// Enforceable surfaces must also accept the full option vocabulary:
		// include/exclude over real members, access, scope keys and per-member
		// rules — a cataloged option compile rejects is not executable.
		if !slices.Contains(ts.Via, "toolhub") && !slices.Contains(ts.Via, "mcp") {
			continue
		}
		member := name
		if len(ts.Members) > 0 {
			member = ts.Members[0]
		}
		in := v2FixtureInputs(t)
		// The fixture's own connectors spec carries exclude — merged with an
		// include probe it would be a genuine contradiction, not a catalog gap.
		in.User.Docs["tools"] = map[string]any{}
		in.PlatformCeiling.Docs["tools"]["extended"].(map[string]any)[name] = map[string]any{
			"via":    ts.Via[0],
			"tools":  map[string]any{"include": []any{member}},
			"access": "read",
			"scope":  map[string]any{"paths": []any{"/workspace"}},
			"rules":  map[string]any{member: map[string]any{"confirm": true}},
		}
		if _, err := Compile(in); err != nil {
			t.Fatalf("toolset %s rejects its own option surface: %v", name, err)
		}
	}
	if placeable == 0 || denied == 0 {
		t.Fatalf("sweep saw %d placeable, %d denied toolsets — catalog shrank", placeable, denied)
	}
}

// TestEveryCatalogRightIsGrantable sweeps the rights catalog through grant
// projection: with the ceiling offering every domain, the ledger may grant
// every cataloged operation and each must land in grants.yaml. An operation
// compile could not admit would be a capability the space can never hold.
func TestEveryCatalogRightIsGrantable(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	in := v2FixtureInputs(t)
	domains := make([]any, 0, len(docs.Rights.Capabilities))
	ledger := map[string][]string{}
	for domain, cap := range docs.Rights.Capabilities {
		domains = append(domains, domain)
		ops := make([]string, 0, len(cap.Operations))
		for op := range cap.Operations {
			ops = append(ops, op)
		}
		ledger[domain] = ops
	}
	in.PlatformCeiling.Docs["grants"]["allow"] = domains
	in.Grants = ledger
	rev, err := Compile(in)
	if err != nil {
		t.Fatalf("full rights ledger rejected: %v", err)
	}
	allow, err := rev.GrantAllow()
	if err != nil {
		t.Fatal(err)
	}
	for domain, ops := range ledger {
		for _, op := range ops {
			if !slices.Contains(allow[domain], op) {
				t.Fatalf("grants.yaml lost %s.%s", domain, op)
			}
		}
	}
}
