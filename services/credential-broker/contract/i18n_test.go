package contract

import (
	"strings"
	"testing"
)

// LabelI18n/TitleI18n let a reviewed contract carry deployer-declared
// translations: exact tag wins, region variants fall back to their base tag,
// unknown languages keep the base text.
func TestI18nResolution(t *testing.T) {
	c := Contract{ID: "c1", Revision: 1, Title: "GitHub PAT", TitleI18n: map[string]string{
		"ru": "GitHub: токен", "kk": "GitHub токені", "es": "GitHub: token personal"},
		Storage: "local",
		Fields: []Field{{ID: "token", Label: "GitHub personal access token", LabelI18n: map[string]string{
			"ru": "Персональный токен GitHub", "es": "Token de acceso personal de GitHub"}, Kind: "secret", Required: true, MaxBytes: 64}},
		Deliveries: []Delivery{{Type: "env", Field: "token", Target: "TOKEN"}}}
	if got := c.TitleFor("ru"); got != "GitHub: токен" {
		t.Fatalf("ru title: %q", got)
	}
	if got := c.TitleFor("es-ES"); got != "GitHub: token personal" {
		t.Fatalf("es-ES must normalize to es: %q", got)
	}
	if got := c.TitleFor("fr"); got != "GitHub PAT" {
		t.Fatalf("unsupported lang must fall back to base: %q", got)
	}
	if got := c.TitleFor(""); got != "GitHub PAT" {
		t.Fatalf("empty lang must fall back to base: %q", got)
	}
	f := c.Fields[0]
	if got := f.LabelFor("kk"); got != "GitHub personal access token" {
		t.Fatalf("missing kk label must fall back: %q", got)
	}
	if got := f.LabelFor("RU-ru"); got != "Персональный токен GitHub" {
		t.Fatalf("case/region must normalize: %q", got)
	}
}

// i18n maps are bounded like the texts they translate: a handful of tags,
// plain-language keys, values under the label/title bound.
func TestI18nValidation(t *testing.T) {
	base := func() Contract {
		return Contract{ID: "c1", Revision: 1, Title: "T", Storage: "local",
			Fields:     []Field{{ID: "token", Label: "L", Kind: "secret", Required: true, MaxBytes: 64}},
			Deliveries: []Delivery{{Type: "env", Field: "token", Target: "TOKEN"}}}
	}
	c := base()
	c.TitleI18n = map[string]string{"ru": "Т", "es-ES": "T"}
	c.Fields[0].LabelI18n = map[string]string{"kk": "Л"}
	if c.Validate() != nil {
		t.Fatal("valid i18n maps rejected")
	}
	bad := []map[string]string{
		{"not a lang": "x"},
		{"ru": ""},
		{"en": strings.Repeat("x", 241)},
		{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9"},
	}
	for _, m := range bad {
		c = base()
		c.TitleI18n = m
		if c.Validate() == nil {
			t.Fatalf("bad title_i18n accepted: %v", m)
		}
		c = base()
		c.Fields[0].LabelI18n = m
		if c.Validate() == nil {
			t.Fatalf("bad label_i18n accepted: %v", m)
		}
	}
	// Request-scoped extras follow the same field rules.
	for _, m := range bad {
		e := &Extra{Fields: []Field{{ID: "opt", Label: "L", LabelI18n: m, Kind: "string", MaxBytes: 64}},
			Deliveries: []Delivery{{Type: "env", Field: "opt", Target: "OPT"}}}
		if e.Validate() == nil {
			t.Fatalf("bad extra label_i18n accepted: %v", m)
		}
	}
}
