package serviceauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestParseTokenEnvelopes(t *testing.T) {
	good := `{"` + strings.Repeat("a", 32) + `":{"identity_schema":1,"principal_id":"alice","external_identity_id":"telegram-11","context_id":"alice","runtime_id":"alice","conversation_id":"telegram-11","delivery_target_id":"telegram-11","policy_version":"policy-1"}}`
	entries, err := ParseTokenEnvelopes([]byte(good))
	if err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	env := entries[strings.Repeat("a", 32)]
	if env.PrincipalID != "alice" {
		t.Fatalf("envelope: %+v", env)
	}
	for _, test := range []struct{ name, body string }{
		{"bad json", `not-json`},
		{"empty token", `{"":{}}`},
		{"crlf token", `{"tok` + "\r\n" + `en":{}}`},
		{"invalid envelope", `{"` + strings.Repeat("b", 32) + `":{"identity_schema":1,"principal_id":"alice"}}`},
		{"one bad entry poisons file", good[:len(good)-1] + `,"short":{"identity_schema":1,"principal_id":"alice"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseTokenEnvelopes([]byte(test.body)); err == nil {
				t.Fatal("invalid file accepted")
			}
		})
	}
}

func TestScopedTokenEnvelope(t *testing.T) {
	token := strings.Repeat("c", 32)
	body := `{"` + token + `":{"identity_schema":1,"principal_id":"hermes-admin","external_identity_id":"mock-1","context_id":"hermes-admin","runtime_id":"hermes-admin","conversation_id":"mock-1","delivery_target_id":"mock-1","policy_version":"policy-1","scopes":["admin"]}}`
	entries, err := ParseTokenEnvelopes([]byte(body))
	if err != nil {
		t.Fatalf("scoped envelope rejected: %v", err)
	}
	env := entries[token]
	if !env.HasScope("admin") || env.HasScope("write") {
		t.Fatalf("scopes: %+v", env.Scopes)
	}
	for _, test := range []struct{ name, scopes string }{
		{"bad scope token", `"scopes":["Admin"]`},
		{"scope with space", `"scopes":["adm in"]`},
		{"too many scopes", `"scopes":["a","b","c","d","e","f","g","h","i"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := strings.Replace(body, `"scopes":["admin"]`, test.scopes, 1)
			if _, err := ParseTokenEnvelopes([]byte(bad)); err == nil {
				t.Fatal("invalid scopes accepted")
			}
		})
	}
}

func TestReadTokenEnvelopes(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadTokenEnvelopes(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, []byte(strings.Repeat(" ", MaxTokensFile+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenEnvelopes(big); err == nil {
		t.Fatal("oversized file accepted")
	}
	path := filepath.Join(dir, "tokens.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadTokenEnvelopes(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestEnrolledPolicyAcceptsOrgContextAndRejectsAmbiguity(t *testing.T) {
	env := identity.Envelope{PrincipalID: "bob", ContextID: "team", RuntimeID: "bob", PolicyVersion: "policy-current"}
	entries := map[string]identity.Envelope{"one": env}
	if got, ok := EnrolledPolicy(entries, "bob", "bob"); !ok || got != "policy-current" {
		t.Fatal("org-scoped enrollment was not selected")
	}
	entries["two"] = env
	if _, ok := EnrolledPolicy(entries, "bob", "bob"); ok {
		t.Fatal("ambiguous enrollment accepted")
	}
	if _, ok := EnrolledPolicy(map[string]identity.Envelope{"one": env}, "bob", "alice"); ok {
		t.Fatal("foreign runtime accepted")
	}
}
