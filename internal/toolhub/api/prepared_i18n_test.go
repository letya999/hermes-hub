package api

import (
	"regexp"
	"testing"
	"unicode"
)

var contractEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Reviewed prepared contracts render through the broker browser form, so every
// prose label ships translations for each supported UI language while the base
// text stays English. Identifier-only labels such as env names carry no prose
// and keep no translation table.
func TestPreparedContractsCarryLocalizations(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatalf("prepared catalog: %v", err)
	}
	checked := map[string]bool{}
	for _, entry := range entries {
		if entry.ContractID == "" || checked[entry.ContractID] {
			continue
		}
		c, err := entry.BrokerContract()
		if err != nil {
			t.Fatalf("%s contract %s: %v", entry.ID, entry.ContractID, err)
		}
		checked[entry.ContractID] = true
		for _, lang := range []string{"ru", "kk", "es"} {
			if c.TitleI18n[lang] == "" {
				t.Errorf("%s: title_i18n[%s] missing", c.ID, lang)
			}
		}
		if hasCyrillic(c.Title) {
			t.Errorf("%s: base title not English: %q", c.ID, c.Title)
		}
		for _, f := range c.Fields {
			if hasCyrillic(f.Label) {
				t.Errorf("%s.%s: base label not English: %q", c.ID, f.ID, f.Label)
			}
			if contractEnvName.MatchString(f.Label) {
				continue
			}
			for _, lang := range []string{"ru", "kk", "es"} {
				if f.LabelI18n[lang] == "" {
					t.Errorf("%s.%s: label_i18n[%s] missing", c.ID, f.ID, lang)
				}
			}
		}
	}
	if len(checked) == 0 {
		t.Fatal("no prepared contracts checked")
	}
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
