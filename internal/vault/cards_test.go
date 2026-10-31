package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func validCard() map[string]any {
	return map[string]any{
		"type":       "contact",
		"status":     "active",
		"confidence": 0.9,
		"source":     "daily/2026-10-31.md",
		"tags":       []any{"people", "work"},
		"aliases":    []any{"N.", "Nick"},
		"created":    "2026-10-31",
		"updated":    "2026-10-31",
	}
}

// CardError is an error value callers can log or surface — its message
// carries the field and the broken rule.
func TestCardErrorString(t *testing.T) {
	err := CardError{Field: "confidence", Rule: "range"}
	if got := err.Error(); got == "" {
		t.Fatal("CardError renders an empty message")
	} else if got != "card: confidence violates range" {
		t.Fatalf("unexpected CardError message: %q", got)
	}
}

func TestCardSchemaValid(t *testing.T) {
	if errs := ValidateCard("memory/cards/contacts/nick.md", validCard()); len(errs) != 0 {
		t.Fatalf("valid card rejected: %v", errs)
	}
}

func TestCardSchemaRejectsBadType(t *testing.T) {
	doc := validCard()
	doc["type"] = "bogus"
	errs := ValidateCard("memory/cards/contacts/nick.md", doc)
	if len(errs) == 0 {
		t.Fatal("unknown card type accepted")
	}
	for _, e := range errs {
		if e.Field == "type" {
			return
		}
	}
	t.Fatalf("type violation not attributed to the type field: %v", errs)
}

func TestCardSchemaReportsField(t *testing.T) {
	doc := validCard()
	delete(doc, "tags")
	doc["status"] = "flying"
	doc["confidence"] = 1.7
	errs := ValidateCard("memory/cards/contacts/nick.md", doc)
	got := map[string]string{}
	for _, e := range errs {
		got[e.Field] = e.Rule
	}
	for field, rule := range map[string]string{"tags": "required", "status": "enum", "confidence": "range"} {
		if got[field] != rule {
			t.Fatalf("missing structured error %s/%s in %v", field, rule, errs)
		}
	}
}

// The scaffold's memory/schema.json is the same contract ValidateCard
// enforces — one source of truth, not two drifting copies.
func TestCardSchemaFileIsContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("schema.json is not a JSON schema: %v", err)
	}
	for _, name := range []string{"type", "status", "confidence", "source", "tags", "aliases", "created", "updated"} {
		if _, ok := doc.Properties[name]; !ok {
			t.Fatalf("schema.json missing property %q", name)
		}
	}
	// The validator honors every schema field, not a subset.
	card := validCard()
	delete(card, "created")
	card["aliases"] = "not-an-array"
	got := map[string]string{}
	for _, e := range ValidateCard("memory/cards/notes/x.md", card) {
		got[e.Field] = e.Rule
	}
	if got["created"] != "required" || got["aliases"] != "type" {
		t.Fatalf("schema fields not enforced: %v", got)
	}
}

// The file contract is real: every required name and every declared property
// rule in schema.json is enforced by ValidateCard, not a subset.
func TestCardSchemaParity(t *testing.T) {
	for _, name := range []string{"type", "status", "tags", "created", "updated"} {
		card := validCard()
		delete(card, name)
		found := false
		for _, e := range ValidateCard("memory/cards/notes/x.md", card) {
			if e.Field == name && e.Rule == "required" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing required field %q not reported", name)
		}
	}
	for name, bad := range map[string]any{
		"source": 42, "tags": []any{42}, "aliases": true,
		"created": "not-a-date", "updated": "32/13/9999",
	} {
		card := validCard()
		card[name] = bad
		found := false
		for _, e := range ValidateCard("memory/cards/notes/x.md", card) {
			if e.Field == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("invalid %s not reported", name)
		}
	}
}
