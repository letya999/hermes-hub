package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"unicode"

	"github.com/letya999/credential-broker/internal/strictjson"
)

func TestShippedContractsAreValid(t *testing.T) {
	files, e := filepath.Glob("../examples/contracts/*.json")
	if e != nil {
		t.Fatal(e)
	}
	if len(files) < 5 {
		t.Fatal("missing reviewed examples")
	}
	for _, name := range files {
		t.Run(filepath.Base(name), func(t *testing.T) {
			raw, e := os.ReadFile(name)
			if e != nil {
				t.Fatal(e)
			}
			var c Contract
			if e = strictjson.Decode(raw, &c); e != nil {
				t.Fatal(e)
			}
			if e = c.Validate(); e != nil {
				t.Fatal(e)
			}
			// Shipped examples follow the prepared-catalog convention: the base
			// title/labels are English prose, ru/kk/es translations ride in the
			// i18n maps. Identifier-only labels (env names) are exempt.
			for _, s := range append([]string{c.Title}, fieldLabels(c)...) {
				if hasCyrillic(s) {
					t.Fatalf("base text must be English: %q", s)
				}
			}
			for _, lang := range []string{"ru", "kk", "es"} {
				if c.TitleI18n[lang] == "" {
					t.Fatalf("%s title_i18n missing %q", c.ID, lang)
				}
				for _, f := range c.Fields {
					if isEnvName(f.Label) {
						continue
					}
					if f.LabelI18n[lang] == "" {
						t.Fatalf("%s field %s label_i18n missing %q", c.ID, f.ID, lang)
					}
				}
			}
		})
	}
}

var exampleEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func isEnvName(label string) bool { return exampleEnvName.MatchString(label) }

func fieldLabels(c Contract) []string {
	labels := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		labels = append(labels, f.Label)
	}
	return labels
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
