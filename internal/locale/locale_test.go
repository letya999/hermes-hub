package locale

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Lookup reports whether a specific language's catalog carries a key — the
// presence check T's missing-key fallback deliberately hides.
func TestLookupReportsPresence(t *testing.T) {
	if s, ok := Lookup(LangES, "lang.saved"); !ok || !strings.Contains(s, "%s") {
		t.Fatalf("es lang.saved lookup = %q, %v", s, ok)
	}
	if _, ok := Lookup(LangES, "never.a.real.key"); ok {
		t.Fatal("Lookup returned true for an unknown key")
	}
}

// TestCatalogParity is the completeness contract: every language table carries
// the same key set and every entry has the same printf-verb multiset, so a
// translated string can never fail to substitute.
func TestCatalogParity(t *testing.T) {
	verb := regexp.MustCompile(`%[+#\-0-9.]*([a-zA-Z])`)
	en := catalogs[Default]
	if len(en) == 0 {
		t.Fatal("english catalog is empty")
	}
	for _, lang := range All {
		table := catalogs[lang]
		if lang == Default {
			continue
		}
		if table == nil {
			t.Fatalf("%s catalog not registered", lang)
		}
		for key, format := range en {
			got, ok := table[key]
			if !ok {
				t.Errorf("%s catalog missing key %q", lang, key)
				continue
			}
			enVerbs := verb.FindAllStringSubmatch(format, -1)
			gotVerbs := verb.FindAllStringSubmatch(got, -1)
			if len(enVerbs) != len(gotVerbs) {
				t.Errorf("%s key %q verb count %d != en %d", lang, key, len(gotVerbs), len(enVerbs))
				continue
			}
			for i := range enVerbs {
				if enVerbs[i][1] != gotVerbs[i][1] {
					t.Errorf("%s key %q verb %d: %q vs en %q", lang, key, i, gotVerbs[i][1], enVerbs[i][1])
				}
			}
		}
		for key := range table {
			if _, ok := en[key]; !ok {
				t.Errorf("%s catalog has extra key %q absent from en", lang, key)
			}
		}
	}
}

func TestParse(t *testing.T) {
	for tag, want := range map[string]string{"en": "en", "ru-RU": "ru", "kk_KZ": "kk", "ES": "es", " kk ": "kk", "en-US-x": "en"} {
		got, ok := Parse(tag)
		if !ok || got != Lang(want) {
			t.Fatalf("Parse(%q) = %q,%v want %q", tag, got, ok, want)
		}
	}
	for _, tag := range []string{"", "de", "-ru", "de-CH"} {
		if _, ok := Parse(tag); ok {
			t.Fatalf("Parse(%q) unexpectedly ok", tag)
		}
	}
}

func TestTFallbackAndArgs(t *testing.T) {
	if got := T(LangEN, "lang.choose"); got != catalogs[LangEN]["lang.choose"] {
		t.Fatalf("en render = %q", got)
	}
	// Unknown language falls back to English.
	if got := T(Lang("de"), "lang.choose"); got != catalogs[LangEN]["lang.choose"] {
		t.Fatalf("missing lang did not fall back: %q", got)
	}
	// Missing key returns the key itself.
	if got := T(LangEN, "no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key returned %q", got)
	}
	// Args substitute.
	if got := T(LangEN, "lang.saved", "English"); got == "lang.saved" || !strings.Contains(got, "English") {
		t.Fatalf("args not substituted: %q", got)
	}
}

// A user can pick a language before any run has materialized their workspace:
// persisting must create the directory, otherwise first-contact persistence
// silently no-ops and the picker would re-appear on every message.
func TestStorePrefsCreatesWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet-created", "workspace")
	if !StoreResult(dir, LangKK, "detected") {
		t.Fatal("StoreResult into fresh workspace failed")
	}
	if p := LoadPrefs(dir); p.Lang != LangKK || p.Source != SourceDetected {
		t.Fatalf("prefs = %+v", p)
	}
}

func TestPrefsRoundTripAndLadder(t *testing.T) {
	dir := t.TempDir()
	if p := LoadPrefs(dir); p.Lang != "" || p.Source != "" {
		t.Fatalf("empty dir returned prefs %+v", p)
	}
	if LangOf(dir) != Default {
		t.Fatalf("empty dir lang = %q", LangOf(dir))
	}
	// No prefs, no hint → picker.
	if lang, action := Resolve(dir, ""); lang != Default || action != "picker" {
		t.Fatalf("fresh resolve = %q,%q", lang, action)
	}
	// No prefs, strong hint → detected.
	if lang, action := Resolve(dir, "kk-KZ"); lang != LangKK || action != "detected" {
		t.Fatalf("hinted resolve = %q,%q", lang, action)
	}
	if !StoreResult(dir, LangKK, "detected") {
		t.Fatal("StoreResult detected did not persist")
	}
	p := LoadPrefs(dir)
	if p.Lang != LangKK || p.Source != SourceDetected {
		t.Fatalf("prefs = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, PrefsFile)); err != nil {
		t.Fatalf("prefs file missing: %v", err)
	}
	// Detected persists, later resolves stay silent.
	if lang, action := Resolve(dir, ""); lang != LangKK || action != "" {
		t.Fatalf("detected resolve = %q,%q", lang, action)
	}
	// A stored default may be upgraded by a later hint.
	dir2 := t.TempDir()
	if !StoreResult(dir2, LangEN, "picker") {
		t.Fatal("picker result must persist as default")
	}
	if p := LoadPrefs(dir2); p.Lang != LangEN || p.Source != SourceDefault {
		t.Fatalf("picker persist = %+v", p)
	}
	// A stored default answers silently without a hint.
	if lang, action := Resolve(dir2, ""); lang != LangEN || action != "" {
		t.Fatalf("stored default resolve = %q,%q", lang, action)
	}
	if lang, action := Resolve(dir2, "ru"); lang != LangRU || action != "detected" {
		t.Fatalf("default upgrade = %q,%q", lang, action)
	}
	// A chosen language is never overridden.
	StoreResult(dir, LangES, "chosen")
	if lang, action := Resolve(dir, "en"); lang != LangES || action != "" {
		t.Fatalf("chosen resolve = %q,%q", lang, action)
	}
	if LangOf(dir) != LangES {
		t.Fatalf("LangOf stored = %q", LangOf(dir))
	}
	// A corrupt or unsupported stored language never wins.
	dir3 := t.TempDir()
	if err := StorePrefs(dir3, Prefs{Lang: Lang("xx"), Source: SourceChosen}); err != nil {
		t.Fatal(err)
	}
	if LangOf(dir3) != Default {
		t.Fatalf("corrupt LangOf = %q", LangOf(dir3))
	}
	if lang, action := Resolve(dir3, "kk"); lang != LangKK || action != "detected" {
		t.Fatalf("corrupt-lang resolve = %q,%q", lang, action)
	}
	if StoreResult(dir, LangEN, "") {
		t.Fatal("empty action must not persist")
	}
	if err := StorePrefs("", Prefs{Lang: LangEN, Source: SourceChosen}); err == nil {
		t.Fatal("empty workspace dir must error")
	}
}

// TestEnglishKeysPresent pins the catalog surface the gateway migrates to:
// every deterministic user-facing string has a canonical key.
func TestEnglishKeysPresent(t *testing.T) {
	for _, key := range []string{
		// Language picker and /language.
		"lang.choose", "lang.saved", "lang.detected", "lang.status", "lang.unknown",
		// Command menu descriptions.
		"menu.desc.start", "menu.desc.new", "menu.desc.sessions", "menu.desc.use",
		"menu.desc.delete", "menu.desc.session", "menu.desc.status", "menu.desc.routines",
		"menu.desc.usage", "menu.desc.compact", "menu.desc.goal", "menu.desc.model",
		"menu.desc.connectors", "menu.desc.connections", "menu.desc.watches",
		"menu.desc.assignments", "menu.desc.assignment",
		"menu.desc.credentials", "menu.desc.approve", "menu.desc.stop",
		"menu.desc.cancel", "menu.desc.archive", "menu.desc.archived", "menu.desc.language",
		// Gateway replies.
		"chat.no-access", "chat.edit-norestart", "chat.task-open-failed",
		"chat.group-no-secrets", "chat.unknown-command", "chat.working",
		"chat.approve-usage", "chat.approve-denied", "chat.approve-saved",
		"chat.approve-scope", "chat.creds-usage", "chat.creds-saved",
		"chat.creds-expired", "chat.started", "chat.cancel-usage",
		"chat.cancel-state", "chat.cancel-scope", "chat.routines-unavail",
		"chat.routines-title", "chat.routines-empty", "chat.usage-busy",
		"chat.usage-measuring", "chat.secrets-rejected", "chat.secrets-form",
		"chat.bot-conflict", "chat.auto-titled",
		"chat.skill-unavailable", "chat.skill-active",
		// Assignment replies.
		"assignment.list-unavailable", "assignment.list-title", "assignment.wakes-today",
		"assignment.list-empty", "assignment.usage", "assignment.unavailable",
		"assignment.unavailable-source",
		"assignment.status", "assignment.unknown-verb", "assignment.rejected",
		"assignment.accepted",
		// Routine listing fragments.
		"routine.active", "routine.paused", "routine.done", "routine.missed",
		"routine.unconfirmed", "routine.delivered", "routine.no-upcoming",
		// Duration formatting.
		"time.sec", "time.min", "time.hour",
	} {
		if _, ok := catalogs[LangEN][key]; !ok {
			t.Errorf("en catalog missing key %q", key)
		}
	}
}

// HUB_DEFAULT_LANG lets an operator set the hub-wide fallback for users with
// no preference and no channel evidence; unsupported values keep English.
func TestFallbackEnv(t *testing.T) {
	if Fallback() != Default {
		t.Fatalf("unset env fallback = %q", Fallback())
	}
	t.Setenv("HUB_DEFAULT_LANG", "kk-KZ")
	if Fallback() != LangKK {
		t.Fatalf("env fallback = %q", Fallback())
	}
	dir := t.TempDir()
	if lang, action := Resolve(dir, ""); lang != LangKK || action != "picker" {
		t.Fatalf("resolve under env fallback = %q,%q", lang, action)
	}
	if LangOf(dir) != LangKK {
		t.Fatalf("LangOf under env fallback = %q", LangOf(dir))
	}
	t.Setenv("HUB_DEFAULT_LANG", "xx")
	if Fallback() != Default {
		t.Fatalf("bad env fallback = %q", Fallback())
	}
}

// Operator tooling (hubctl, scripts) has no workspace, so its language comes
// from the operator's own environment: explicit HUB_LANG wins, then the
// standard LC_ALL/LANG ladder, then the hub fallback.
func TestOperatorLang(t *testing.T) {
	t.Setenv("HUB_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
	if Operator() != Fallback() {
		t.Fatalf("empty env = %q, want fallback %q", Operator(), Fallback())
	}
	t.Setenv("LANG", "kk_KZ.UTF-8")
	if Operator() != LangKK {
		t.Fatalf("LANG = %q", Operator())
	}
	t.Setenv("LC_ALL", "es-ES")
	if Operator() != LangES {
		t.Fatalf("LC_ALL beats LANG = %q", Operator())
	}
	t.Setenv("HUB_LANG", "ru-RU")
	if Operator() != LangRU {
		t.Fatalf("HUB_LANG wins = %q", Operator())
	}
	t.Setenv("HUB_LANG", "xx")
	if Operator() != LangES {
		t.Fatalf("unknown HUB_LANG falls through = %q", Operator())
	}
}

func TestNamesCoverAll(t *testing.T) {
	for _, l := range All {
		if Names[l] == "" {
			t.Fatalf("lang %q has no native name", l)
		}
	}
}

func TestNegotiate(t *testing.T) {
	cases := []struct {
		header string
		want   Lang
		ok     bool
	}{
		{"", "", false},
		{"en-US,en;q=0.9", LangEN, true},
		{"ru;q=0.8,kk;q=0.9", LangKK, true},
		{"fr-FR,fr;q=0.9", "", false},
		{"es-ES,es;q=0.8,en;q=0.5", LangES, true},
		{"de, ru", LangRU, true},
		{"*;q=0.1", "", false},
		{"en;q=0", "", false},
	}
	for _, c := range cases {
		got, ok := Negotiate(c.header)
		if got != c.want || ok != c.ok {
			t.Errorf("Negotiate(%q)=%q,%v want %q,%v", c.header, got, ok, c.want, c.ok)
		}
	}
}
