package catalog_test

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/catalog"
)

// specRightsTable is the normative SPEC-0092 capability table: every domain
// and operation an agent may ever be granted, with the expected subject.
var specRightsTable = map[string]map[string]string{
	"workspace":     {"read": "self", "write": "self", "create_project": "self", "read_other": "cross"},
	"memory":        {"read": "self", "search": "self", "remember": "self"},
	"models":        {"list": "self", "use": "self", "switch": "self"},
	"tools":         {"list": "self", "select": "self"},
	"skills":        {"list": "self", "invoke": "self", "draft": "self", "publish": "self"},
	"connectors":    {"discover": "self", "prepare": "self", "enable": "self", "disable": "self", "remove": "self", "share": "cross"},
	"agents":        {"inspect_self": "self", "restart_self": "self", "monitor_self": "self", "inspect_other": "cross"},
	"users":         {"list": "cross", "invite": "cross"},
	"stack":         {"inspect": "self", "change": "self"},
	"config":        {"read": "self", "edit_own": "self"},
	"channels":      {"send": "self", "send_voice": "self"},
	"observability": {"read_own_logs": "self", "read_logs": "cross"},
}

func TestRightsCatalogMatchesSpecTable(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs.Rights.Capabilities) != len(specRightsTable) {
		t.Fatalf("rights domains %d, spec table %d", len(docs.Rights.Capabilities), len(specRightsTable))
	}
	for domain, ops := range specRightsTable {
		cap, ok := docs.Rights.Capabilities[domain]
		if !ok {
			t.Fatalf("spec domain %q missing from rights catalog", domain)
		}
		if cap.Description == "" {
			t.Fatalf("domain %q lacks a norm description", domain)
		}
		if len(cap.Operations) != len(ops) {
			t.Fatalf("domain %q: catalog ops %v, spec ops %v", domain, cap.Operations, ops)
		}
		for op, subject := range ops {
			row, ok := cap.Operations[op]
			if !ok {
				t.Fatalf("spec operation %s.%s missing from rights catalog", domain, op)
			}
			if row.Subject != subject {
				t.Fatalf("%s.%s subject %q, spec says %q", domain, op, row.Subject, subject)
			}
			if row.Description == "" {
				t.Fatalf("%s.%s lacks a norm description", domain, op)
			}
			if row.Effect == "" || len(row.Issuers) == 0 {
				t.Fatalf("%s.%s lacks effect/issuers metadata", domain, op)
			}
		}
	}
	for domain := range docs.Rights.Capabilities {
		if _, ok := specRightsTable[domain]; !ok {
			t.Fatalf("catalog invents non-spec domain %q", domain)
		}
	}
}

func TestEveryRightHasRegisteredPEP(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for domain, cap := range docs.Rights.Capabilities {
		for op, row := range cap.Operations {
			if len(row.PEP) == 0 {
				t.Fatalf("%s.%s has no PEP enforcement site", domain, op)
			}
		}
	}
}

func TestEveryToolDeclaresRequires(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	members := 0
	for name, ts := range docs.Toolsets.Toolsets {
		for _, member := range ts.Members {
			members++
			ops, ok := ts.Requires[member]
			if !ok || len(ops) == 0 {
				t.Fatalf("toolset %s member %s declares no requires", name, member)
			}
			for _, right := range ops {
				domain, op, ok := splitRight(right)
				if !ok {
					t.Fatalf("%s member %s requires malformed %q", name, member, right)
				}
				if err := docs.ValidateRight(domain, op); err != nil {
					t.Fatalf("%s member %s requires unknown right %q: %v", name, member, right, err)
				}
			}
		}
	}
	if members == 0 {
		t.Fatal("no toolset members generated")
	}
	t.Logf("covered %d toolset members", members)
}

func splitRight(right string) (string, string, bool) {
	for i := 0; i < len(right); i++ {
		if right[i] == '.' {
			return right[:i], right[i+1:], i+1 < len(right)
		}
	}
	return "", "", false
}

func TestValidateRightRejectsUnknown(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := docs.ValidateRight("workspace", "delete_all"); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if err := docs.ValidateRight("nonexistent", "read"); err == nil {
		t.Fatal("unknown domain accepted")
	}
	if err := docs.ValidateRight("workspace", "read"); err != nil {
		t.Fatalf("spec right rejected: %v", err)
	}
}
