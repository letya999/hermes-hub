// Package locale owns the hub's user-facing message catalogs: a small typed
// Lang set, per-language string tables and the single T() render point every
// deterministic reply goes through. The package is a leaf — it imports only
// the standard library so commodel, communication, toolhub and cmd can all
// depend on it.
package locale

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Lang is a supported user interface language.
type Lang string

const (
	LangEN Lang = "en"
	LangRU Lang = "ru"
	LangKK Lang = "kk"
	LangES Lang = "es"
)

// Default answers when nothing better is known about the user.
const Default = LangEN

// Fallback is the language used when neither preference nor channel evidence
// says otherwise. HUB_DEFAULT_LANG lets an operator run a non-English hub;
// unset or unsupported values keep Default.
func Fallback() Lang {
	if l, ok := Parse(os.Getenv("HUB_DEFAULT_LANG")); ok {
		return l
	}
	return Default
}

// All lists supported languages in picker order.
var All = []Lang{LangEN, LangRU, LangKK, LangES}

// Names are the languages' self-names, used for picker buttons — always the
// native name regardless of the current UI language.
var Names = map[Lang]string{
	LangEN: "English",
	LangRU: "Русский",
	LangKK: "Қазақша",
	LangES: "Español",
}

// supported lists every Lang with a UI translation, independent of whether a
// catalog file registered yet.
var supported = map[Lang]bool{LangEN: true, LangRU: true, LangKK: true, LangES: true}

// Parse resolves an IETF-ish tag ("en", "ru-RU", "kk_KZ") to a supported Lang.
func Parse(tag string) (Lang, bool) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	l := Lang(tag)
	return l, supported[l]
}

// Negotiate resolves an HTTP Accept-Language header to a supported Lang by
// q-value order; tags without q count as 1.0 and "*" or unsupported tags are
// skipped. ok is false when nothing usable was offered.
func Negotiate(header string) (Lang, bool) {
	type choice struct {
		lang Lang
		q    float64
	}
	var choices []choice
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if i := strings.Index(part, ";"); i >= 0 {
			tag = strings.TrimSpace(part[:i])
			for _, param := range strings.Split(part[i+1:], ";") {
				param = strings.TrimSpace(param)
				if strings.HasPrefix(param, "q=") {
					if v, err := strconv.ParseFloat(param[2:], 64); err == nil {
						q = v
					}
				}
			}
		}
		if tag == "*" || q <= 0 {
			continue
		}
		if l, ok := Parse(tag); ok {
			choices = append(choices, choice{l, q})
		}
	}
	if len(choices) == 0 {
		return "", false
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].q > choices[j].q })
	return choices[0].lang, true
}

// T renders key in lang with fmt.Sprintf arguments. A missing translation
// falls back to English; a missing key returns the key itself so the gap is
// visible instead of silent.
func T(lang Lang, key string, args ...any) string {
	format, ok := catalogs[lang][key]
	if !ok {
		if format, ok = catalogs[Default][key]; !ok {
			return key
		}
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// Lookup reports whether lang's own catalog carries key — no fallback — so
// tests and callers can distinguish a real translation from T's
// missing-key echo.
func Lookup(lang Lang, key string) (string, bool) {
	s, ok := catalogs[lang][key]
	return s, ok
}

// catalogs indexes every language table; populated by strings_<lang>.go via
// init so registration order cannot lose a file.
var catalogs = map[Lang]map[string]string{}

func register(lang Lang, table map[string]string) {
	catalogs[lang] = table
}
