package vault

import (
	"fmt"
	"regexp"
	"slices"
)

// CardError is one structured card-schema violation: which field broke which
// rule. Violations are reported, never repaired.
type CardError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

func (e CardError) Error() string { return "card: " + e.Field + " violates " + e.Rule }

// cardSchema is the vault's card contract — the same document the scaffold
// writes to memory/schema.json (SPEC-0085 R1.2): the typed frontmatter every
// card under memory/cards/ and memory/summaries/ carries.
const cardSchema = `{
	"$schema": "http://json-schema.org/draft-07/schema#",
	"type": "object",
	"required": ["type", "status", "tags", "created", "updated"],
	"properties": {
		"type":       {"enum": ["note", "contact", "project", "idea", "decision", "daily-summary", "weekly-summary", "monthly-summary", "yearly-summary"]},
		"status":     {"enum": ["active", "draft", "inactive", "superseded", "archived", "explored", "done", "paused", "reverted", "cancelled"]},
		"confidence": {"type": "number", "minimum": 0, "maximum": 1},
		"source":     {"type": "string"},
		"tags":       {"type": "array", "items": {"type": "string"}},
		"aliases":    {"type": "array", "items": {"type": "string"}},
		"created":    {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
		"updated":    {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}
	}
}`

var cardTypes = []string{"note", "contact", "project", "idea", "decision", "daily-summary", "weekly-summary", "monthly-summary", "yearly-summary"}

var cardStatuses = []string{"active", "draft", "inactive", "superseded", "archived", "explored", "done", "paused", "reverted", "cancelled"}

var cardRequired = []string{"type", "status", "tags", "created", "updated"}

var cardDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// cardStringArray reports whether v is an array whose items are all strings.
func cardStringArray(v any) bool {
	arr, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range arr {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

// ValidateCard checks a card document (parsed frontmatter) against the vault
// schema. It returns one CardError per violated field — the caller reports
// them; nothing is repaired.
func ValidateCard(path string, doc map[string]any) []CardError {
	var errs []CardError
	for _, name := range cardRequired {
		if _, ok := doc[name]; !ok {
			errs = append(errs, CardError{Field: name, Rule: "required"})
		}
	}
	if t, ok := doc["type"]; ok && !slices.Contains(cardTypes, fmt.Sprintf("%v", t)) {
		errs = append(errs, CardError{Field: "type", Rule: "enum"})
	}
	if st, ok := doc["status"]; ok && !slices.Contains(cardStatuses, fmt.Sprintf("%v", st)) {
		errs = append(errs, CardError{Field: "status", Rule: "enum"})
	}
	if c, ok := doc["confidence"]; ok {
		if v, isNum := c.(float64); !isNum || v < 0 || v > 1 {
			errs = append(errs, CardError{Field: "confidence", Rule: "range"})
		}
	}
	if a, ok := doc["aliases"]; ok {
		if _, isArr := a.([]any); !isArr {
			errs = append(errs, CardError{Field: "aliases", Rule: "type"})
		}
	}
	if s, ok := doc["source"]; ok {
		if _, isStr := s.(string); !isStr {
			errs = append(errs, CardError{Field: "source", Rule: "type"})
		}
	}
	if t2, ok := doc["tags"]; ok {
		if !cardStringArray(t2) {
			errs = append(errs, CardError{Field: "tags", Rule: "type"})
		}
	}
	for _, name := range []string{"created", "updated"} {
		if v, ok := doc[name]; ok {
			if s, isStr := v.(string); !isStr || !cardDate.MatchString(s) {
				errs = append(errs, CardError{Field: name, Rule: "format"})
			}
		}
	}
	return errs
}
